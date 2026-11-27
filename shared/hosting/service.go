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
	"time"

	"github.com/dreamoutbox/authkit"
	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
)

// WorkerClientFactory produces a typed worker client given host credentials.
type WorkerClientFactory func(baseURL string, apiKey string) nodemanagerclient.Client

// FileServerClientFactory produces a fileserver management client given host credentials.
type FileServerClientFactory func(baseURL string, apiKey string) fsclient.Client

const (
	// defaultMinecraftFileUID and defaultMinecraftFileGID match the UID/GID used by itzg/minecraft-server.
	defaultMinecraftFileUID = 1000
	defaultMinecraftFileGID = 1000
	// fileserverRandomPasswordBytes is the entropy length used when provisioning fileserver accounts.
	fileserverRandomPasswordBytes = 32
)

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
		factory = func(baseURL string, apiKey string) nodemanagerclient.Client {
			return nodemanagerclient.NewClient(baseURL, apiKey)
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
//   - publicDomain: Public DNS domain players use to connect to servers on this node.
//   - publicIP: Public IPv4/IPv6 address of this node.
//
// Example usage:
//
//	host, apiKey, err := svc.RegisterHost(ctx, "node-1.internal", "http://10.0.0.1:9090", 20, "mc1.example.com", "203.0.113.10")
func (s *Service) RegisterHost(ctx context.Context, hostname string, workerBaseURL string, maxServers int, publicDomain string, publicIP string) (*domain.Host, string, error) {
	if hostname == "" || workerBaseURL == "" || maxServers <= 0 {
		return nil, "", errors.New("invalid host registration parameters")
	}

	rawAPIKey := fmt.Sprintf("mc_%s_%s", uuid.New().String(), uuid.New().String())
	hashBytes := sha256.Sum256([]byte(rawAPIKey))
	hashHex := hex.EncodeToString(hashBytes[:])

	host := domain.Host{
		Hostname:      hostname,
		PublicDomain:  publicDomain,
		PublicIP:      publicIP,
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
		_, err := s.portAllocator.AllocateWithRetry(ctx, 5, func(port int) error {
			server = domain.Server{
				UserID:        userID,
				PlanID:        plan.ID,
				HostID:        host.ID,
				Name:          name,
				Port:          port,
				Status:        domain.ServerStatusProvisioning,
				ServerType:    domain.ServerTypeVanilla,
				ServerVersion: domain.DefaultServerVersion,
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
			domain.RecordServerLog(ctx, s.db, server.ID, userID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
			s.cleanupFailedServer(ctx, &server, wClient)
			lastErr = fmt.Errorf("worker on host %s (%s) failed creating volume: %w", host.Hostname, host.WorkerBaseURL, err)
			continue
		}

		// Step 2: Create and start container
		ctrReq := buildContainerRequest(&server, &plan, volResp.MountPath)

		ctrResp, err := wClient.CreateContainer(ctx, ctrReq)
		if err != nil {
			domain.RecordServerLog(ctx, s.db, server.ID, userID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
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
			domain.RecordServerLog(ctx, s.db, server.ID, userID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
			return nil, fmt.Errorf("failed updating server state: %w", err)
		}

		domain.RecordServerLog(ctx, s.db, server.ID, userID, domain.ServerOpCreate, domain.ServerLogStatusSuccess, fmt.Sprintf("server %s created on host %s", server.Name, host.Hostname))
		return &server, nil
	}

	domain.RecordServerLog(ctx, s.db, 0, userID, domain.ServerOpCreate, domain.ServerLogStatusError, fmt.Sprintf("create %q failed: %v", name, lastErr))
	return nil, fmt.Errorf("all candidate hosts failed deployment: %w", lastErr)
}

// PreparePendingServer reserves a host and port and creates a pending server record for async provisioning.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID of the server owner.
//   - name: Descriptive name for the game server.
//   - planID: Hardware plan tier ID.
//   - serverType: Minecraft server jar type (defaults to VANILLA).
//   - serverVersion: Minecraft version tag (defaults to LATEST).
//
// Example usage:
//
//	srv, err := svc.PreparePendingServer(ctx, user.ID, "MyWorld", plan.ID, domain.ServerTypePaper, "1.21.4")
func (s *Service) PreparePendingServer(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string) (*domain.Server, error) {
	cleanName := strings.TrimSpace(name)
	if len(cleanName) < 3 || len(cleanName) > 64 {
		return nil, errors.New("server name must be between 3 and 64 characters")
	}

	resolvedType, err := domain.ParseServerType(string(serverType))
	if err != nil {
		return nil, err
	}
	resolvedVersion, err := domain.ValidateServerVersion(serverVersion)
	if err != nil {
		return nil, err
	}

	var plan domain.Plan
	if err := s.db.WithContext(ctx).First(&plan, planID).Error; err != nil {
		return nil, fmt.Errorf("invalid plan ID %d: %w", planID, err)
	}

	candidates, err := s.scheduler.SelectCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("scheduling failed: %w", err)
	}

	var server domain.Server
	var allocErr error
	for _, host := range candidates {
		_, err := s.portAllocator.AllocateWithRetry(ctx, 5, func(port int) error {
			server = domain.Server{
				UserID:        userID,
				PlanID:        plan.ID,
				HostID:        host.ID,
				Name:          cleanName,
				Port:          port,
				Status:        domain.ServerStatusPending,
				ServerType:    resolvedType,
				ServerVersion: resolvedVersion,
			}
			return s.db.WithContext(ctx).Create(&server).Error
		})
		if err == nil {
			server.Plan = plan
			server.Host = host
			return &server, nil
		}
		allocErr = err
	}

	return nil, fmt.Errorf("failed reserving port on candidate hosts: %w", allocErr)
}

// ProvisionServer provisions disk volume and container on the server's assigned host for an existing pending/error server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	srv, err := svc.ProvisionServer(ctx, 42)
func (s *Service) ProvisionServer(ctx context.Context, serverID uint) (*domain.Server, error) {
	server, host, err := s.getServerAndHost(ctx, 0, serverID)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, serverID, 0, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
		return nil, err
	}

	server.Status = domain.ServerStatusProvisioning
	if err := s.db.WithContext(ctx).Model(server).Update("status", domain.ServerStatusProvisioning).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
		return nil, err
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)

	volResp, err := wClient.CreateVolume(ctx, server.ID, server.Plan.DiskLimitGB)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("worker on host %s failed creating volume: %w", host.Hostname, err)
	}

	ctrReq := buildContainerRequest(server, &server.Plan, volResp.MountPath)
	ctrResp, err := wClient.CreateContainer(ctx, ctrReq)
	if err != nil {
		_ = wClient.DeleteVolume(ctx, strconv.FormatUint(uint64(server.ID), 10))
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("worker on host %s failed creating container: %w", host.Hostname, err)
	}

	server.Status = domain.ServerStatusRunning
	server.ContainerID = ctrResp.ContainerID
	server.MountPath = volResp.MountPath
	if err := s.db.WithContext(ctx).Save(server).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpCreate, domain.ServerLogStatusError, err.Error())
		return nil, fmt.Errorf("failed saving provisioned server state: %w", err)
	}

	domain.RecordServerLog(ctx, s.db, server.ID, server.UserID, domain.ServerOpCreate, domain.ServerLogStatusSuccess, fmt.Sprintf("server %s provisioned on host %s", server.Name, host.Hostname))
	return server, nil
}

