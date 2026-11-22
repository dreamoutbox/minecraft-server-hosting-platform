package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
)

const (
	// DefaultMaxAttempts defines how many times a queued job is tried before marking it failed.
	DefaultMaxAttempts = 3
	// DefaultPollInterval controls background polling frequency for delayed retries.
	DefaultPollInterval = 1 * time.Second
	// DefaultRetryBackoff sets the base delay multiplied by attempt count before automatic retry.
	DefaultRetryBackoff = 5 * time.Second
)

// Service manages PostgreSQL-backed asynchronous jobs with automatic and manual retry support.
type Service struct {
	db           *gorm.DB
	hostingSvc   *hosting.Service
	backupSvc    *backup.Service
	pollInterval time.Duration
	retryBackoff time.Duration
	wakeCh       chan struct{}
}

// NewService constructs a job queue processor bound to hosting and backup services.
//
// Arguments:
//   - db: Active PostgreSQL database handle.
//   - hostingSvc: Hosting service used for provisioning and deletion jobs.
//   - backupSvc: Backup service used for backup and restore jobs.
//
// Example usage:
//
//	jobSvc := jobs.NewService(db, hostingSvc, backupSvc)
func NewService(db *gorm.DB, hostingSvc *hosting.Service, backupSvc *backup.Service) *Service {
	return &Service{
		db:           db,
		hostingSvc:   hostingSvc,
		backupSvc:    backupSvc,
		pollInterval: DefaultPollInterval,
		retryBackoff: DefaultRetryBackoff,
		wakeCh:       make(chan struct{}, 1),
	}
}

// SetTimings overrides polling interval and retry backoff (primarily for deterministic tests).
//
// Arguments:
//   - pollInterval: Ticker interval for checking delayed retries.
//   - retryBackoff: Base wait duration between automatic retry attempts.
//
// Example usage:
//
//	jobSvc.SetTimings(50*time.Millisecond, 10*time.Millisecond)
func (s *Service) SetTimings(pollInterval, retryBackoff time.Duration) {
	if pollInterval > 0 {
		s.pollInterval = pollInterval
	}
	if retryBackoff >= 0 {
		s.retryBackoff = retryBackoff
	}
}

// EnqueueCreateServer creates a pending server record and queues async provisioning so callers do not block.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Server owner ID.
//   - name: Desired server display name.
//   - planID: Hardware plan tier ID.
//   - serverType: Minecraft server jar type (defaults to VANILLA).
//   - serverVersion: Minecraft version tag (defaults to LATEST).
//
// Example usage:
//
//	srv, job, err := jobSvc.EnqueueCreateServer(ctx, user.ID, "MyWorld", plan.ID, domain.ServerTypeVanilla, "LATEST")
func (s *Service) EnqueueCreateServer(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string) (*domain.Server, *domain.ServerJob, error) {
	return s.EnqueueCreateServerWithExpiry(ctx, userID, name, planID, serverType, serverVersion, nil)
}

