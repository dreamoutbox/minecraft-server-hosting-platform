package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/auth"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// mockWorkerClient records invocations for hosting integration verification.
type mockWorkerClient struct {
	createdVolumes    map[string]bool
	createdContainers map[string]worker.CreateContainerRequest
	stoppedContainers map[string]bool
	deletedContainers map[string]bool
	deletedVolumes    map[string]bool
}

func newMockWorkerClient() *mockWorkerClient {
	return &mockWorkerClient{
		createdVolumes:    make(map[string]bool),
		createdContainers: make(map[string]worker.CreateContainerRequest),
		stoppedContainers: make(map[string]bool),
		deletedContainers: make(map[string]bool),
		deletedVolumes:    make(map[string]bool),
	}
}

func (m *mockWorkerClient) GetStatus(ctx context.Context) (*worker.HostStatusResponse, error) {
	return &worker.HostStatusResponse{
		Hostname:         "node-test",
		ActiveContainers: len(m.createdContainers),
		MaxServers:       10,
	}, nil
}

func (m *mockWorkerClient) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*worker.CreateVolumeResponse, error) {
	id := fmt.Sprintf("%d", serverID)
	m.createdVolumes[id] = true
	return &worker.CreateVolumeResponse{
		MountPath: fmt.Sprintf("/var/lib/mc-platform/mounts/%d", serverID),
		ImagePath: fmt.Sprintf("/var/lib/mc-platform/images/%d.img", serverID),
	}, nil
}

func (m *mockWorkerClient) DeleteVolume(ctx context.Context, volumeID string) error {
	m.deletedVolumes[volumeID] = true
	return nil
}

func (m *mockWorkerClient) ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("mock-tar"))), nil
}

func (m *mockWorkerClient) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	return nil
}

func (m *mockWorkerClient) CreateContainer(ctx context.Context, req worker.CreateContainerRequest) (*worker.CreateContainerResponse, error) {
	id := fmt.Sprintf("ctr-server-%d", req.ServerID)
	m.createdContainers[id] = req
	return &worker.CreateContainerResponse{
		ContainerID: id,
		Port:        req.Port,
		MountPath:   req.MountPath,
	}, nil
}

func (m *mockWorkerClient) StopContainer(ctx context.Context, containerID string) error {
	m.stoppedContainers[containerID] = true
	return nil
}

func (m *mockWorkerClient) DeleteContainer(ctx context.Context, containerID string) error {
	m.deletedContainers[containerID] = true
	return nil
}

func (m *mockWorkerClient) GetContainerStatus(ctx context.Context, containerID string) (*worker.ContainerStatusResponse, error) {
	return &worker.ContainerStatusResponse{
		ContainerID: containerID,
		Status:      worker.ContainerStatusRunning,
	}, nil
}

func setupHostingTestEnv(t *testing.T) (*gin.Engine, *Service, *mockWorkerClient, *domain.User, string) {
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil, nil, nil, nil, ""
	}
	_ = domain.AutoMigrate(db)
	_ = domain.SeedPlans(db)

	jwtSecret := "hosting-test-jwt-secret"
	mockClient := newMockWorkerClient()

	scheduler := NewLeastLoadedScheduler(db)
	portAllocator := NewPortAllocator(db, 25565, 25575)
	service := NewService(db, scheduler, portAllocator, func(baseURL, apiKey string) worker.Client {
		return mockClient
	})
	handler := NewHandler(service)

	r := gin.New()
	handler.RegisterRoutes(r, jwtSecret)

	// Create test user
	user := domain.User{
		Email:        fmt.Sprintf("hosting-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hashed",
		Role:         domain.UserRoleUser,
	}
	_ = db.Create(&user)

	tokens, _ := auth.GenerateTokenPair(user.ID, user.Email, user.Role, jwtSecret)
	return r, service, mockClient, &user, tokens.AccessToken
}

