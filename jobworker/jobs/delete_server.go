package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
)

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

	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	return s.createJob(ctx, domain.ServerJob{
		Type:        domain.JobTypeDeleteServer,
		Status:      domain.JobStatusPending,
		UserID:      actorID,
		ServerID:    server.ID,
		MaxAttempts: DefaultMaxAttempts,
		NextRunAt:   time.Now(),
	})
}

func (s *Service) executeDeleteServer(ctx context.Context, job *domain.ServerJob) error {
	return s.hostingSvc.DeleteServer(ctx, job.UserID, job.ServerID)
}