// EnqueueCreateServerWithExpiry creates a pending server record with an optional expiration timestamp
// and queues async provisioning so callers do not block.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Server owner ID.
//   - name: Desired server display name.
//   - planID: Hardware plan tier ID.
//   - serverType: Minecraft server jar type (defaults to VANILLA).
//   - serverVersion: Minecraft version tag (defaults to LATEST).
//   - expiresAt: Optional expiration timestamp (nil for non-expiring admin servers).
//
// Example usage:
//
//	exp := time.Now().AddDate(0, 0, 30)
//	srv, job, err := jobSvc.EnqueueCreateServerWithExpiry(ctx, user.ID, "MyWorld", plan.ID, domain.ServerTypeVanilla, "LATEST", &exp)
func (s *Service) EnqueueCreateServerWithExpiry(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string, expiresAt *time.Time) (*domain.Server, *domain.ServerJob, error) {
	server, err := s.hostingSvc.PreparePendingServer(ctx, userID, name, planID, serverType, serverVersion)
	if err != nil {
		return nil, nil, err
	}

	if expiresAt != nil {
		if err := s.db.WithContext(ctx).Model(server).Update("expires_at", expiresAt).Error; err != nil {
			return nil, nil, fmt.Errorf("failed setting server expiration: %w", err)
		}
		server.ExpiresAt = expiresAt
	}

	job, err := s.createJob(ctx, domain.ServerJob{
		Type:        domain.JobTypeCreateServer,
		Status:      domain.JobStatusPending,
		UserID:      userID,
		ServerID:    server.ID,
		MaxAttempts: DefaultMaxAttempts,
		NextRunAt:   time.Now(),
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Delete(server).Error
		return nil, nil, err
	}

	return server, job, nil
}

// EnqueueDeleteServer queues asynchronous container/volume teardown and server deletion.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting deletion.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	job, err := jobSvc.EnqueueDeleteServer(ctx, user.ID, 42)
func (s *Service) EnqueueDeleteServer(ctx context.Context, userID uint, serverID uint) (*domain.ServerJob, error) {
	server, _, err := s.hostingSvc.GetServerForOwner(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}
	if server.Status == domain.ServerStatusMigrating {
		return nil, hosting.ErrServerMigrating
	}

	var existingCount int64
	if err := s.db.WithContext(ctx).Model(&domain.ServerJob{}).
		Where("server_id = ? AND type = ? AND status IN (?)", server.ID, domain.JobTypeDeleteServer, []domain.JobStatus{domain.JobStatusPending, domain.JobStatusRunning}).
		Count(&existingCount).Error; err == nil && existingCount > 0 {
		return nil, errors.New("server deletion is already queued")
	}

	return s.createJob(ctx, domain.ServerJob{
		Type:        domain.JobTypeDeleteServer,
		Status:      domain.JobStatusPending,
		UserID:      userID,
		ServerID:    server.ID,
		MaxAttempts: DefaultMaxAttempts,
		NextRunAt:   time.Now(),
	})
}

// EnqueueBackup creates a pending BackupRecord and queues an asynchronous volume export/upload job.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting backup (0 allowed for internal callers).
//   - serverID: Target game server ID.
//
// Example usage:
//
//	rec, job, err := jobSvc.EnqueueBackup(ctx, user.ID, 42)
func (s *Service) EnqueueBackup(ctx context.Context, userID uint, serverID uint) (*domain.BackupRecord, *domain.ServerJob, error) {
	server, _, err := s.hostingSvc.GetServerForOwner(ctx, userID, serverID)
	if err != nil {
		return nil, nil, err
	}
	if server.Status == domain.ServerStatusPending || server.Status == domain.ServerStatusProvisioning {
		return nil, nil, errors.New("cannot backup a server while it is still provisioning")
	}
	if server.Status == domain.ServerStatusMigrating {
		return nil, nil, hosting.ErrServerMigrating
	}

	record, err := s.backupSvc.CreatePendingBackup(ctx, server.ID)
	if err != nil {
		return nil, nil, err
	}

	backupID := record.ID
	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	job, err := s.createJob(ctx, domain.ServerJob{
		Type:        domain.JobTypeBackup,
		Status:      domain.JobStatusPending,
		UserID:      actorID,
		ServerID:    server.ID,
		BackupID:    &backupID,
		MaxAttempts: DefaultMaxAttempts,
		NextRunAt:   time.Now(),
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Delete(record).Error
		return nil, nil, err
	}

	return record, job, nil
}

// EnqueueRestore validates the target backup snapshot and queues an asynchronous restore job.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting restore (0 allowed for internal callers).
//   - serverID: Target game server ID.
//   - backupID: BackupRecord primary key to restore.
//
// Example usage:
//
//	job, err := jobSvc.EnqueueRestore(ctx, user.ID, 42, 105)
func (s *Service) EnqueueRestore(ctx context.Context, userID uint, serverID uint, backupID uint) (*domain.ServerJob, error) {
	server, _, err := s.hostingSvc.GetServerForOwner(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}
	if server.Status == domain.ServerStatusMigrating {
		return nil, hosting.ErrServerMigrating
	}
	if err := s.backupSvc.ValidateRestoreTarget(ctx, server.ID, backupID); err != nil {
		return nil, err
	}

	var rec domain.BackupRecord
	if err := s.db.WithContext(ctx).First(&rec, backupID).Error; err != nil {
		return nil, fmt.Errorf("backup record not found: %w", err)
	}
	if rec.Status != domain.BackupStatusCompleted {
		return nil, errors.New("only completed backups can be restored")
	}

	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}
	bid := backupID

	return s.createJob(ctx, domain.ServerJob{
		Type:        domain.JobTypeRestore,
		Status:      domain.JobStatusPending,
		UserID:      actorID,
		ServerID:    server.ID,
		BackupID:    &bid,
		MaxAttempts: DefaultMaxAttempts,
		NextRunAt:   time.Now(),
	})
}

// EnqueueImageBackup creates a pending ServerImageBackup and queues an asynchronous ext4 .img backup job.
// Returns (nil, nil, nil) if an image backup job for serverID is already pending or running.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Initiating admin/system user ID (0 allowed for CRON/internal callers).
//   - serverID: Target game server ID.
//
// Example usage:
//
//	rec, job, err := jobSvc.EnqueueImageBackup(ctx, 0, 42)
func (s *Service) EnqueueImageBackup(ctx context.Context, userID uint, serverID uint) (*domain.ServerImageBackup, *domain.ServerJob, error) {
	var server domain.Server
	if err := s.db.WithContext(ctx).First(&server, serverID).Error; err != nil {
		return nil, nil, fmt.Errorf("server %d not found: %w", serverID, err)
	}
	if server.Status != domain.ServerStatusRunning && server.Status != domain.ServerStatusStopped {
		return nil, nil, fmt.Errorf("cannot image-backup server %d in status %s", server.ID, server.Status)
	}

	var existingCount int64
	if err := s.db.WithContext(ctx).Model(&domain.ServerJob{}).
		Where("server_id = ? AND type = ? AND status IN (?)", server.ID, domain.JobTypeBackupImage, []domain.JobStatus{domain.JobStatusPending, domain.JobStatusRunning}).
		Count(&existingCount).Error; err == nil && existingCount > 0 {
		return nil, nil, nil
	}

	record, err := s.backupSvc.CreatePendingImageBackup(ctx, server.ID)
	if err != nil {
		return nil, nil, err
	}

	imgBackupID := record.ID
	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	job, err := s.createJob(ctx, domain.ServerJob{
		Type:          domain.JobTypeBackupImage,
		Status:        domain.JobStatusPending,
		UserID:        actorID,
		ServerID:      server.ID,
		ImageBackupID: &imgBackupID,
		MaxAttempts:   DefaultMaxAttempts,
		NextRunAt:     time.Now(),
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Delete(record).Error
		return nil, nil, err
	}
	return record, job, nil
}

// EnqueueImageRestore validates the target ServerImageBackup and queues an asynchronous ext4 .img restore job.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Initiating admin user ID (0 allowed for internal callers).
//   - serverID: Target game server ID.
//   - imageBackupID: Completed ServerImageBackup primary key to restore.
//
// Example usage:
//
//	job, err := jobSvc.EnqueueImageRestore(ctx, adminID, 42, 7)
func (s *Service) EnqueueImageRestore(ctx context.Context, userID uint, serverID uint, imageBackupID uint) (*domain.ServerJob, error) {
	var server domain.Server
	if err := s.db.WithContext(ctx).First(&server, serverID).Error; err != nil {
		return nil, fmt.Errorf("server %d not found: %w", serverID, err)
	}
	if server.Status == domain.ServerStatusMigrating {
		return nil, hosting.ErrServerMigrating
	}
	if err := s.backupSvc.ValidateImageRestoreTarget(ctx, server.ID, imageBackupID); err != nil {
		return nil, err
	}

	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}
	ibid := imageBackupID

	return s.createJob(ctx, domain.ServerJob{
		Type:          domain.JobTypeRestoreImage,
		Status:        domain.JobStatusPending,
		UserID:        actorID,
		ServerID:      server.ID,
		ImageBackupID: &ibid,
		MaxAttempts:   DefaultMaxAttempts,
		NextRunAt:     time.Now(),
	})
}

// RetryJob resets a failed job back to pending so the queue worker re-executes it immediately.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Authenticated caller user ID.
//   - isStaffOrAdmin: Whether caller can retry jobs owned by other users.
//   - jobID: Primary key of the failed ServerJob.
//
// Example usage:
//
//	job, err := jobSvc.RetryJob(ctx, user.ID, false, 12)
func (s *Service) RetryJob(ctx context.Context, userID uint, isStaffOrAdmin bool, jobID uint) (*domain.ServerJob, error) {
	var job domain.ServerJob
	if err := s.db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}
	if !isStaffOrAdmin && job.UserID != userID {
		return nil, errors.New("unauthorized access to job")
	}
	if job.Status != domain.JobStatusFailed {
		return nil, errors.New("only failed jobs can be retried")
	}

	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&job).Updates(map[string]interface{}{
		"status":      domain.JobStatusPending,
		"attempts":    0,
		"last_error":  "",
		"next_run_at": now,
		"started_at":  nil,
		"finished_at": nil,
	}).Error; err != nil {
		return nil, fmt.Errorf("failed resetting job state: %w", err)
	}

	if job.Type == domain.JobTypeCreateServer {
		_ = s.db.WithContext(ctx).Model(&domain.Server{}).
			Where("id = ? AND status = ?", job.ServerID, domain.ServerStatusError).
			Update("status", domain.ServerStatusPending).Error
	}
	if job.Type == domain.JobTypeBackup && job.BackupID != nil {
		_ = s.db.WithContext(ctx).Model(&domain.BackupRecord{}).
			Where("id = ?", *job.BackupID).
			Update("status", domain.BackupStatusPending).Error
	}
	if job.Type == domain.JobTypeBackupImage && job.ImageBackupID != nil {
		_ = s.db.WithContext(ctx).Model(&domain.ServerImageBackup{}).
			Where("id = ?", *job.ImageBackupID).
			Update("status", domain.BackupStatusPending).Error
	}

	if err := s.db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		return nil, err
	}
	s.notifyWorker()
	return &job, nil
}

