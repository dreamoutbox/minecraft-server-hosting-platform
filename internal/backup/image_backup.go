package backup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

const minImageBackupPartSlots = 4

// CreatePendingImageBackup inserts a pending ServerImageBackup record prior to async queue execution.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	rec, err := svc.CreatePendingImageBackup(ctx, 42)
func (s *Service) CreatePendingImageBackup(ctx context.Context, serverID uint) (*domain.ServerImageBackup, error) {
	var server domain.Server
	if err := s.db.WithContext(ctx).First(&server, serverID).Error; err != nil {
		return nil, fmt.Errorf("failed fetching server %d: %w", serverID, err)
	}

	prefix := fmt.Sprintf("image-backups/server-%d/%d", server.ID, time.Now().UnixNano())
	record := domain.ServerImageBackup{
		ServerID:      server.ID,
		StoragePrefix: prefix,
		PartKeys:      "[]",
		PartCount:     0,
		SizeBytes:     0,
		Status:        domain.BackupStatusPending,
	}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("failed creating pending image backup record: %w", err)
	}
	return &record, nil
}

// ExecuteImageBackupRecord mints presigned S3 PUT URLs, instructs the host worker to quiesce and
// tar.gz-split the server's ext4 .img file into <= 1 GiB parts, saves uploaded part keys, and
// prunes older completed image backups beyond s.maxBackups.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Initiating user ID (defaults to server owner if 0).
//   - serverID: Target game server ID.
//   - imageBackupID: Primary key of the pending ServerImageBackup record.
//
// Example usage:
//
//	rec, err := svc.ExecuteImageBackupRecord(ctx, 0, 42, rec.ID)
func (s *Service) ExecuteImageBackupRecord(ctx context.Context, userID uint, serverID uint, imageBackupID uint) (*domain.ServerImageBackup, error) {
	var server domain.Server
	err := s.db.WithContext(ctx).
		Preload("Host").
		Preload("Plan").
		First(&server, serverID).Error
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, userID, domain.ServerOpImageBackup, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("failed fetching server %d: %w", serverID, err)
	}
	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	var record domain.ServerImageBackup
	if err := s.db.WithContext(ctx).First(&record, imageBackupID).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, actorID, domain.ServerOpImageBackup, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("image backup record %d not found: %w", imageBackupID, err)
	}
	if record.ServerID != server.ID {
		err := errors.New("image backup record does not belong to specified server")
		domain.RecordServerLog(ctx, s.db, serverID, actorID, domain.ServerOpImageBackup, domain.ServerLogStatusError, err.Error())
		return nil, err
	}

	markFailed := func(cause error) (*domain.ServerImageBackup, error) {
		record.Status = domain.BackupStatusFailed
		_ = s.db.WithContext(ctx).Model(&record).Update("status", domain.BackupStatusFailed).Error
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpImageBackup, domain.ServerLogStatusError, cause.Error())
		return nil, cause
	}

	// Pre-mint enough part slots for worst-case incompressible ext4 image; unused presigned URLs simply expire.
	slotCount := server.Plan.DiskLimitGB + 2
	if slotCount < minImageBackupPartSlots {
		slotCount = minImageBackupPartSlots
	}

	partKeys := make([]string, 0, slotCount)
	putURLs := make([]string, 0, slotCount)
	for i := 1; i <= slotCount; i++ {
		key := fmt.Sprintf("%s/part-%03d.tar.gz", record.StoragePrefix, i)
		putURL, presignErr := s.storage.PresignPut(ctx, key, s.presignTTL, backupContentType)
		if presignErr != nil {
			return markFailed(fmt.Errorf("failed generating presigned upload URL for part %d: %w", i, presignErr))
		}
		partKeys = append(partKeys, key)
		putURLs = append(putURLs, putURL)
	}

	volID := strconv.FormatUint(uint64(server.ID), 10)
	wClient := s.workerClientFactory(server.Host.WorkerBaseURL, server.Host.APIKey)

	req := worker.UploadImageBackupRequest{
		PresignedURLs: putURLs,
		MaxPartBytes:  s.maxImagePartBytes,
	}
	if server.Status == domain.ServerStatusRunning && server.ContainerID != "" {
		req.ContainerID = server.ContainerID
	}

	upResp, err := wClient.UploadImageBackup(ctx, volID, req)
	if err != nil {
		return markFailed(fmt.Errorf("failed uploading ext4 image backup from worker: %w", err))
	}
	if upResp.PartsUploaded <= 0 || upResp.PartsUploaded > len(partKeys) {
		return markFailed(fmt.Errorf("worker reported invalid uploaded part count: %d", upResp.PartsUploaded))
	}

	if err := record.SetPartKeys(partKeys[:upResp.PartsUploaded]); err != nil {
		return markFailed(err)
	}
	record.SizeBytes = upResp.TotalBytes
	record.Status = domain.BackupStatusCompleted
	if err := s.db.WithContext(ctx).Save(&record).Error; err != nil {
		return markFailed(fmt.Errorf("failed saving image backup record: %w", err))
	}

	if pruneErr := s.pruneExcessImageBackups(ctx, server.ID); pruneErr != nil {
		log.Printf("[WARN] failed pruning excess image backups for server %d: %v", server.ID, pruneErr)
	}

	domain.RecordServerLog(
		ctx,
		s.db,
		server.ID,
		actorID,
		domain.ServerOpImageBackup,
		domain.ServerLogStatusSuccess,
		fmt.Sprintf("image backup #%d completed (%d parts, %d bytes)", record.ID, record.PartCount, record.SizeBytes),
	)
	return &record, nil
}

