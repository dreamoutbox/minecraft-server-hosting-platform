package hosting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/storage"
	worker "github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
)

var (
	// ErrServerMigrating is returned when mutating a server undergoing active host migration.
	ErrServerMigrating = errors.New("server is currently migrating to another host")
	// ErrNoDestinationHost is returned when no alternate host possesses available capacity.
	ErrNoDestinationHost = errors.New("no eligible destination host with capacity available")
	// ErrMigrationHealthCheckFailed indicates the destination container failed health verification.
	ErrMigrationHealthCheckFailed = errors.New("destination container failed pre-cutover health check")
)

// MigrationService coordinates zero-data-loss blue/green server moves across nodes.
type MigrationService struct {
	hostingSvc    *Service
	backupSvc     *backup.Service
	storage       storage.StorageProvider
	workerFactory WorkerClientFactory
}

// NewMigrationService instantiates a server migration coordinator.
//
// Arguments:
//   - hostingSvc: Hosting service managing cluster state and placement.
//   - backupSvc: Backup service providing volume snapshots.
//   - storage: Object storage provider hosting world archives.
//   - factory: Worker client factory; defaults to hostingSvc factory if nil.
//
// Example usage:
//
//	ms := hosting.NewMigrationService(hostingSvc, backupSvc, storageProvider, nil)
func NewMigrationService(hostingSvc *Service, backupSvc *backup.Service, storage storage.StorageProvider, factory WorkerClientFactory) *MigrationService {
	if factory == nil {
		factory = hostingSvc.workerClientFactory
	}
	return &MigrationService{
		hostingSvc:    hostingSvc,
		backupSvc:     backupSvc,
		storage:       storage,
		workerFactory: factory,
	}
}