// GetJob retrieves a job record after checking caller permissions.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Authenticated caller user ID.
//   - isStaffOrAdmin: Whether caller may inspect any user's job.
//   - jobID: Job primary key.
//
// Example usage:
//
//	job, err := jobSvc.GetJob(ctx, user.ID, false, 12)
func (s *Service) GetJob(ctx context.Context, userID uint, isStaffOrAdmin bool, jobID uint) (*domain.ServerJob, error) {
	var job domain.ServerJob
	if err := s.db.WithContext(ctx).First(&job, jobID).Error; err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}
	if !isStaffOrAdmin && job.UserID != userID {
		return nil, errors.New("unauthorized access to job")
	}
	return &job, nil
}

// ListServerJobs returns recent queued and completed jobs targeting a given server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//   - limit: Maximum number of records to return (defaults to 20).
//
// Example usage:
//
//	jobsList, err := jobSvc.ListServerJobs(ctx, 42, 20)
func (s *Service) ListServerJobs(ctx context.Context, serverID uint, limit int) ([]domain.ServerJob, error) {
	if limit <= 0 {
		limit = 20
	}
	var list []domain.ServerJob
	err := s.db.WithContext(ctx).
		Where("server_id = ?", serverID).
		Order("created_at desc, id desc").
		Limit(limit).
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

// ProcessNext claims and runs the next due pending job from PostgreSQL.
// Returns (true, nil) if a job was claimed and processed, or (false, nil) if none were ready.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	processed, err := jobSvc.ProcessNext(ctx)
func (s *Service) ProcessNext(ctx context.Context) (bool, error) {
	var candidates []domain.ServerJob
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Where("status = ? AND next_run_at <= ?", domain.JobStatusPending, now).
		Order("next_run_at asc, id asc").
		Limit(1).
		Find(&candidates).Error; err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		return false, nil
	}
	candidate := candidates[0]

	claimRes := s.db.WithContext(ctx).Model(&domain.ServerJob{}).
		Where("id = ? AND status = ?", candidate.ID, domain.JobStatusPending).
		Updates(map[string]interface{}{
			"status":     domain.JobStatusRunning,
			"attempts":   gorm.Expr("attempts + 1"),
			"started_at": now,
		})
	if claimRes.Error != nil {
		return false, claimRes.Error
	}
	if claimRes.RowsAffected == 0 {
		return false, nil
	}

	if err := s.db.WithContext(ctx).First(&candidate, candidate.ID).Error; err != nil {
		return false, err
	}

	log.Printf("[INFO] processing job id=%d type=%s server_id=%d attempt=%d/%d", candidate.ID, candidate.Type, candidate.ServerID, candidate.Attempts, candidate.MaxAttempts)

	execErr := s.executeJob(ctx, &candidate)
	if execErr != nil {
		log.Printf("[WARN] job failed id=%d type=%s server_id=%d attempt=%d/%d err=%v", candidate.ID, candidate.Type, candidate.ServerID, candidate.Attempts, candidate.MaxAttempts, execErr)
		s.handleJobFailure(ctx, &candidate, execErr)
		return true, execErr
	}

	finished := time.Now()
	_ = s.db.WithContext(ctx).Model(&candidate).Updates(map[string]interface{}{
		"status":      domain.JobStatusCompleted,
		"last_error":  "",
		"finished_at": &finished,
	}).Error
	log.Printf("[INFO] completed job id=%d type=%s server_id=%d attempt=%d/%d", candidate.ID, candidate.Type, candidate.ServerID, candidate.Attempts, candidate.MaxAttempts)
	return true, nil
}

