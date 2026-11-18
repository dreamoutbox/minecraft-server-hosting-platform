package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/storage"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// WorkerClientFactory instantiates a worker client targeting a specific host.
type WorkerClientFactory func(baseURL string, apiKey string) worker.Client

// Service coordinates world snapshot archives, restorations, and automated cron sweeps.
type Service struct {
	db                  *gorm.DB
	storage             storage.StorageProvider
	workerClientFactory WorkerClientFactory
	cronRunner          *cron.Cron
}

// NewService instantiates a backup and disaster recovery coordination service.
//
// Arguments:
//   - db: Active database handle.
//   - storage: Object storage provider for persistence.
//   - factory: Worker client factory; defaults to HTTP worker client if nil.
//
// Example usage:
//
//	svc := backup.NewService(db, storageProvider, nil)
func NewService(db *gorm.DB, storage storage.StorageProvider, factory WorkerClientFactory) *Service {
	if factory == nil {
		factory = func(baseURL string, apiKey string) worker.Client {
			return worker.NewClient(baseURL, apiKey)
		}
	}

	return &Service{
		db:                  db,
		storage:             storage,
		workerClientFactory: factory,
		cronRunner:          cron.New(),
	}
}

// BackupServer captures a fresh volume archive from the host worker and saves it in object storage.
// It creates a BackupRecord in the database tracking size, timestamp, and storage key.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Primary key of the target game server.
//
// Example usage:
//
//	rec, err := svc.BackupServer(ctx, 42)
func (s *Service) BackupServer(ctx context.Context, serverID uint) (*domain.BackupRecord, error) {
	record, err := s.CreatePendingBackup(ctx, serverID)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpBackup, domain.ServerLogStatusError, err.Error())
		return nil, err
	}
	return s.ExecuteBackupRecord(ctx, 0, serverID, record.ID)
}

// CreatePendingBackup inserts a pending BackupRecord before async queue execution.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	rec, err := svc.CreatePendingBackup(ctx, 42)
func (s *Service) CreatePendingBackup(ctx context.Context, serverID uint) (*domain.BackupRecord, error) {
	var server domain.Server
	if err := s.db.WithContext(ctx).First(&server, serverID).Error; err != nil {
		return nil, fmt.Errorf("failed fetching server %d: %w", serverID, err)
	}

	storageKey := fmt.Sprintf("backups/server-%d/%d.tar", server.ID, time.Now().UnixNano())
	record := domain.BackupRecord{
		ServerID:   server.ID,
		StorageKey: storageKey,
		SizeBytes:  0,
		Status:     domain.BackupStatusPending,
	}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("failed creating pending backup record: %w", err)
	}
	return &record, nil
}

// ExecuteBackupRecord exports the volume for serverID, uploads it to storage, and updates record status.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Initiating user ID (defaults to server owner if 0).
//   - serverID: Target game server ID.
//   - backupRecordID: Pending or failed BackupRecord primary key.
//
// Example usage:
//
//	rec, err := svc.ExecuteBackupRecord(ctx, user.ID, 42, rec.ID)
func (s *Service) ExecuteBackupRecord(ctx context.Context, userID uint, serverID uint, backupRecordID uint) (*domain.BackupRecord, error) {
	var server domain.Server
	err := s.db.WithContext(ctx).
		Preload("Host").
		Preload("Plan").
		First(&server, serverID).Error
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, userID, domain.ServerOpBackup, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("failed fetching server %d: %w", serverID, err)
	}
	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	var record domain.BackupRecord
	if err := s.db.WithContext(ctx).First(&record, backupRecordID).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, actorID, domain.ServerOpBackup, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("backup record %d not found: %w", backupRecordID, err)
	}
	if record.ServerID != server.ID {
		err := errors.New("backup record does not belong to specified server")
		domain.RecordServerLog(ctx, s.db, serverID, actorID, domain.ServerOpBackup, domain.ServerLogStatusError, err.Error())
		return nil, err
	}

	markFailed := func(cause error) (*domain.BackupRecord, error) {
		record.Status = domain.BackupStatusFailed
		_ = s.db.WithContext(ctx).Model(&record).Update("status", domain.BackupStatusFailed).Error
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpBackup, domain.ServerLogStatusError, cause.Error())
		return nil, cause
	}

	volID := strconv.FormatUint(uint64(server.ID), 10)
	wClient := s.workerClientFactory(server.Host.WorkerBaseURL, server.Host.APIKey)

	tarStream, err := wClient.ExportVolume(ctx, volID)
	if err != nil {
		return markFailed(fmt.Errorf("failed exporting volume on worker: %w", err))
	}
	defer func() { _ = tarStream.Close() }()

	tarBytes, err := io.ReadAll(tarStream)
	if err != nil {
		return markFailed(fmt.Errorf("failed reading exported tar bytes: %w", err))
	}

	if err := s.storage.Put(ctx, record.StorageKey, tarBytes); err != nil {
		return markFailed(fmt.Errorf("failed storing backup archive: %w", err))
	}

	record.SizeBytes = int64(len(tarBytes))
	record.Status = domain.BackupStatusCompleted
	if err := s.db.WithContext(ctx).Save(&record).Error; err != nil {
		return markFailed(fmt.Errorf("failed saving backup record: %w", err))
	}

	domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpBackup, domain.ServerLogStatusSuccess, fmt.Sprintf("backup #%d completed (%d bytes)", record.ID, record.SizeBytes))
	return &record, nil
}

