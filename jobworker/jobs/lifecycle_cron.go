package jobs

import (
	"context"
	"log"
	"sync"
	"time"
)

// StartLifecycleCron launches a background ticker that stops expired servers and cleans up
// servers past the retention window until ctx is canceled.
//
// Arguments:
//   - ctx: Cancellation context.
//   - interval: Sweep interval (defaults to 1 minute if <= 0).
//   - retentionDays: Grace period in days after expiration.
//
// Example usage:
//
//	stopCron := jobSvc.StartLifecycleCron(sigCtx, time.Minute, 7)
//	defer stopCron()
func (s *Service) StartLifecycleCron(ctx context.Context, interval time.Duration, retentionDays int) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				if stopped, err := s.ProcessExpiredServers(runCtx, now); err != nil {
					log.Printf("[WARN] expired server sweep error: %v", err)
				} else if stopped > 0 {
					log.Printf("[INFO] stopped %d expired server(s)", stopped)
				}
				if deleted, err := s.CleanupRetentionServers(runCtx, now, retentionDays); err != nil {
					log.Printf("[WARN] retention cleanup sweep error: %v", err)
				} else if deleted > 0 {
					log.Printf("[INFO] deleted %d server(s) past %d-day retention", deleted, retentionDays)
				}
			}
		}
	}()

	return func() {
		cancel()
		wg.Wait()
	}
}
