package jobs

import (
	"context"
	"fmt"
	"log"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// EnqueueAllImageBackups scans all provisioned servers (running and stopped) and enqueues
// ext4 image backup jobs in server_jobs, returning every newly created job and skipping
// any server that already has an image backup pending or running.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	jobsList, err := jobSvc.EnqueueAllImageBackups(ctx)
func (s *Service) EnqueueAllImageBackups(ctx context.Context) ([]*domain.ServerJob, error) {
	var servers []domain.Server
	if err := s.db.WithContext(ctx).
		Where("status IN (?)", []domain.ServerStatus{domain.ServerStatusRunning, domain.ServerStatusStopped}).
		Order("id asc").
		Find(&servers).Error; err != nil {
		return nil, fmt.Errorf("failed querying provisioned servers for image backup sweep: %w", err)
	}

	var queued []*domain.ServerJob
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
			queued = append(queued, job)
		}
	}
	return queued, nil
}

// EnqueueScheduledImageBackups scans all provisioned servers (running and stopped) and enqueues
// ext4 image backup jobs in server_jobs, returning the count of newly enqueued jobs.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	count, err := jobSvc.EnqueueScheduledImageBackups(ctx)
func (s *Service) EnqueueScheduledImageBackups(ctx context.Context) (int, error) {
	queued, err := s.EnqueueAllImageBackups(ctx)
	if err != nil {
		return 0, err
	}
	return len(queued), nil
}
