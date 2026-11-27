package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
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
		return s.executeCreateServer(ctx, job)
	case domain.JobTypeDeleteServer:
		return s.executeDeleteServer(ctx, job)
	case domain.JobTypeBackup:
		return s.executeBackupServer(ctx, job)
	case domain.JobTypeRestore:
		return s.executeRestoreServer(ctx, job)
	case domain.JobTypeBackupImage:
		return s.executeBackupImage(ctx, job)
	case domain.JobTypeRestoreImage:
		return s.executeRestoreImage(ctx, job)
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
