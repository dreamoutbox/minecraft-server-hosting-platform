package backup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/storage"
)

const (
	// DefaultMaxBackups is the default per-server count of completed backups retained.
	DefaultMaxBackups = 8
	// DefaultPresignTTL is the default validity duration for presigned S3 upload/download URLs.
	DefaultPresignTTL = time.Hour
	backupContentType = "application/gzip"
)

// WorkerClientFactory instantiates a worker client targeting a specific host.
type WorkerClientFactory func(baseURL string, apiKey string) nodemanagerclient.Client

// Service coordinates world snapshot archives, ext4 image backups, and restorations.
type Service struct {
	db                  *gorm.DB
	storage             storage.StorageProvider
	workerClientFactory WorkerClientFactory
	maxBackups          int
	presignTTL          time.Duration
	maxImagePartBytes   int64
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
		factory = func(baseURL string, apiKey string) nodemanagerclient.Client {
			return nodemanagerclient.NewClient(baseURL, apiKey)
		}
	}

	return &Service{
		db:                  db,
		storage:             storage,
		workerClientFactory: factory,
		maxBackups:          DefaultMaxBackups,
		presignTTL:          DefaultPresignTTL,
	}
}

// SetMaxBackups configures the maximum number of completed backups retained per server.
//
// Arguments:
//   - limit: Positive maximum number of completed snapshots to keep per server.
//
// Example usage:
//
//	svc.SetMaxBackups(8)
func (s *Service) SetMaxBackups(limit int) {
	if limit > 0 {
		s.maxBackups = limit
	}
}

// SetPresignTTL configures the expiration window for minted presigned S3 PUT/GET URLs.
//
// Arguments:
//   - ttl: Positive validity duration for presigned URLs.
//
// Example usage:
//
//	svc.SetPresignTTL(time.Hour)
func (s *Service) SetPresignTTL(ttl time.Duration) {
	if ttl > 0 {
		s.presignTTL = ttl
	}
}

// SetMaxImagePartBytes overrides the per-part byte ceiling for ext4 image tar.gz uploads (0 uses worker's 1 GiB default).
//
// Arguments:
//   - maxBytes: Maximum bytes per compressed part (primarily used to test multi-part splits with small sizes).
//
// Example usage:
//
//	svc.SetMaxImagePartBytes(64 * 1024)
func (s *Service) SetMaxImagePartBytes(maxBytes int64) {
	if maxBytes >= 0 {
		s.maxImagePartBytes = maxBytes
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

	storageKey := fmt.Sprintf("backups/server-%d/%d.tar.gz", server.ID, time.Now().UnixNano())
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

	putURL, err := s.storage.PresignPut(ctx, record.StorageKey, s.presignTTL, backupContentType)
	if err != nil {
		return markFailed(fmt.Errorf("failed generating presigned upload URL: %w", err))
	}

	upResp, err := wClient.UploadVolumeBackup(ctx, volID, putURL)
	if err != nil {
		return markFailed(fmt.Errorf("failed uploading backup archive from worker: %w", err))
	}

	record.SizeBytes = upResp.SizeBytes
	record.Status = domain.BackupStatusCompleted
	if err := s.db.WithContext(ctx).Save(&record).Error; err != nil {
		return markFailed(fmt.Errorf("failed saving backup record: %w", err))
	}

	if pruneErr := s.pruneExcessBackups(ctx, server.ID); pruneErr != nil {
		log.Printf("[WARN] failed pruning excess backups for server %d: %v", server.ID, pruneErr)
	}

	domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpBackup, domain.ServerLogStatusSuccess, fmt.Sprintf("backup #%d completed (%d bytes)", record.ID, record.SizeBytes))
	return &record, nil
}

// deleteStorageKeys removes all specified object keys from S3/object storage.
func (s *Service) deleteStorageKeys(ctx context.Context, keys []string) error {
	var errs []error
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := s.storage.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// pruneExcessBackups deletes completed BackupRecord rows and S3 objects beyond s.maxBackups for serverID.
func (s *Service) pruneExcessBackups(ctx context.Context, serverID uint) error {
	if s.maxBackups <= 0 {
		return nil
	}

	var completed []domain.BackupRecord
	if err := s.db.WithContext(ctx).
		Where("server_id = ? AND status = ?", serverID, domain.BackupStatusCompleted).
		Order("created_at desc, id desc").
		Find(&completed).Error; err != nil {
		return fmt.Errorf("failed querying completed backups for pruning: %w", err)
	}
	if len(completed) <= s.maxBackups {
		return nil
	}

	var errs []error
	for _, old := range completed[s.maxBackups:] {
		if err := s.deleteStorageKeys(ctx, []string{old.StorageKey}); err != nil {
			errs = append(errs, fmt.Errorf("backup #%d storage cleanup: %w", old.ID, err))
			continue
		}
		if err := s.db.WithContext(ctx).Delete(&old).Error; err != nil {
			errs = append(errs, fmt.Errorf("backup #%d db delete: %w", old.ID, err))
		}
	}
	return errors.Join(errs...)
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

	// Mint short-lived presigned GET URL so worker downloads archive without S3 credentials
	getURL, err := s.storage.PresignGet(ctx, record.StorageKey, s.presignTTL)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed generating presigned download URL: %w", err)
	}

	wClient := s.workerClientFactory(server.Host.WorkerBaseURL, server.Host.APIKey)

	// Step 1: Stop container if active
	if server.ContainerID != "" {
		_ = wClient.StopContainer(ctx, server.ContainerID)
	}

	// Step 2: Worker downloads and unpacks archive into volume
	volID := strconv.FormatUint(uint64(server.ID), 10)
	if err := wClient.RestoreVolumeBackup(ctx, volID, getURL); err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed restoring volume contents on worker: %w", err)
	}

	// Step 3: Restart container
	ctrReq := nodemanagerclient.CreateContainerRequest{
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