// cleanupFailedServer purges database state for failed provisioning to free ports.
func (s *Service) cleanupFailedServer(ctx context.Context, server *domain.Server, wClient nodemanagerclient.Client) {
	_ = s.db.WithContext(ctx).Delete(server).Error
}

// StopServer halts a running game server container via its hosting nodemanagerclient.
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
	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	if server.Status == domain.ServerStatusMigrating {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpStop, domain.ServerLogStatusError, ErrServerMigrating.Error())
		return nil, ErrServerMigrating
	}

	if server.ContainerID != "" {
		wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
		if err := wClient.StopContainer(ctx, server.ContainerID); err != nil {
			domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpStop, domain.ServerLogStatusError, err.Error())
			return nil, fmt.Errorf("worker stop container failed: %w", err)
		}
	}

	server.Status = domain.ServerStatusStopped
	if err := s.db.WithContext(ctx).Save(server).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpStop, domain.ServerLogStatusError, err.Error())
		return nil, err
	}

	domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpStop, domain.ServerLogStatusSuccess, "server stopped")
	return server, nil
}

// StartServer resumes a stopped game server container via its hosting nodemanagerclient.
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
	if server.IsExpired(time.Now()) {
		return nil, errors.New("server has expired; please extend your subscription before starting")
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
	ctrReq := buildContainerRequest(server, &server.Plan, server.MountPath)

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
		domain.RecordServerLog(ctx, s.db, serverID, userID, domain.ServerOpDelete, domain.ServerLogStatusError, err.Error())
		return err
	}
	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	if server.Status == domain.ServerStatusMigrating {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpDelete, domain.ServerLogStatusError, ErrServerMigrating.Error())
		return ErrServerMigrating
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)

	containerRef := server.ContainerID
	if containerRef == "" {
		containerRef = fmt.Sprintf("mc-server-%d", server.ID)
	}
	if err := wClient.DeleteContainer(ctx, containerRef); err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpDelete, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("worker delete container failed: %w", err)
	}

	volumeID := strconv.FormatUint(uint64(server.ID), 10)
	if err := wClient.DeleteVolume(ctx, volumeID); err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpDelete, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("worker delete volume failed: %w", err)
	}

	if host.FileServerBaseURL != "" && s.fileServerClientFactory != nil {
		fsClient := s.fileServerClientFactory(host.FileServerBaseURL, host.APIKey)
		if err := fsClient.RevokeServer(ctx, server.ID); err != nil {
			log.Printf("[WARN] fileserver revoke failed for server %d on host %s: %v", server.ID, host.Hostname, err)
		}
	}

	if err := s.db.WithContext(ctx).Delete(server).Error; err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpDelete, domain.ServerLogStatusError, err.Error())
		return fmt.Errorf("failed deleting server from database: %w", err)
	}

	domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpDelete, domain.ServerLogStatusSuccess, fmt.Sprintf("server %s deleted", server.Name))
	return nil
}

