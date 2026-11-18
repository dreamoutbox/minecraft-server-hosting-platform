package hosting

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

var (
	// ErrNoPortsAvailable indicates all ports within the configured range are allocated.
	ErrNoPortsAvailable = errors.New("no available ports within configured cluster range")
	// ErrPortConflictExhausted indicates concurrent conflicts exceeded maximum retries.
	ErrPortConflictExhausted = errors.New("failed allocating port after maximum retry attempts")
)

// PortAllocator finds and assigns cluster-wide unique ports within the configured bounds.
type PortAllocator struct {
	db      *gorm.DB
	minPort int
	maxPort int
}

// NewPortAllocator constructs a port allocator with explicit range boundaries.
//
// Arguments:
//   - db: Active database handle.
//   - minPort: Lower port bound (inclusive).
//   - maxPort: Upper port bound (inclusive).
//
// Example usage:
//
//	allocator := hosting.NewPortAllocator(db, 25565, 25600)
func NewPortAllocator(db *gorm.DB, minPort, maxPort int) *PortAllocator {
	return &PortAllocator{
		db:      db,
		minPort: minPort,
		maxPort: maxPort,
	}
}

// AllocateNextPort finds the lowest unallocated port within the configured cluster range.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	port, err := allocator.AllocateNextPort(ctx)
func (p *PortAllocator) AllocateNextPort(ctx context.Context) (int, error) {
	var usedPorts []int
	err := p.db.WithContext(ctx).Model(&domain.Server{}).
		Where("port >= ? AND port <= ?", p.minPort, p.maxPort).
		Pluck("port", &usedPorts).Error
	if err != nil {
		return 0, fmt.Errorf("failed querying used ports: %w", err)
	}

	usedMap := make(map[int]bool, len(usedPorts))
	for _, port := range usedPorts {
		usedMap[port] = true
	}

	for port := p.minPort; port <= p.maxPort; port++ {
		if !usedMap[port] {
			return port, nil
		}
	}

	return 0, ErrNoPortsAvailable
}

// AllocateWithRetry runs an allocation-and-create closure with automatic retry on port collisions.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - maxRetries: Maximum conflict retries before giving up.
//   - createFunc: Callback that attempts to persist the server given an allocated port.
//
// Example usage:
//
//	port, err := allocator.AllocateWithRetry(ctx, 5, func(port int) error {
//	    return db.Create(&domain.Server{Port: port}).Error
//	})
func (p *PortAllocator) AllocateWithRetry(ctx context.Context, maxRetries int, createFunc func(port int) error) (int, error) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		port, err := p.AllocateNextPort(ctx)
		if err != nil {
			return 0, err
		}

		err = createFunc(port)
		if err == nil {
			return port, nil
		}

		if isUniquePortViolation(err) {
			continue // concurrent worker claimed this port; retry with next available
		}

		return 0, err
	}

	return 0, ErrPortConflictExhausted
}

// isUniquePortViolation checks whether a database error was caused by a unique port collision.
func isUniquePortViolation(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "unique") && strings.Contains(errStr, "port") || strings.Contains(errStr, "23505")
}
