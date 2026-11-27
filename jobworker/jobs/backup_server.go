package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
)

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

func (s *Service) executeBackupServer(ctx context.Context, job *domain.ServerJob) error {
	if job.BackupID == nil {
		return errors.New("backup job missing backup_id")
	}
	_, err := s.backupSvc.ExecuteBackupRecord(ctx, job.UserID, job.ServerID, *job.BackupID)
	return err
}