// UpdateServerSettings validates and persists jar distribution type and version for a server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting the change (0 bypasses ownership check for staff/admin).
//   - serverID: Target game server ID.
//   - serverType: Raw server jar type (VANILLA, PAPER, FABRIC, FORGE).
//   - serverVersion: Raw Minecraft version string (e.g. LATEST or 1.21.4).
//
// Example usage:
//
//	srv, err := svc.UpdateServerSettings(ctx, user.ID, 42, "PAPER", "1.21.4")
func (s *Service) UpdateServerSettings(ctx context.Context, userID uint, serverID uint, serverType string, serverVersion string) (*domain.Server, error) {
	server, _, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}

	parsedType, err := domain.ParseServerType(serverType)
	if err != nil {
		return nil, err
	}
	validVersion, err := domain.ValidateServerVersion(serverVersion)
	if err != nil {
		return nil, err
	}

	server.ServerType = parsedType
	server.ServerVersion = validVersion
	if err := s.db.WithContext(ctx).Model(server).Updates(map[string]interface{}{
		"server_type":    parsedType,
		"server_version": validVersion,
	}).Error; err != nil {
		return nil, fmt.Errorf("failed saving server settings: %w", err)
	}
	return server, nil
}

// GetServerForOwner loads server and host after checking user ownership (userID == 0 bypasses ownership check).
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Requesting user ID, or 0 for privileged/internal callers.
//   - serverID: Target game server ID.
//
// Example usage:
//
//	srv, host, err := svc.GetServerForOwner(ctx, user.ID, 42)
func (s *Service) GetServerForOwner(ctx context.Context, userID uint, serverID uint) (*domain.Server, *domain.Host, error) {
	return s.getServerAndHost(ctx, userID, serverID)
}

func buildContainerRequest(server *domain.Server, plan *domain.Plan, mountPath string) nodemanagerclient.CreateContainerRequest {
	return nodemanagerclient.CreateContainerRequest{
		ServerID:      server.ID,
		Port:          server.Port,
		CPULimit:      plan.CPULimit,
		MemLimitMB:    plan.MemLimitMB,
		DiskLimitGB:   plan.DiskLimitGB,
		MountPath:     mountPath,
		ServerType:    string(server.ServerType),
		ServerVersion: server.ServerVersion,
	}
}