// Start launches the background job worker loop and returns a stop function that waits for clean exit.
//
// Arguments:
//   - ctx: Root context controlling worker shutdown.
//
// Example usage:
//
//	waitStop := jobSvc.Start(ctx)
//	defer waitStop()
func (s *Service) Start(ctx context.Context) func() {
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(s.pollInterval)
		defer ticker.Stop()

		for {
			s.drainReadyJobs(runCtx)
			select {
			case <-runCtx.Done():
				return
			case <-s.wakeCh:
			case <-ticker.C:
			}
		}
	}()

	return func() {
		cancel()
		wg.Wait()
	}
}

func (s *Service) drainReadyJobs(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		processed, err := s.ProcessNext(ctx)
		if err != nil {
			log.Printf("[WARN] job queue processing error: %v", err)
		}
		if !processed {
			return
		}
	}
}

func (s *Service) createJob(ctx context.Context, job domain.ServerJob) (*domain.ServerJob, error) {
	if err := s.db.WithContext(ctx).Create(&job).Error; err != nil {
		return nil, fmt.Errorf("failed persisting queued job: %w", err)
	}
	s.notifyWorker()
	return &job, nil
}

func (s *Service) notifyWorker() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *Service) executeJob(ctx context.Context, job *domain.ServerJob) error {
	switch job.Type {
	case domain.JobTypeCreateServer:
		_, err := s.hostingSvc.ProvisionServer(ctx, job.ServerID)
		return err
	case domain.JobTypeDeleteServer:
		return s.hostingSvc.DeleteServer(ctx, job.UserID, job.ServerID)
	case domain.JobTypeBackup:
		if job.BackupID == nil {
			return errors.New("backup job missing backup_id")
		}
		_, err := s.backupSvc.ExecuteBackupRecord(ctx, job.UserID, job.ServerID, *job.BackupID)
		return err
	case domain.JobTypeRestore:
		if job.BackupID == nil {
			return errors.New("restore job missing backup_id")
		}
		return s.backupSvc.RestoreServer(ctx, job.ServerID, *job.BackupID)
	case domain.JobTypeBackupImage:
		if job.ImageBackupID == nil {
			return errors.New("image backup job missing image_backup_id")
		}
		_, err := s.backupSvc.ExecuteImageBackupRecord(ctx, job.UserID, job.ServerID, *job.ImageBackupID)
		return err
	case domain.JobTypeRestoreImage:
		if job.ImageBackupID == nil {
			return errors.New("image restore job missing image_backup_id")
		}
		return s.backupSvc.RestoreImageBackup(ctx, job.ServerID, *job.ImageBackupID)
	default:
		return fmt.Errorf("unsupported job type: %s", job.Type)
	}
}

