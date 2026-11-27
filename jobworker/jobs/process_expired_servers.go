package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

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