// BackupAllServerImages synchronously backs up ext4 .img files for all provisioned (running or stopped) servers.
// Used as a synchronous fallback when no async job queue is attached.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Initiating admin user ID (0 defaults to each server's owner).
//
// Example usage:
//
//	records, err := svc.BackupAllServerImages(ctx, adminID)
func (s *Service) BackupAllServerImages(ctx context.Context, userID uint) ([]domain.ServerImageBackup, error) {
	var servers []domain.Server
	if err := s.db.WithContext(ctx).
		Where("status IN (?)", []domain.ServerStatus{domain.ServerStatusRunning, domain.ServerStatusStopped}).
		Order("id asc").
		Find(&servers).Error; err != nil {
		return nil, fmt.Errorf("failed listing provisioned servers: %w", err)
	}

	var results []domain.ServerImageBackup
	var errs []error
	for _, srv := range servers {
		pending, err := s.CreatePendingImageBackup(ctx, srv.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("server %d: %w", srv.ID, err))
			continue
		}
		rec, err := s.ExecuteImageBackupRecord(ctx, userID, srv.ID, pending.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("server %d: %w", srv.ID, err))
			continue
		}
		results = append(results, *rec)
	}
	return results, errors.Join(errs...)
}

// ListImageBackups returns all ext4 image backup records for a server ordered newest-first.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	backups, err := svc.ListImageBackups(ctx, 42)
func (s *Service) ListImageBackups(ctx context.Context, serverID uint) ([]domain.ServerImageBackup, error) {
	var records []domain.ServerImageBackup
	err := s.db.WithContext(ctx).
		Where("server_id = ?", serverID).
		Order("created_at desc, id desc").
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}

// ValidateImageRestoreTarget verifies that imageBackupID exists, belongs to serverID, is completed, and has part keys.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//   - imageBackupID: Candidate ServerImageBackup ID.
//
// Example usage:
//
//	err := svc.ValidateImageRestoreTarget(ctx, 42, 7)
func (s *Service) ValidateImageRestoreTarget(ctx context.Context, serverID uint, imageBackupID uint) error {
	var record domain.ServerImageBackup
	if err := s.db.WithContext(ctx).First(&record, imageBackupID).Error; err != nil {
		return fmt.Errorf("image backup record not found: %w", err)
	}
	if record.ServerID != serverID {
		return errors.New("image backup record does not belong to the specified server")
	}
	if record.Status != domain.BackupStatusCompleted {
		return errors.New("only completed image backups can be restored")
	}
	keys, err := record.GetPartKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("image backup record has no stored archive parts")
	}
	return nil
}

