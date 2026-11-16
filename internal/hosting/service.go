package hosting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"

	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// WorkerClientFactory produces a typed worker client given host credentials.
type WorkerClientFactory func(baseURL string, apiKey string) worker.Client

// FileServerClientFactory produces a fileserver management client given host credentials.
type FileServerClientFactory func(baseURL string, apiKey string) fsclient.Client

// Service coordinates server scheduling, port allocation, and host worker provisioning.
type Service struct {
	db                      *gorm.DB
	scheduler               Scheduler
	portAllocator           *PortAllocator
	workerClientFactory     WorkerClientFactory
	fileServerClientFactory FileServerClientFactory
}

// NewService instantiates the hosting orchestrator service.
//
// Arguments:
//   - db: Active database handle.
//   - scheduler: Node placement scheduler.
//   - portAllocator: Cluster-wide port allocator.
//   - factory: Factory creating worker clients; if nil, defaults to HTTP worker client.
//
// Example usage:
//
//	svc := hosting.NewService(db, scheduler, allocator, nil)
func NewService(db *gorm.DB, scheduler Scheduler, portAllocator *PortAllocator, factory WorkerClientFactory) *Service {
	if factory == nil {
		factory = func(baseURL string, apiKey string) worker.Client {
			return worker.NewClient(baseURL, apiKey)
		}
	}
	return &Service{
		db:                  db,
		scheduler:           scheduler,
		portAllocator:       portAllocator,
		workerClientFactory: factory,
		fileServerClientFactory: func(baseURL string, apiKey string) fsclient.Client {
			return fsclient.New(baseURL, apiKey)
		},
	}
}

// SetFileServerClientFactory overrides the factory used to contact node fileservers.
//
// Arguments:
//   - factory: Factory creating fileserver management clients.
//
// Example usage:
//
//	svc.SetFileServerClientFactory(customFactory)
func (s *Service) SetFileServerClientFactory(factory FileServerClientFactory) {
	if factory != nil {
		s.fileServerClientFactory = factory
	}
}

// RegisterHost registers a physical or virtual node into the cluster and generates a unique API key.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - hostname: Fully qualified domain name or label of the node.
//   - workerBaseURL: HTTP address of the node's worker daemon.
//   - maxServers: Capacity limit for simultaneous servers on this host.
//
// Example usage:
//
//	host, apiKey, err := svc.RegisterHost(ctx, "node-1.internal", "http://10.0.0.1:9090", 20)
func (s *Service) RegisterHost(ctx context.Context, hostname string, workerBaseURL string, maxServers int) (*domain.Host, string, error) {
	if hostname == "" || workerBaseURL == "" || maxServers <= 0 {
		return nil, "", errors.New("invalid host registration parameters")
	}

	rawAPIKey := fmt.Sprintf("mc_%s_%s", uuid.New().String(), uuid.New().String())
	hashBytes := sha256.Sum256([]byte(rawAPIKey))
	hashHex := hex.EncodeToString(hashBytes[:])

	host := domain.Host{
		Hostname:      hostname,
		WorkerBaseURL: workerBaseURL,
		APIKey:        rawAPIKey,
		APIKeyHash:    hashHex,
		MaxServers:    maxServers,
		Status:        domain.HostStatusActive,
	}

	if err := s.db.WithContext(ctx).Create(&host).Error; err != nil {
		return nil, "", fmt.Errorf("failed creating host record: %w", err)
	}

	return &host, rawAPIKey, nil
}

// RotateHostAPIKey generates a new secure API key for the host, updates its hash, and returns the new key once.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - hostID: Host primary key.
//
// Example usage:
//
//	newKey, err := svc.RotateHostAPIKey(ctx, 5)
func (s *Service) RotateHostAPIKey(ctx context.Context, hostID uint) (string, error) {
	var host domain.Host
	if err := s.db.WithContext(ctx).First(&host, hostID).Error; err != nil {
		return "", fmt.Errorf("host %d not found: %w", hostID, err)
	}

	rawAPIKey := fmt.Sprintf("mc_%s_%s", uuid.New().String(), uuid.New().String())
	hashBytes := sha256.Sum256([]byte(rawAPIKey))
	hashHex := hex.EncodeToString(hashBytes[:])

	host.APIKey = rawAPIKey
	host.APIKeyHash = hashHex

	if err := s.db.WithContext(ctx).Save(&host).Error; err != nil {
		return "", fmt.Errorf("failed saving rotated host key: %w", err)
	}

	return rawAPIKey, nil
}

// ListHosts returns all registered nodes in the platform cluster.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	hosts, err := svc.ListHosts(ctx)
func (s *Service) ListHosts(ctx context.Context) ([]domain.Host, error) {
	var hosts []domain.Host
	if err := s.db.WithContext(ctx).Find(&hosts).Error; err != nil {
		return nil, err
	}
	return hosts, nil
}