// ValidateRestoreTarget verifies that backupRecordID exists and belongs to serverID.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//   - backupRecordID: Candidate backup record ID.
//
// Example usage:
//
//	err := svc.ValidateRestoreTarget(ctx, 42, 105)
func (s *Service) ValidateRestoreTarget(ctx context.Context, serverID uint, backupRecordID uint) error {
	var record domain.BackupRecord
	if err := s.db.WithContext(ctx).First(&record, backupRecordID).Error; err != nil {
		return fmt.Errorf("backup record not found: %w", err)
	}
	if record.ServerID != serverID {
		return errors.New("backup record does not belong to the specified server")
	}
	return nil
}

// RestoreServer unpacks a stored snapshot archive into the server's volume and restarts the server container.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//   - backupRecordID: Primary key of the BackupRecord to restore.
//
// Example usage:
//
//	err := svc.RestoreServer(ctx, 42, 105)
func (s *Service) RestoreServer(ctx context.Context, serverID uint, backupRecordID uint) error {
	var record domain.BackupRecord
	if err := s.db.WithContext(ctx).First(&record, backupRecordID).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("backup record not found: %w", err)
	}

	if record.ServerID != serverID {
		err := errors.New("backup record does not belong to the specified server")
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return err
	}

	var server domain.Server
	err := s.db.WithContext(ctx).
		Preload("Host").
		Preload("Plan").
		First(&server, serverID).Error
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("server not found: %w", err)
	}

	// Pull archive payload from object storage
	archiveBytes, err := s.storage.Get(ctx, record.StorageKey)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed fetching archive from storage: %w", err)
	}

	wClient := s.workerClientFactory(server.Host.WorkerBaseURL, server.Host.APIKey)

	// Step 1: Stop container if active
	if server.ContainerID != "" {
		_ = wClient.StopContainer(ctx, server.ContainerID)
	}

	// Step 2: Unpack tar into volume
	volID := strconv.FormatUint(uint64(server.ID), 10)
	if err := wClient.ImportVolume(ctx, volID, bytes.NewReader(archiveBytes)); err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed importing volume contents on worker: %w", err)
	}

	// Step 3: Restart container
	ctrReq := worker.CreateContainerRequest{
		ServerID:      server.ID,
		Port:          server.Port,
		CPULimit:      server.Plan.CPULimit,
		MemLimitMB:    server.Plan.MemLimitMB,
		DiskLimitGB:   server.Plan.DiskLimitGB,
		MountPath:     server.MountPath,
		ServerType:    string(server.ServerType),
		ServerVersion: server.ServerVersion,
	}

	ctrResp, err := wClient.CreateContainer(ctx, ctrReq)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed restarting container after restore: %w", err)
	}

	server.ContainerID = ctrResp.ContainerID
	server.Status = domain.ServerStatusRunning
	if err := s.db.WithContext(ctx).Save(&server).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed updating server state after restore: %w", err)
	}

	domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusSuccess, fmt.Sprintf("restored from backup #%d", record.ID))
	return nil
}

// ListBackups returns all snapshot records for a given server ordered chronologically descending.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	backups, err := svc.ListBackups(ctx, 42)
func (s *Service) ListBackups(ctx context.Context, serverID uint) ([]domain.BackupRecord, error) {
	var records []domain.BackupRecord
	err := s.db.WithContext(ctx).
		Where("server_id = ?", serverID).
		Order("created_at desc").
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}

// SweepAndBackupAllActive scans for all running game servers and creates fresh snapshots.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	err := svc.SweepAndBackupAllActive(ctx)
func (s *Service) SweepAndBackupAllActive(ctx context.Context) error {
	var activeServers []domain.Server
	err := s.db.WithContext(ctx).
		Where("status = ?", domain.ServerStatusRunning).
		Find(&activeServers).Error
	if err != nil {
		return fmt.Errorf("failed listing active servers for sweep: %w", err)
	}

	for _, srv := range activeServers {
		_, err := s.BackupServer(ctx, srv.ID)
		if err != nil {
			log.Printf("[ERROR] scheduled backup failed for server %d: %v", srv.ID, err)
		}
	}

	return nil
}

// ScheduleBackups registers a recurring cron expression to sweep and archive active servers.
//
// Arguments:
//   - cronSpec: Standard cron schedule string (e.g. "@daily" or "0 2 * * *").
//
// Example usage:
//
//	err := svc.ScheduleBackups("@daily")
func (s *Service) ScheduleBackups(cronSpec string) error {
	_, err := s.cronRunner.AddFunc(cronSpec, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if sweepErr := s.SweepAndBackupAllActive(ctx); sweepErr != nil {
			log.Printf("[ERROR] cron sweep encountered error: %v", sweepErr)
		}
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %s: %w", cronSpec, err)
	}

	s.cronRunner.Start()
	return nil
}

// StopCron halts the background scheduler.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	svc.StopCron()
func (s *Service) StopCron() {
	if s.cronRunner != nil {
		s.cronRunner.Stop()
	}
}