// OpenFileManagerURL provisions the requesting user and directory permission on the target node's
// fileserver, requests a short-lived login token, and constructs the redirect URL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - actorUserID: Account ID requesting file manager access (provisioned on the fileserver).
//   - serverID: Target game server ID.
//   - allowNonOwner: When true (staff/admin), bypasses server ownership check and grants access to actorUserID.
//
// Example usage:
//
//	redirectURL, err := svc.OpenFileManagerURL(ctx, user.ID, 42, false)
func (s *Service) OpenFileManagerURL(ctx context.Context, actorUserID uint, serverID uint, allowNonOwner bool) (string, error) {
	ownerCheckID := actorUserID
	if allowNonOwner {
		ownerCheckID = 0
	}
	server, host, err := s.getServerAndHost(ctx, ownerCheckID, serverID)
	if err != nil {
		return "", err
	}

	if host.FileServerBaseURL == "" {
		return "", fmt.Errorf("fileserver URL is not configured for host %s", host.Hostname)
	}

	targetUserID := actorUserID
	if targetUserID == 0 {
		targetUserID = server.UserID
	}
	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, targetUserID).Error; err != nil {
		return "", fmt.Errorf("failed loading fileserver user: %w", err)
	}

	randomPassword, err := authkit.GenerateRandomPassword(fileserverRandomPasswordBytes)
	if err != nil {
		return "", fmt.Errorf("failed generating fileserver user password: %w", err)
	}

	fileUID := defaultMinecraftFileUID
	fileGID := defaultMinecraftFileGID
	fsClient := s.fileServerClientFactory(host.FileServerBaseURL, host.APIKey)
	if _, err := fsClient.RegisterUser(ctx, user.ID, user.Email, randomPassword, &fileUID, &fileGID); err != nil {
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
func (s *Service) GetServer(ctx context.Context, userID uint, serverID uint) (*domain.Server, *nodemanagerclient.ContainerStatusResponse, error) {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, nil, err
	}

	var statusResp *nodemanagerclient.ContainerStatusResponse
	if server.ContainerID != "" {
		wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
		statusResp, _ = wClient.GetContainerStatus(ctx, server.ContainerID)
		// Reconcile stale running status if the container was removed (e.g. worker restart).
		if statusResp != nil && server.Status == domain.ServerStatusRunning &&
			(statusResp.Status == nodemanagerclient.ContainerStatusStopped || statusResp.Status == nodemanagerclient.ContainerStatusUnknown) {
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

// GetServerConsoleLogs retrieves trailing container console logs for a user's Minecraft server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting access (or 0 to bypass ownership check).
//   - serverID: Target server primary key.
//   - tail: Maximum number of trailing lines to return.
//
// Example usage:
//
//	logs, err := svc.GetServerConsoleLogs(ctx, user.ID, server.ID, 200)
func (s *Service) GetServerConsoleLogs(ctx context.Context, userID uint, serverID uint, tail int) (*nodemanagerclient.ContainerLogsResponse, error) {
	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}
	if server.ContainerID == "" {
		return &nodemanagerclient.ContainerLogsResponse{Logs: ""}, nil
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
	return wClient.GetContainerLogs(ctx, server.ContainerID, tail)
}

// SendServerCommand sends a Minecraft console command to a running server container via RCON.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID requesting execution (or 0 to bypass ownership check).
//   - serverID: Target server primary key.
//   - command: Minecraft console command string.
//
// Example usage:
//
//	res, err := svc.SendServerCommand(ctx, user.ID, server.ID, "list")
func (s *Service) SendServerCommand(ctx context.Context, userID uint, serverID uint, command string) (*nodemanagerclient.ExecCommandResponse, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil, errors.New("command cannot be empty")
	}

	server, host, err := s.getServerAndHost(ctx, userID, serverID)
	if err != nil {
		return nil, err
	}
	if server.ContainerID == "" {
		return nil, errors.New("server has no provisioned container")
	}
	if server.Status != domain.ServerStatusRunning {
		return nil, fmt.Errorf("server must be running to send console commands (current status: %s)", server.Status)
	}

	actorID := userID
	if actorID == 0 {
		actorID = server.UserID
	}

	wClient := s.workerClientFactory(host.WorkerBaseURL, host.APIKey)
	res, err := wClient.ExecContainerCommand(ctx, server.ContainerID, trimmed)
	if err != nil {
		domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpConsoleCommand, domain.ServerLogStatusError, fmt.Sprintf("command %q failed: %v", trimmed, err))
		return nil, err
	}

	domain.RecordServerLog(ctx, s.db, server.ID, actorID, domain.ServerOpConsoleCommand, domain.ServerLogStatusSuccess, fmt.Sprintf("executed %q", trimmed))
	return res, nil
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
