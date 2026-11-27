package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

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

func (s *Service) executeBackupImage(ctx context.Context, job *domain.ServerJob) error {
	if job.ImageBackupID == nil {
		return errors.New("image backup job missing image_backup_id")
	}
	_, err := s.backupSvc.ExecuteImageBackupRecord(ctx, job.UserID, job.ServerID, *job.ImageBackupID)
	return err
}
