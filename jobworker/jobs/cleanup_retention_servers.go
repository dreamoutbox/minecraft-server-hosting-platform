package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

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