func (s *Service) handleJobFailure(ctx context.Context, job *domain.ServerJob, execErr error) {
	now := time.Now()
	if job.Attempts < job.MaxAttempts {
		delay := s.retryBackoff * time.Duration(job.Attempts)
		_ = s.db.WithContext(ctx).Model(job).Updates(map[string]interface{}{
			"status":      domain.JobStatusPending,
			"last_error":  execErr.Error(),
			"next_run_at": now.Add(delay),
		}).Error
		return
	}

	_ = s.db.WithContext(ctx).Model(job).Updates(map[string]interface{}{
		"status":      domain.JobStatusFailed,
		"last_error":  execErr.Error(),
		"finished_at": &now,
	}).Error

	if job.Type == domain.JobTypeCreateServer {
		_ = s.db.WithContext(ctx).Model(&domain.Server{}).
			Where("id = ?", job.ServerID).
			Update("status", domain.ServerStatusError).Error
	}
}

// ProcessExpiredServers stops any running servers whose ExpiresAt timestamp has passed.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - now: Reference timestamp for expiration checks.
//
// Example usage:
//
//	stopped, err := jobSvc.ProcessExpiredServers(ctx, time.Now())
func (s *Service) ProcessExpiredServers(ctx context.Context, now time.Time) (int, error) {
	var expired []domain.Server
	if err := s.db.WithContext(ctx).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at <= ?", domain.ServerStatusRunning, now).
		Find(&expired).Error; err != nil {
		return 0, fmt.Errorf("failed querying expired servers: %w", err)
	}

	stoppedCount := 0
	for _, srv := range expired {
		if ctx.Err() != nil {
			break
		}
		if _, err := s.hostingSvc.StopServer(ctx, srv.UserID, srv.ID); err != nil {
			domain.RecordServerLog(ctx, s.db, srv.ID, 0, domain.ServerOpExpireStop, domain.ServerLogStatusError, fmt.Sprintf("failed stopping expired server: %v", err))
			continue
		}
		domain.RecordServerLog(ctx, s.db, srv.ID, 0, domain.ServerOpExpireStop, domain.ServerLogStatusSuccess, "stopped expired server")
		stoppedCount++
	}
	return stoppedCount, nil
}

