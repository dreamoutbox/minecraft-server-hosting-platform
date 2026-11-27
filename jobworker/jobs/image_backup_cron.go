package jobs

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// StartImageBackupCron schedules daily (or custom cronSpec) ext4 image backup enqueue sweeps
// using the container's local timezone and stops cleanly when ctx is canceled or stopFn is invoked.
//
// Arguments:
//   - ctx: Cancellation context.
//   - cronSpec: Standard 5-field cron expression (e.g. "0 5 * * *").
//
// Example usage:
//
//	stopBackupCron, err := jobSvc.StartImageBackupCron(sigCtx, "0 5 * * *")
//	defer stopBackupCron()
func (s *Service) StartImageBackupCron(ctx context.Context, cronSpec string) (func(), error) {
	c := cron.New()
	_, err := c.AddFunc(cronSpec, func() {
		sweepCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		enqueued, sweepErr := s.EnqueueScheduledImageBackups(sweepCtx)
		if sweepErr != nil {
			log.Printf("[WARN] scheduled image backup cron sweep error: %v", sweepErr)
			return
		}
		log.Printf("[INFO] scheduled image backup cron enqueued %d job(s)", enqueued)
	})
	if err != nil {
		return nil, fmt.Errorf("invalid image backup cron expression %q: %w", cronSpec, err)
	}

	c.Start()

	var stopOnce sync.Once
	stopFn := func() {
		stopOnce.Do(func() {
			stopCtx := c.Stop()
			<-stopCtx.Done()
		})
	}

	go func() {
		<-ctx.Done()
		stopFn()
	}()

	return stopFn, nil
}
