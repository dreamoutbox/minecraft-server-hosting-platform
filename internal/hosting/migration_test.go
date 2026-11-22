package hosting

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/storage"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// multiHostWorkerClient simulates worker operations on a specific host node.
type multiHostWorkerClient struct {
	mu         sync.Mutex
	hostname   string
	volumes    map[string][]byte
	containers map[string]worker.CreateContainerRequest
	failHealth bool
	failVolume bool
}

func newMultiHostWorkerClient(hostname string) *multiHostWorkerClient {
	return &multiHostWorkerClient{
		hostname:   hostname,
		volumes:    make(map[string][]byte),
		containers: make(map[string]worker.CreateContainerRequest),
	}
}

func (m *multiHostWorkerClient) GetStatus(ctx context.Context) (*worker.HostStatusResponse, error) {
	return &worker.HostStatusResponse{Hostname: m.hostname, MaxServers: 10}, nil
}

func (m *multiHostWorkerClient) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*worker.CreateVolumeResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failVolume {
		return nil, fmt.Errorf("simulated volume failure on %s", m.hostname)
	}
	id := fmt.Sprintf("%d", serverID)
	if _, exists := m.volumes[id]; !exists {
		m.volumes[id] = []byte("empty-vol")
	}
	return &worker.CreateVolumeResponse{
		MountPath: fmt.Sprintf("/var/lib/mounts/%s/%d", m.hostname, serverID),
		ImagePath: fmt.Sprintf("/var/lib/images/%s/%d.img", m.hostname, serverID),
	}, nil
}

func (m *multiHostWorkerClient) DeleteVolume(ctx context.Context, volumeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.volumes, volumeID)
	return nil
}

func (m *multiHostWorkerClient) ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := m.volumes[volumeID]
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *multiHostWorkerClient) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.volumes[volumeID] = data
	return nil
}

func (m *multiHostWorkerClient) UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (*worker.UploadVolumeBackupResponse, error) {
	return &worker.UploadVolumeBackupResponse{SizeBytes: 0}, nil
}

func (m *multiHostWorkerClient) RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error {
	return nil
}

func (m *multiHostWorkerClient) CreateContainer(ctx context.Context, req worker.CreateContainerRequest) (*worker.CreateContainerResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("ctr-%s-%d", m.hostname, req.ServerID)
	m.containers[id] = req
	return &worker.CreateContainerResponse{
		ContainerID: id,
		Port:        req.Port,
		MountPath:   req.MountPath,
	}, nil
}

func (m *multiHostWorkerClient) StopContainer(ctx context.Context, containerID string) error {
	return nil
}

func (m *multiHostWorkerClient) DeleteContainer(ctx context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.containers, containerID)
	return nil
}

func (m *multiHostWorkerClient) GetContainerStatus(ctx context.Context, containerID string) (*worker.ContainerStatusResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failHealth {
		return &worker.ContainerStatusResponse{
			ContainerID: containerID,
			Status:      worker.ContainerStatusStopped,
		}, nil
	}
	return &worker.ContainerStatusResponse{
		ContainerID: containerID,
		Status:      worker.ContainerStatusRunning,
	}, nil
}

func (m *multiHostWorkerClient) GetContainerLogs(ctx context.Context, containerID string, tail int) (*worker.ContainerLogsResponse, error) {
	return &worker.ContainerLogsResponse{ContainerID: containerID, Logs: ""}, nil
}

func (m *multiHostWorkerClient) ExecContainerCommand(ctx context.Context, containerID string, command string) (*worker.ExecCommandResponse, error) {
	return &worker.ExecCommandResponse{ContainerID: containerID, Command: command, Output: ""}, nil
}

func setupMigrationTestEnv(t *testing.T) (*MigrationService, *Service, map[string]*multiHostWorkerClient, *domain.User, *domain.Host, *domain.Host, *domain.Server) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil, nil, nil, nil, nil, nil, nil
	}
	_ = domain.AutoMigrate(db)
	_ = domain.SeedPlans(db)

	workers := map[string]*multiHostWorkerClient{
		"http://node-alpha:9090": newMultiHostWorkerClient("node-alpha"),
		"http://node-beta:9090":  newMultiHostWorkerClient("node-beta"),
	}

	workerFactory := func(baseURL, apiKey string) worker.Client {
		if client, ok := workers[baseURL]; ok {
			return client
		}
		return newMultiHostWorkerClient("unknown")
	}

	memStorage := storage.NewMemoryStorageProvider()
	scheduler := NewLeastLoadedScheduler(db)
	portAllocator := NewPortAllocator(db, 25570, 25590)
	hostingSvc := NewService(db, scheduler, portAllocator, workerFactory)
	backupSvc := backup.NewService(db, memStorage, workerFactory)
	migrationSvc := NewMigrationService(hostingSvc, backupSvc, memStorage, workerFactory)

	user := domain.User{
		Email:        fmt.Sprintf("mig-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hashed",
		Role:         domain.UserRoleUser,
	}
	_ = db.Create(&user)

	h1 := domain.Host{Hostname: "node-alpha", WorkerBaseURL: "http://node-alpha:9090", APIKey: "k1", APIKeyHash: "h1", MaxServers: 10, Status: domain.HostStatusActive}
	h2 := domain.Host{Hostname: "node-beta", WorkerBaseURL: "http://node-beta:9090", APIKey: "k2", APIKeyHash: "h2", MaxServers: 10, Status: domain.HostStatusActive}
	_ = db.Create(&h1)
	_ = db.Create(&h2)

	var plan domain.Plan
	_ = db.First(&plan)

	server := domain.Server{
		UserID:      user.ID,
		PlanID:      plan.ID,
		HostID:      h1.ID,
		Name:        "MigratableServer",
		Port:        25577,
		Status:      domain.ServerStatusRunning,
		ContainerID: "ctr-node-alpha-origin",
		MountPath:   "/var/lib/mounts/node-alpha/target",
	}
	db.Where("port = ?", 25577).Delete(&domain.Server{})
	_ = db.Create(&server)

	// Set initial world data on source host
	workers["http://node-alpha:9090"].volumes[fmt.Sprintf("%d", server.ID)] = []byte("valuable-world-chunks-12345")

	return migrationSvc, hostingSvc, workers, &user, &h1, &h2, &server
}