// CreateServer schedules, reserves a port, provisions storage and container, and activates a server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID of the server owner.
//   - name: Descriptive name for the game server.
//   - planID: ID of selected resource tier.
//
// Example usage:
//
//	srv, err := svc.CreateServer(ctx, user.ID, "SurvivalWorld", plan.ID)
func (s *Service) CreateServer(ctx context.Context, userID uint, name string, planID uint) (*domain.Server, error) {
	var plan domain.Plan
	if err := s.db.WithContext(ctx).First(&plan, planID).Error; err != nil {
		return nil, fmt.Errorf("invalid plan ID %d: %w", planID, err)
	}

	candidates, err := s.scheduler.SelectCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("scheduling failed: %w", err)
	}

	var lastErr error
	for _, host := range candidates {
		var server domain.Server
		allocatedPort, err := s.portAllocator.AllocateWithRetry(ctx, 5, func(port int) error {
			server = domain.Server{
				UserID: userID,
				PlanID: plan.ID,
				HostID: host.ID,
				Name:   name,
				Port:   port,
				Status: domain.ServerStatusProvisioning,
			}
			return s.db.WithContext(ctx).Create(&server).Error
		})
		if err != nil {
			lastErr = fmt.Errorf("failed allocating server port on host %d: %w", host.ID, err)
			continue
		}

		wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)

		// Step 1: Provision volume
		volResp, err := wClient.CreateVolume(ctx, server.ID, plan.DiskLimitGB)
		if err != nil {
			s.cleanupFailedServer(ctx, &server, wClient)
			lastErr = fmt.Errorf("worker on host %s (%s) failed creating volume: %w", host.Hostname, host.WorkerBaseURL, err)
			continue
		}

		// Step 2: Create and start container
		ctrReq := worker.CreateContainerRequest{
			ServerID:    server.ID,
			Port:        allocatedPort,
			CPULimit:    plan.CPULimit,
			MemLimitMB:  plan.MemLimitMB,
			DiskLimitGB: plan.DiskLimitGB,
			MountPath:   volResp.MountPath,
		}

		ctrResp, err := wClient.CreateContainer(ctx, ctrReq)
		if err != nil {
			_ = wClient.DeleteVolume(ctx, strconv.FormatUint(uint64(server.ID), 10))
			s.cleanupFailedServer(ctx, &server, wClient)
			lastErr = fmt.Errorf("worker on host %s (%s) failed creating container: %w", host.Hostname, host.WorkerBaseURL, err)
			continue
		}

		// Step 3: Mark server active
		server.Status = domain.ServerStatusRunning
		server.ContainerID = ctrResp.ContainerID
		server.MountPath = volResp.MountPath

		if err := s.db.WithContext(ctx).Save(&server).Error; err != nil {
			return nil, fmt.Errorf("failed updating server state: %w", err)
		}

		return &server, nil
	}

	return nil, fmt.Errorf("all candidate hosts failed deployment: %w", lastErr)
}

// cleanupFailedServer purges database state for failed provisioning to free ports.
func (s *Service) cleanupFailedServer(ctx context.Context, server *domain.Server, wClient worker.Client) {
	_ = s.db.WithContext(ctx).Delete(server).Error
}

// StopServer halts a running game server container via its hosting worker.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting the stop.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	srv, err := svc.StopServer(ctx, user.ID, 42)
func (s *Service) StopServer(ctx context.Context, userID uint, serverID uint) (*domain.Server, error) {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}

	if server.Status == domain.ServerStatusMigrating {
		return nil, ErrServerMigrating
	}

	if server.ContainerID != "" {
		wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
		if err := wClient.StopContainer(ctx, server.ContainerID); err != nil {
			return nil, fmt.Errorf("worker stop container failed: %w", err)
		}
	}

	server.Status = domain.ServerStatusStopped
	if err := s.db.WithContext(ctx).Save(server).Error; err != nil {
		return nil, err
	}

	return server, nil
}

// StartServer resumes a stopped game server container via its hosting worker.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting the start.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	srv, err := svc.StartServer(ctx, user.ID, 42)
func (s *Service) StartServer(ctx context.Context, userID uint, serverID uint) (*domain.Server, error) {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}

	if server.Status == domain.ServerStatusMigrating {
		return nil, ErrServerMigrating
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
	ctrReq := worker.CreateContainerRequest{
		ServerID:    server.ID,
		Port:        server.Port,
		CPULimit:    server.Plan.CPULimit,
		MemLimitMB:  server.Plan.MemLimitMB,
		DiskLimitGB: server.Plan.DiskLimitGB,
		MountPath:   server.MountPath,
	}

	ctrResp, err := wClient.CreateContainer(ctx, ctrReq)
	if err != nil {
		return nil, fmt.Errorf("worker start container failed: %w", err)
	}

	server.ContainerID = ctrResp.ContainerID
	server.Status = domain.ServerStatusRunning
	if err := s.db.WithContext(ctx).Save(server).Error; err != nil {
		return nil, err
	}

	return server, nil
}

