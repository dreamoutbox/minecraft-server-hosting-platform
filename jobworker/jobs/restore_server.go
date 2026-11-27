package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
)

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

func (s *Service) executeRestoreServer(ctx context.Context, job *domain.ServerJob) error {
	if job.BackupID == nil {
		return errors.New("restore job missing backup_id")
	}
	return s.backupSvc.RestoreServer(ctx, job.ServerID, *job.BackupID)
}