func TestSuccessfulMigrationPreservingPort(t *testing.T) {
	migrationSvc, hostingSvc, workers, user, _, h2, server := setupMigrationTestEnv(t)
	if migrationSvc == nil {
		return
	}

	ctx := context.Background()
	volID := fmt.Sprintf("%d", server.ID)

	// Execute migration
	err := migrationSvc.MigrateServer(ctx, user.ID, server.ID, h2.ID)
	if err != nil {
		t.Fatalf("MigrateServer failed: %v", err)
	}

	// 1. Reload server from database
	var migrated domain.Server
	if err := hostingSvc.db.First(&migrated, server.ID).Error; err != nil {
		t.Fatalf("failed reloading server: %v", err)
	}

	// 2. Verify server moved to destination host
	if migrated.HostID != h2.ID {
		t.Errorf("expected host_id to be %d, got %d", h2.ID, migrated.HostID)
	}

	// 3. Verify port is preserved
	if migrated.Port != server.Port {
		t.Errorf("port changed after migration: got %d, expected %d", migrated.Port, server.Port)
	}

	// 4. Verify world data transferred intact to destination worker
	destWorker := workers["http://node-beta:9090"]
	destData := destWorker.volumes[volID]
	if string(destData) != "valuable-world-chunks-12345" {
		t.Errorf("world data mismatch on destination: got %s", string(destData))
	}

	// 5. Verify source worker resources were released
	srcWorker := workers["http://node-alpha:9090"]
	if _, exists := srcWorker.volumes[volID]; exists {
		t.Error("expected source volume to be removed after successful migration")
	}
	if _, exists := srcWorker.containers["ctr-node-alpha-origin"]; exists {
		t.Error("expected source container to be removed after successful migration")
	}

	// 6. Verify status is running
	if migrated.Status != domain.ServerStatusRunning {
		t.Errorf("expected status running, got: %s", migrated.Status)
	}
}

func TestForcedFailureMidMigrationLeavesSourceRunning(t *testing.T) {
	migrationSvc, hostingSvc, workers, user, h1, h2, server := setupMigrationTestEnv(t)
	if migrationSvc == nil {
		return
	}

	ctx := context.Background()
	volID := fmt.Sprintf("%d", server.ID)

	// Cause health check to fail on destination worker
	workers["http://node-beta:9090"].failHealth = true

	// Execute migration (should fail gracefully)
	err := migrationSvc.MigrateServer(ctx, user.ID, server.ID, h2.ID)
	if err == nil {
		t.Fatal("expected migration error on destination health check failure, got nil")
	}

	// 1. Reload server from DB
	var unchanged domain.Server
	if err := hostingSvc.db.First(&unchanged, server.ID).Error; err != nil {
		t.Fatalf("failed reloading server: %v", err)
	}

	// 2. Verify server is still on source host
	if unchanged.HostID != h1.ID {
		t.Errorf("expected server to remain on source host %d, got %d", h1.ID, unchanged.HostID)
	}

	// 3. Verify server is still running on source
	if unchanged.Status != domain.ServerStatusRunning {
		t.Errorf("expected server status to revert to running, got %s", unchanged.Status)
	}

	// 4. Verify source worker still holds original volume data
	srcWorker := workers["http://node-alpha:9090"]
	if string(srcWorker.volumes[volID]) != "valuable-world-chunks-12345" {
		t.Errorf("source volume corrupted after aborted migration: got %s", string(srcWorker.volumes[volID]))
	}

	// 5. Verify destination resources were cleaned up
	destWorker := workers["http://node-beta:9090"]
	if _, exists := destWorker.volumes[volID]; exists {
		t.Error("expected destination volume to be cleaned up after migration failure")
	}
}

func TestLifecycleBlockedDuringMigration(t *testing.T) {
	_, hostingSvc, _, user, _, _, server := setupMigrationTestEnv(t)
	if hostingSvc == nil {
		return
	}

	ctx := context.Background()

	// Set server status to migrating
	server.Status = domain.ServerStatusMigrating
	_ = hostingSvc.db.Save(server)

	// Attempt StopServer -> should be rejected
	_, err := hostingSvc.StopServer(ctx, user.ID, server.ID)
	if err != ErrServerMigrating {
		t.Errorf("expected ErrServerMigrating on StopServer, got %v", err)
	}

	// Attempt StartServer -> should be rejected
	_, err = hostingSvc.StartServer(ctx, user.ID, server.ID)
	if err != ErrServerMigrating {
		t.Errorf("expected ErrServerMigrating on StartServer, got %v", err)
	}

	// Attempt DeleteServer -> should be rejected
	err = hostingSvc.DeleteServer(ctx, user.ID, server.ID)
	if err != ErrServerMigrating {
		t.Errorf("expected ErrServerMigrating on DeleteServer, got %v", err)
	}
}