// DeleteServer tears down container, removes persistent volume, and deletes the server record.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting the deletion.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	err := svc.DeleteServer(ctx, user.ID, 42)
func (s *Service) DeleteServer(ctx context.Context, userID uint, serverID uint) error {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return err
	}

	if server.Status == domain.ServerStatusMigrating {
		return ErrServerMigrating
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)

	containerRef := server.ContainerID
	if containerRef == "" {
		containerRef = fmt.Sprintf("mc-server-%d", server.ID)
	}
	if err := wClient.DeleteContainer(ctx, containerRef); err != nil {
		return fmt.Errorf("worker delete container failed: %w", err)
	}

	volumeID := strconv.FormatUint(uint64(server.ID), 10)
	if err := wClient.DeleteVolume(ctx, volumeID); err != nil {
		return fmt.Errorf("worker delete volume failed: %w", err)
	}

	if host.FileServerBaseURL != "" && s.fileServerClientFactory != nil {
		fsClient := s.fileServerClientFactory(host.FileServerBaseURL, host.APIKey)
		if err := fsClient.RevokeServer(ctx, server.ID); err != nil {
			log.Printf("[WARN] fileserver revoke failed for server %d on host %s: %v", server.ID, host.Hostname, err)
		}
	}

	if err := s.db.WithContext(ctx).Delete(server).Error; err != nil {
		return fmt.Errorf("failed deleting server from database: %w", err)
	}

	return nil
}

// OpenFileManagerURL provisions the user and directory permission on the target node's
// fileserver, requests a short-lived login token, and constructs the redirect URL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting file manager access.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	redirectURL, err := svc.OpenFileManagerURL(ctx, user.ID, 42)
func (s *Service) OpenFileManagerURL(ctx context.Context, userID uint, serverID uint) (string, error) {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return "", err
	}

	if host.FileServerBaseURL == "" {
		return "", fmt.Errorf("fileserver URL is not configured for host %s", host.Hostname)
	}

	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, server.UserID).Error; err != nil {
		return "", fmt.Errorf("failed loading server owner: %w", err)
	}

	fsClient := s.fileServerClientFactory(host.FileServerBaseURL, host.APIKey)
	if _, err := fsClient.EnsureUser(ctx, user.ID, user.Email); err != nil {
		return "", fmt.Errorf("failed provisioning fileserver user: %w", err)
	}

	if err := fsClient.Allow(ctx, user.ID, server.ID); err != nil {
		return "", fmt.Errorf("failed granting fileserver directory permission: %w", err)
	}

	tokResp, err := fsClient.CreateLoginToken(ctx, user.ID)
	if err != nil {
		return "", fmt.Errorf("failed creating fileserver login token: %w", err)
	}

	targetPath := server.MountPath
	if targetPath == "" {
		targetPath = fmt.Sprintf("/var/lib/mc-platform/mounts/%d", server.ID)
	}

	q := url.Values{}
	q.Set("token", tokResp.Token)
	q.Set("target", targetPath)

	base := strings.TrimRight(host.FileServerBaseURL, "/")
	return fmt.Sprintf("%s/login?%s", base, q.Encode()), nil
}

// GetServer returns server details along with live status from its worker node.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting details.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	srv, status, err := svc.GetServer(ctx, user.ID, 42)
func (s *Service) GetServer(ctx context.Context, userID uint, serverID uint) (*domain.Server, *worker.ContainerStatusResponse, error) {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, nil, err
	}

	var statusResp *worker.ContainerStatusResponse
	if server.ContainerID != "" {
		wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
		statusResp, _ = wClient.GetContainerStatus(ctx, server.ContainerID)
		// Reconcile stale running status if the container was removed (e.g. worker restart).
		if statusResp != nil && server.Status == domain.ServerStatusRunning &&
			(statusResp.Status == worker.ContainerStatusStopped || statusResp.Status == worker.ContainerStatusUnknown) {
			server.Status = domain.ServerStatusStopped
			_ = s.db.WithContext(ctx).Model(server).Update("status", domain.ServerStatusStopped).Error
		}
	}

	return server, statusResp, nil
}

// ListServers returns all game servers belonging to the user.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID.
//
// Example usage:
//
//	servers, err := svc.ListServers(ctx, user.ID)
func (s *Service) ListServers(ctx context.Context, userID uint) ([]domain.Server, error) {
	var servers []domain.Server
	err := s.db.WithContext(ctx).
		Preload("Plan").
		Preload("Host").
		Where("user_id = ?", userID).
		Find(&servers).Error
	if err != nil {
		return nil, err
	}
	return servers, nil
}

// getServerAndHost retrieves the server entity, ensures caller ownership, and preloads associated host.
func (s *Service) getServerAndHost(ctx context.Context, userID uint, serverID uint) (*domain.Server, *domain.Host, error) {
	var server domain.Server
	err := s.db.WithContext(ctx).
		Preload("Plan").
		Preload("Host").
		First(&server, serverID).Error
	if err != nil {
		return nil, nil, fmt.Errorf("server not found: %w", err)
	}

	if userID != 0 && server.UserID != userID {
		return nil, nil, errors.New("unauthorized access to server")
	}

	hostCopy := server.Host
	return &server, &hostCopy, nil
}
