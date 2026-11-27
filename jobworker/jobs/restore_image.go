package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
)

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

func (s *Service) executeRestoreImage(ctx context.Context, job *domain.ServerJob) error {
	if job.ImageBackupID == nil {
		return errors.New("image restore job missing image_backup_id")
	}
	return s.backupSvc.RestoreImageBackup(ctx, job.ServerID, *job.ImageBackupID)
}