// CleanupRetentionServers permanently deletes servers that have passed the post-expiration retention window.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - now: Reference timestamp.
//   - retentionDays: Grace period in days after ExpiresAt before deletion.
//
// Example usage:
//
//	deleted, err := jobSvc.CleanupRetentionServers(ctx, time.Now(), 7)
func (s *Service) CleanupRetentionServers(ctx context.Context, now time.Time, retentionDays int) (int, error) {
	if retentionDays <= 0 {
		retentionDays = 7
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)

	var candidates []domain.Server
	if err := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ?", cutoff).
		Find(&candidates).Error; err != nil {
		return 0, fmt.Errorf("failed querying retention-expired servers: %w", err)
	}

	deletedCount := 0
	for _, srv := range candidates {
		if ctx.Err() != nil {
			break
		}
		if err := s.hostingSvc.DeleteServer(ctx, srv.UserID, srv.ID); err != nil {
			domain.RecordServerLog(ctx, s.db, srv.ID, 0, domain.ServerOpRetentionDelete, domain.ServerLogStatusError, fmt.Sprintf("failed deleting server after retention: %v", err))
			continue
		}
		domain.RecordServerLog(ctx, s.db, srv.ID, 0, domain.ServerOpRetentionDelete, domain.ServerLogStatusSuccess, fmt.Sprintf("deleted server after %d-day retention", retentionDays))
		deletedCount++
	}
	return deletedCount, nil
}

