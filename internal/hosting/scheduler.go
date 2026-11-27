package hosting

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

var (
	// ErrNoActiveHosts indicates no host in the cluster is in an active state.
	ErrNoActiveHosts = errors.New("no active hosts available in cluster")
	// ErrNoCapacityAvailable indicates all active hosts have reached their maximum server limit.
	ErrNoCapacityAvailable = errors.New("no host capacity available in cluster")
)

// Scheduler defines placement strategies for assigning game servers to hosts.
type Scheduler interface {
	SelectHost(ctx context.Context) (*domain.Host, error)
	SelectCandidates(ctx context.Context) ([]domain.Host, error)
}

// LeastLoadedScheduler selects the host with the lowest ratio of active servers to max capacity.
type LeastLoadedScheduler struct {
	db *gorm.DB
}

type hostCandidate struct {
	host      domain.Host
	loadRatio float64
}

// NewLeastLoadedScheduler initializes a cluster scheduler backed by the database.
//
// Arguments:
//   - db: Active database handle.
//
// Example usage:
//
//	scheduler := hosting.NewLeastLoadedScheduler(db)
func NewLeastLoadedScheduler(db *gorm.DB) *LeastLoadedScheduler {
	return &LeastLoadedScheduler{db: db}
}

// SelectCandidates evaluates active hosts and returns them ordered by lowest load ratio.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	candidates, err := scheduler.SelectCandidates(ctx)
func (s *LeastLoadedScheduler) SelectCandidates(ctx context.Context) ([]domain.Host, error) {
	var hosts []domain.Host
	if err := s.db.WithContext(ctx).Where("status = ?", domain.HostStatusActive).Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("failed querying active hosts: %w", err)
	}

	if len(hosts) == 0 {
		return nil, ErrNoActiveHosts
	}

	var candidates []hostCandidate
	for i := range hosts {
		h := &hosts[i]
		if h.MaxServers <= 0 {
			continue
		}

		var count int64
		err := s.db.WithContext(ctx).Model(&domain.Server{}).
			Where("host_id = ? AND status NOT IN (?)", h.ID, []domain.ServerStatus{domain.ServerStatusError}).
			Count(&count).Error
		if err != nil {
			return nil, fmt.Errorf("failed counting servers on host %d: %w", h.ID, err)
		}

		if int(count) >= h.MaxServers {
			continue
		}

		loadRatio := float64(count) / float64(h.MaxServers)
		candidates = append(candidates, hostCandidate{
			host:      *h,
			loadRatio: loadRatio,
		})
	}

	if len(candidates) == 0 {
		return nil, ErrNoCapacityAvailable
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].loadRatio < candidates[j].loadRatio
	})

	result := make([]domain.Host, len(candidates))
	for i, c := range candidates {
		result[i] = c.host
	}
	return result, nil
}

// SelectHost evaluates active hosts and returns the host with greatest proportional free capacity.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	host, err := scheduler.SelectHost(ctx)
//	if err != nil {
//	    return nil, err
//	}
func (s *LeastLoadedScheduler) SelectHost(ctx context.Context) (*domain.Host, error) {
	candidates, err := s.SelectCandidates(ctx)
	if err != nil {
		return nil, err
	}
	return &candidates[0], nil
}