// RestoreImageBackup mints presigned GET URLs for all ordered archive parts, stops the server
// container if running, invokes the host worker to download/merge/replace <server-id>.img, and
// restarts the container if it was previously running.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//   - imageBackupID: Primary key of the completed ServerImageBackup to restore.
//
// Example usage:
//
//	err := svc.RestoreImageBackup(ctx, 42, 7)
func (s *Service) RestoreImageBackup(ctx context.Context, serverID uint, imageBackupID uint) error {
	if err := s.ValidateImageRestoreTarget(ctx, serverID, imageBackupID); err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
		return err
	}

	var record domain.ServerImageBackup
	if err := s.db.WithContext(ctx).First(&record, imageBackupID).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("image backup record not found: %w", err)
	}

	partKeys, err := record.GetPartKeys()
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
		return err
	}

	var server domain.Server
	err = s.db.WithContext(ctx).
		Preload("Host").
		Preload("Plan").
		First(&server, serverID).Error
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("server not found: %w", err)
	}

	getURLs := make([]string, 0, len(partKeys))
	for i, key := range partKeys {
		getURL, presignErr := s.storage.PresignGet(ctx, key, s.presignTTL)
		if presignErr != nil {
			domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpImageRestore, domain.ServerLogStatusError, presignErr.Error())
			return fmt.Errorf("failed generating presigned download URL for part %d: %w", i+1, presignErr)
		}
		getURLs = append(getURLs, getURL)
	}

	wClient := s.workerClientFactory(server.Host.WorkerBaseURL, server.Host.APIKey)
	wasRunning := server.Status == domain.ServerStatusRunning

	// Stop active container before replacing the underlying ext4 disk image.
	if server.ContainerID != "" {
		_ = wClient.StopContainer(ctx, server.ContainerID)
	}

	volID := strconv.FormatUint(uint64(server.ID), 10)
	restoreResp, err := wClient.RestoreImageBackup(ctx, volID, getURLs)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed restoring ext4 image on worker: %w", err)
	}

	if wasRunning {
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
			domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
			return fmt.Errorf("failed restarting container after image restore: %w", err)
		}
		server.ContainerID = ctrResp.ContainerID
		server.Status = domain.ServerStatusRunning
		if err := s.db.WithContext(ctx).Save(&server).Error; err != nil {
			domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpImageRestore, domain.ServerLogStatusError, err.Error())
			return fmt.Errorf("failed updating server state after image restore: %w", err)
		}
	}

	domain.RecordServerLog(
		ctx,
		s.db,
		server.ID,
		server.UserID,
		domain.ServerOpImageRestore,
		domain.ServerLogStatusSuccess,
		fmt.Sprintf("restored ext4 image from image backup #%d (%d parts, %d bytes)", record.ID, len(partKeys), restoreResp.SizeBytes),
	)
	return nil
}

// pruneExcessImageBackups deletes completed ServerImageBackup rows and all of their S3 part objects beyond s.maxBackups.
func (s *Service) pruneExcessImageBackups(ctx context.Context, serverID uint) error {
	if s.maxBackups <= 0 {
		return nil
	}

	var completed []domain.ServerImageBackup
	if err := s.db.WithContext(ctx).
		Where("server_id = ? AND status = ?", serverID, domain.BackupStatusCompleted).
		Order("created_at desc, id desc").
		Find(&completed).Error; err != nil {
		return fmt.Errorf("failed querying completed image backups for pruning: %w", err)
	}
	if len(completed) <= s.maxBackups {
		return nil
	}

	var errs []error
	for _, old := range completed[s.maxBackups:] {
		keys, err := old.GetPartKeys()
		if err != nil {
			errs = append(errs, fmt.Errorf("image backup #%d decode part keys: %w", old.ID, err))
			continue
		}
		if err := s.deleteStorageKeys(ctx, keys); err != nil {
			errs = append(errs, fmt.Errorf("image backup #%d storage cleanup: %w", old.ID, err))
			continue
		}
		if err := s.db.WithContext(ctx).Delete(&old).Error; err != nil {
			errs = append(errs, fmt.Errorf("image backup #%d db delete: %w", old.ID, err))
		}
	}
	return errors.Join(errs...)
}