func TestSchedulerLeastLoaded(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return
	}
	_ = domain.AutoMigrate(db)

	// Clean up previous hosts for clean test
	db.Exec("DELETE FROM servers")
	db.Exec("DELETE FROM hosts")

	// Host 1: capacity 2, 1 server -> 50%
	h1 := domain.Host{Hostname: "node-1", WorkerBaseURL: "http://node-1:9090", APIKey: "k1", APIKeyHash: "h1", MaxServers: 2, Status: domain.HostStatusActive}
	db.Create(&h1)

	// Host 2: capacity 10, 1 server -> 10% (should be picked)
	h2 := domain.Host{Hostname: "node-2", WorkerBaseURL: "http://node-2:9090", APIKey: "k2", APIKeyHash: "h2", MaxServers: 10, Status: domain.HostStatusActive}
	db.Create(&h2)

	var plan domain.Plan
	_ = db.First(&plan)

	// Add 1 server to each
	db.Create(&domain.Server{UserID: 1, PlanID: plan.ID, HostID: h1.ID, Name: "s1", Port: 25565, Status: domain.ServerStatusRunning})
	db.Create(&domain.Server{UserID: 1, PlanID: plan.ID, HostID: h2.ID, Name: "s2", Port: 25566, Status: domain.ServerStatusRunning})

	scheduler := NewLeastLoadedScheduler(db)
	picked, err := scheduler.SelectHost(context.Background())
	if err != nil {
		t.Fatalf("failed picking host: %v", err)
	}

	if picked.ID != h2.ID {
		t.Errorf("expected host 2 (10%% loaded) to be picked, got host %d (%s)", picked.ID, picked.Hostname)
	}
}

func TestServerLifecycleEndToEnd(t *testing.T) {
	r, _, mockClient, user, token := setupHostingTestEnv(t)
	if r == nil {
		return
	}

	// 1. Register a host via API
	regHostReq := RegisterHostRequest{
		Hostname:      "node-edge.internal",
		WorkerBaseURL: "http://127.0.0.1:9090",
		MaxServers:    5,
	}
	body, _ := json.Marshal(regHostReq)
	wHost := httptest.NewRecorder()
	reqHost, _ := http.NewRequest(http.MethodPost, "/api/v1/hosts", bytes.NewReader(body))
	reqHost.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wHost, reqHost)

	if wHost.Code != http.StatusCreated {
		t.Fatalf("register host failed: %d: %s", wHost.Code, wHost.Body.String())
	}

	var hostResp struct {
		Host   domain.Host `json:"host"`
		APIKey string      `json:"api_key"`
	}
	_ = json.Unmarshal(wHost.Body.Bytes(), &hostResp)
	if hostResp.APIKey == "" {
		t.Fatal("expected api_key to be returned in registration response")
	}

	// 2. Create server via API
	createReq := CreateServerRequest{
		Name:   "MySurvivalServer",
		PlanID: 1, // Small plan
	}
	bodySrv, _ := json.Marshal(createReq)
	wSrv := httptest.NewRecorder()
	reqSrv, _ := http.NewRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(bodySrv))
	reqSrv.Header.Set("Authorization", "Bearer "+token)
	reqSrv.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wSrv, reqSrv)

	if wSrv.Code != http.StatusCreated {
		t.Fatalf("create server failed: %d: %s", wSrv.Code, wSrv.Body.String())
	}

	var server domain.Server
	_ = json.Unmarshal(wSrv.Body.Bytes(), &server)

	if server.ID == 0 {
		t.Fatal("expected server ID to be set")
	}
	if server.Port < 25565 || server.Port > 25575 {
		t.Errorf("allocated port %d outside range [25565, 25575]", server.Port)
	}
	if server.Status != domain.ServerStatusRunning {
		t.Errorf("expected server status running, got: %s", server.Status)
	}

	// Verify mock worker client calls
	serverIDStr := fmt.Sprintf("%d", server.ID)
	if !mockClient.createdVolumes[serverIDStr] {
		t.Error("expected volume to be created on worker")
	}
	if len(mockClient.createdContainers) == 0 {
		t.Error("expected container to be created on worker")
	}

	// 3. Query server details
	wGet := httptest.NewRecorder()
	reqGet, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/servers/%d", server.ID), nil)
	reqGet.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("get server failed: %d", wGet.Code)
	}

	// 4. Stop server
	wStop := httptest.NewRecorder()
	reqStop, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/servers/%d/stop", server.ID), nil)
	reqStop.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wStop, reqStop)
	if wStop.Code != http.StatusOK {
		t.Fatalf("stop server failed: %d", wStop.Code)
	}

	// 5. Delete server
	wDel := httptest.NewRecorder()
	reqDel, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/servers/%d", server.ID), nil)
	reqDel.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete server failed: %d", wDel.Code)
	}

	if !mockClient.deletedVolumes[serverIDStr] {
		t.Error("expected volume to be deleted on worker")
	}

	// 6. Verify user server list is empty
	wList := httptest.NewRecorder()
	reqList, _ := http.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	reqList.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wList, reqList)

	var listResp struct {
		Servers []domain.Server `json:"servers"`
	}
	_ = json.Unmarshal(wList.Body.Bytes(), &listResp)
	for _, s := range listResp.Servers {
		if s.UserID == user.ID && s.ID == server.ID {
			t.Errorf("server %d still found in user servers list", server.ID)
		}
	}
}