// StartLifecycleCron launches a background ticker that stops expired servers and cleans up
// servers past the retention window until ctx is canceled.
//
// Arguments:
//   - ctx: Cancellation context.
//   - interval: Sweep interval (defaults to 1 minute if <= 0).
//   - retentionDays: Grace period in days after expiration.
//
// Example usage:
//
//	stopCron := jobSvc.StartLifecycleCron(sigCtx, time.Minute, 7)
//	defer stopCron()
func (s *Service) StartLifecycleCron(ctx context.Context, interval time.Duration, retentionDays int) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				if stopped, err := s.ProcessExpiredServers(runCtx, now); err != nil {
					log.Printf("[WARN] expired server sweep error: %v", err)
				} else if stopped > 0 {
					log.Printf("[INFO] stopped %d expired server(s)", stopped)
				}
				if deleted, err := s.CleanupRetentionServers(runCtx, now, retentionDays); err != nil {
					log.Printf("[WARN] retention cleanup sweep error: %v", err)
				} else if deleted > 0 {
					log.Printf("[INFO] deleted %d server(s) past %d-day retention", deleted, retentionDays)
				}
			}
		}
	}()

	return func() {
		cancel()
		wg.Wait()
	}
}

// EnqueueScheduledImageBackups scans all provisioned servers (running and stopped) and enqueues
// ext4 image backup jobs in server_jobs, skipping any server that already has one queued.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	count, err := jobSvc.EnqueueScheduledImageBackups(ctx)
func (s *Service) EnqueueScheduledImageBackups(ctx context.Context) (int, error) {
	var servers []domain.Server
	if err := s.db.WithContext(ctx).
		Where("status IN (?)", []domain.ServerStatus{domain.ServerStatusRunning, domain.ServerStatusStopped}).
		Find(&servers).Error; err != nil {
		return 0, fmt.Errorf("failed querying provisioned servers for image backup sweep: %w", err)
	}

	enqueued := 0
	for _, srv := range servers {
		if ctx.Err() != nil {
			break
		}
		_, job, err := s.EnqueueImageBackup(ctx, 0, srv.ID)
		if err != nil {
			log.Printf("[WARN] scheduled image backup enqueue failed for server %d: %v", srv.ID, err)
			continue
		}
		if job != nil {
			enqueued++
		}
	}
	return enqueued, nil
}

// StartImageBackupCron schedules daily (or custom cronSpec) ext4 image backup enqueue sweeps
// using the container's local timezone and stops cleanly when ctx is canceled or stopFn is invoked.
//
// Arguments:
//   - ctx: Cancellation context.
//   - cronSpec: Standard 5-field cron expression (e.g. "0 5 * * *").
//
// Example usage:
//
//	stopBackupCron, err := jobSvc.StartImageBackupCron(sigCtx, "0 5 * * *")
//	defer stopBackupCron()
func (s *Service) StartImageBackupCron(ctx context.Context, cronSpec string) (func(), error) {
	c := cron.New()
	_, err := c.AddFunc(cronSpec, func() {
		sweepCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		enqueued, sweepErr := s.EnqueueScheduledImageBackups(sweepCtx)
		if sweepErr != nil {
			log.Printf("[WARN] scheduled image backup cron sweep error: %v", sweepErr)
			return
		}
		log.Printf("[INFO] scheduled image backup cron enqueued %d job(s)", enqueued)
	})
	if err != nil {
		return nil, fmt.Errorf("invalid image backup cron expression %q: %w", cronSpec, err)
	}

	c.Start()

	var stopOnce sync.Once
	stopFn := func() {
		stopOnce.Do(func() {
			stopCtx := c.Stop()
			<-stopCtx.Done()
		})
	}

	go func() {
		<-ctx.Done()
		stopFn()
	}()

	return stopFn, nil
}