// MigrateServer moves a game server to an alternate host, preserving its globally unique port.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting the migration (or 0 for admin).
//   - serverID: Target game server ID.
//   - targetHostID: Explicit destination host ID (or 0 to let the scheduler choose).
//
// Example usage:
//
//	err := ms.MigrateServer(ctx, user.ID, 42, 0)
func (m *MigrationService) MigrateServer(ctx context.Context, userID uint, serverID uint, targetHostID uint) error {
	server, sourceHost, err := m.hostingSvc.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return err
	}

	sourceBaseURL := sourceHost.WorkerBaseURL
	sourceAPIKey := sourceHost.APIKey

	if server.Status == domain.ServerStatusMigrating {
		return ErrServerMigrating
	}

	// Step 1: Mark server status as migrating
	originalStatus := server.Status
	server.Status = domain.ServerStatusMigrating
	if err := m.hostingSvc.db.WithContext(ctx).Save(server).Error; err != nil {
		return fmt.Errorf("failed marking server as migrating: %w", err)
	}

	// Helper to safely revert status if migration aborts early
	abortMigration := func(cause error) error {
		server.Status = originalStatus
		_ = m.hostingSvc.db.WithContext(ctx).Save(server)
		return cause
	}

	// Step 2: Trigger fresh backup snapshot to guarantee zero data loss
	backupRecord, err := m.backupSvc.BackupServer(ctx, server.ID)
	if err != nil {
		return abortMigration(fmt.Errorf("failed generating pre-migration backup: %w", err))
	}

	archiveBytes, err := m.storage.Get(ctx, backupRecord.StorageKey)
	if err != nil {
		return abortMigration(fmt.Errorf("failed loading backup snapshot: %w", err))
	}

	// Step 3: Select destination host with capacity
	destHost, err := m.selectDestinationHost(ctx, sourceHost.ID, targetHostID)
	if err != nil {
		return abortMigration(err)
	}

	destWorker := m.workerFactory(destHost.WorkerBaseURL, destHost.APIKey)
	volID := strconv.FormatUint(uint64(server.ID), 10)

	// Step 4: Provision volume on destination host
	volResp, err := destWorker.CreateVolume(ctx, server.ID, server.Plan.DiskLimitGB)
	if err != nil {
		return abortMigration(fmt.Errorf("failed provisioning destination volume: %w", err))
	}

	// Step 5: Import world snapshot into destination volume
	if err := destWorker.ImportVolume(ctx, volID, bytes.NewReader(archiveBytes)); err != nil {
		_ = destWorker.DeleteVolume(ctx, volID)
		return abortMigration(fmt.Errorf("failed importing snapshot to destination: %w", err))
	}

	// Step 6: Create container on destination preserving exact existing port
	ctrReq := buildContainerRequest(server, &server.Plan, volResp.MountPath)

	ctrResp, err := destWorker.CreateContainer(ctx, ctrReq)
	if err != nil {
		_ = destWorker.DeleteVolume(ctx, volID)
		return abortMigration(fmt.Errorf("failed creating destination container: %w", err))
	}

	// Step 7: Pre-cutover Blue/Green Health Check on destination container
	destHealthy := false
	for attempt := 0; attempt < 5; attempt++ {
		status, statusErr := destWorker.GetContainerStatus(ctx, ctrResp.ContainerID)
		if statusErr == nil && status.Status == worker.ContainerStatusRunning {
			destHealthy = true
			break
		}
		select {
		case <-ctx.Done():
			return abortMigration(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}

	if !destHealthy {
		// Cleanup destination; leave source running untouched
		_ = destWorker.StopContainer(ctx, ctrResp.ContainerID)
		_ = destWorker.DeleteContainer(ctx, ctrResp.ContainerID)
		_ = destWorker.DeleteVolume(ctx, volID)
		return abortMigration(ErrMigrationHealthCheckFailed)
	}

	// Step 8: Cutover success: update Server coordinates in database
	oldContainerID := server.ContainerID
	server.HostID = destHost.ID
	server.Host = *destHost
	server.ContainerID = ctrResp.ContainerID
	server.MountPath = volResp.MountPath
	server.Status = domain.ServerStatusRunning

	err = m.hostingSvc.db.WithContext(ctx).Model(&domain.Server{}).
		Where("id = ?", server.ID).
		Updates(map[string]interface{}{
			"host_id":      destHost.ID,
			"container_id": ctrResp.ContainerID,
			"mount_path":   volResp.MountPath,
			"status":       domain.ServerStatusRunning,
		}).Error
	if err != nil {
		return fmt.Errorf("cutover succeeded but database update failed: %w", err)
	}

	// Step 9: Release source-side worker resources
	sourceWorker := m.workerFactory(sourceBaseURL, sourceAPIKey)
	if oldContainerID != "" {
		_ = sourceWorker.StopContainer(ctx, oldContainerID)
		_ = sourceWorker.DeleteContainer(ctx, oldContainerID)
	}
	_ = sourceWorker.DeleteVolume(ctx, volID)

	return nil
}

// selectDestinationHost picks a qualified destination node with remaining capacity.
func (m *MigrationService) selectDestinationHost(ctx context.Context, sourceHostID uint, targetHostID uint) (*domain.Host, error) {
	if targetHostID != 0 {
		var target domain.Host
		if err := m.hostingSvc.db.WithContext(ctx).First(&target, targetHostID).Error; err != nil {
			return nil, fmt.Errorf("target destination host %d not found: %w", targetHostID, err)
		}
		if target.ID == sourceHostID {
			return nil, errors.New("destination host cannot be the same as current source host")
		}
		if target.Status != domain.HostStatusActive {
			return nil, errors.New("target destination host is not active")
		}
		return &target, nil
	}

	var candidates []domain.Host
	err := m.hostingSvc.db.WithContext(ctx).
		Where("status = ? AND id != ?", domain.HostStatusActive, sourceHostID).
		Find(&candidates).Error
	if err != nil {
		return nil, fmt.Errorf("failed finding candidate hosts: %w", err)
	}

	var bestHost *domain.Host
	bestRatio := 1.1

	for i := range candidates {
		h := &candidates[i]
		if h.MaxServers <= 0 {
			continue
		}

		var count int64
		_ = m.hostingSvc.db.WithContext(ctx).Model(&domain.Server{}).
			Where("host_id = ? AND status NOT IN (?)", h.ID, []domain.ServerStatus{domain.ServerStatusError}).
			Count(&count).Error

		if int(count) >= h.MaxServers {
			continue
		}

		ratio := float64(count) / float64(h.MaxServers)
		if bestHost == nil || ratio < bestRatio {
			bestHost = h
			bestRatio = ratio
		}
	}

	if bestHost == nil {
		return nil, ErrNoDestinationHost
	}

	return bestHost, nil
}
