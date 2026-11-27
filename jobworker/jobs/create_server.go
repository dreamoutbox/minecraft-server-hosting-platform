package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

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

func (s *Service) executeCreateServer(ctx context.Context, job *domain.ServerJob) error {
	_, err := s.hostingSvc.ProvisionServer(ctx, job.ServerID)
	return err
}
