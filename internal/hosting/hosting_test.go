package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dreamoutbox/authkit"
	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/gin-gonic/gin"

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

func (m *mockWorkerClient) UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (*worker.UploadVolumeBackupResponse, error) {
	return &worker.UploadVolumeBackupResponse{SizeBytes: 0}, nil
}

func (m *mockWorkerClient) RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error {
	return nil
}

func (m *mockWorkerClient) UploadImageBackup(ctx context.Context, volumeID string, req worker.UploadImageBackupRequest) (*worker.UploadImageBackupResponse, error) {
	return &worker.UploadImageBackupResponse{}, nil
}

func (m *mockWorkerClient) RestoreImageBackup(ctx context.Context, volumeID string, presignedURLs []string) (*worker.RestoreImageBackupResponse, error) {
	return &worker.RestoreImageBackupResponse{}, nil
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

func (m *mockWorkerClient) GetContainerLogs(ctx context.Context, containerID string, tail int) (*worker.ContainerLogsResponse, error) {
	return &worker.ContainerLogsResponse{
		ContainerID: containerID,
		Logs:        "[Server thread/INFO]: Starting minecraft server\n",
	}, nil
}

func (m *mockWorkerClient) ExecContainerCommand(ctx context.Context, containerID string, command string) (*worker.ExecCommandResponse, error) {
	return &worker.ExecCommandResponse{
		ContainerID: containerID,
		Command:     command,
		Output:      "ok: " + command,
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

	tokens, _ := authkit.GenerateTokenPair(user.ID, user.Email, user.Role, jwtSecret)
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

	// Disable any pre-existing hosts so they don't interfere with load calculation
	db.Model(&domain.Host{}).Where("1 = 1").Update("status", domain.HostStatusDisabled)

	// Host 1: capacity 2, 1 server -> 50%
	h1 := domain.Host{Hostname: fmt.Sprintf("node-1-%d", time.Now().UnixNano()), WorkerBaseURL: "http://node-1:9090", APIKey: "k1", APIKeyHash: "h1", MaxServers: 2, Status: domain.HostStatusActive}
	db.Create(&h1)

	// Host 2: capacity 10, 1 server -> 10% (should be picked)
	h2 := domain.Host{Hostname: fmt.Sprintf("node-2-%d", time.Now().UnixNano()), WorkerBaseURL: "http://node-2:9090", APIKey: "k2", APIKeyHash: "h2", MaxServers: 10, Status: domain.HostStatusActive}
	db.Create(&h2)

	var plan domain.Plan
	_ = db.First(&plan)

	port1 := 25501
	port2 := 25502
	db.Where("port IN (?)", []int{port1, port2}).Delete(&domain.Server{})

	// Add 1 server to each
	if err := db.Create(&domain.Server{UserID: 1, PlanID: plan.ID, HostID: h1.ID, Name: "s1", Port: port1, Status: domain.ServerStatusRunning}).Error; err != nil {
		t.Fatalf("failed inserting server 1: %v", err)
	}
	if err := db.Create(&domain.Server{UserID: 1, PlanID: plan.ID, HostID: h2.ID, Name: "s2", Port: port2, Status: domain.ServerStatusRunning}).Error; err != nil {
		t.Fatalf("failed inserting server 2: %v", err)
	}

	scheduler := NewLeastLoadedScheduler(db)
	candidates, err := scheduler.SelectCandidates(context.Background())
	if err != nil {
		t.Fatalf("failed picking host: %v", err)
	}

	var h1Idx, h2Idx = -1, -1
	for i, c := range candidates {
		if c.ID == h1.ID {
			h1Idx = i
		}
		if c.ID == h2.ID {
			h2Idx = i
		}
	}
	if h2Idx == -1 || h1Idx == -1 {
		t.Fatalf("expected both h1 and h2 in candidates, got h1Idx=%d, h2Idx=%d", h1Idx, h2Idx)
	}
	if h2Idx >= h1Idx {
		t.Errorf("expected h2 (10%% load) to be ranked higher than h1 (50%% load), got h2 at %d, h1 at %d", h2Idx, h1Idx)
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

func TestCreateServerFallbackOnUnreachableHost(t *testing.T) {
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
	_ = domain.SeedPlans(db)

	// Disable pre-existing hosts
	db.Model(&domain.Host{}).Where("1 = 1").Update("status", domain.HostStatusDisabled)

	// Host 1: 0 servers, will fail (offline)
	hOffline := domain.Host{
		Hostname:      fmt.Sprintf("offline-node-%d", time.Now().UnixNano()),
		WorkerBaseURL: "http://offline-node:9090",
		APIKey:        "offline-key",
		APIKeyHash:    "offline-hash",
		MaxServers:    10,
		Status:        domain.HostStatusActive,
	}
	if err := db.Create(&hOffline).Error; err != nil {
		t.Fatalf("failed creating offline host: %v", err)
	}

	// Host 2: 0 servers, will succeed (healthy)
	hHealthy := domain.Host{
		Hostname:      fmt.Sprintf("healthy-node-%d", time.Now().UnixNano()),
		WorkerBaseURL: "http://healthy-node:9090",
		APIKey:        "healthy-key",
		APIKeyHash:    "healthy-hash",
		MaxServers:    10,
		Status:        domain.HostStatusActive,
	}
	if err := db.Create(&hHealthy).Error; err != nil {
		t.Fatalf("failed creating healthy host: %v", err)
	}

	healthyClient := newMockWorkerClient()

	workerFactory := func(baseURL, apiKey string) worker.Client {
		if baseURL == hOffline.WorkerBaseURL {
			return &failingWorkerClient{}
		}
		return healthyClient
	}

	testPortMin := 25680
	testPortMax := 25690
	db.Where("port >= ? AND port <= ?", testPortMin, testPortMax).Delete(&domain.Server{})

	scheduler := NewLeastLoadedScheduler(db)
	portAllocator := NewPortAllocator(db, testPortMin, testPortMax)
	service := NewService(db, scheduler, portAllocator, workerFactory)

	var plan domain.Plan
	_ = db.First(&plan)

	srv, err := service.CreateServer(context.Background(), 1, "FallbackServer", plan.ID)
	if err != nil {
		t.Fatalf("expected CreateServer to fallback to healthy host, failed with: %v", err)
	}

	if srv.HostID != hHealthy.ID {
		t.Errorf("expected server placed on healthy host %d, got %d", hHealthy.ID, srv.HostID)
	}

	// Clean up test server and hosts
	db.Delete(srv)
	db.Delete(&hOffline)
	db.Delete(&hHealthy)
}

// failingWorkerClient simulates an unreachable worker daemon.
type failingWorkerClient struct {
	mockWorkerClient
}

func (f *failingWorkerClient) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*worker.CreateVolumeResponse, error) {
	return nil, errors.New("dial tcp: connect: connection refused")
}

func TestOpenFileManagerURLAndRevokeOnDelete(t *testing.T) {
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
	_ = domain.SeedPlans(db)

	var callOrder []string
	var lastEnsureReq fsclient.EnsureUserRequest
	var lastPermReq fsclient.PermissionRequest
	var lastTokenPath string
	var revokedServerID string
	var failRevoke bool

	fakeFS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fs-secret-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/users":
			callOrder = append(callOrder, "ensure_user")
			_ = json.NewDecoder(r.Body).Decode(&lastEnsureReq)
			_ = json.NewEncoder(w).Encode(fsclient.EnsureUserResponse{ID: lastEnsureReq.ID, Email: lastEnsureReq.Email, Created: true})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/permissions":
			callOrder = append(callOrder, "allow")
			_ = json.NewDecoder(r.Body).Decode(&lastPermReq)
			_ = json.NewEncoder(w).Encode(fsclient.PermissionResponse{Allowed: true})
		case r.Method == http.MethodPost && r.URL.Path[len(r.URL.Path)-len("/login-token"):] == "/login-token":
			callOrder = append(callOrder, "login_token")
			lastTokenPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(fsclient.LoginTokenResponse{Token: "tok-abc", ExpiresAt: time.Now().Add(time.Minute)})
		case r.Method == http.MethodDelete && r.URL.Path != "":
			if failRevoke {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			revokedServerID = r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeFS.Close()

	db.Model(&domain.Host{}).Where("1 = 1").Update("status", domain.HostStatusDisabled)
	host := domain.Host{
		Hostname:          fmt.Sprintf("fs-node-%d", time.Now().UnixNano()),
		WorkerBaseURL:     "http://fs-worker:9090",
		FileServerBaseURL: fakeFS.URL,
		APIKey:            "fs-secret-key",
		APIKeyHash:        "fs-hash",
		MaxServers:        10,
		Status:            domain.HostStatusActive,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("create host: %v", err)
	}

	user := domain.User{
		Email:        fmt.Sprintf("fs-owner-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hashed",
		Role:         domain.UserRoleUser,
	}
	adminUser := domain.User{
		Email:        fmt.Sprintf("fs-admin-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hashed",
		Role:         domain.UserRoleAdmin,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&adminUser).Error; err != nil {
		t.Fatalf("create admin user: %v", err)
	}

	var plan domain.Plan
	_ = db.First(&plan)

	portMin, portMax := 25760, 25770
	db.Where("port >= ? AND port <= ?", portMin, portMax).Delete(&domain.Server{})

	mockWorker := newMockWorkerClient()
	svc := NewService(db, NewLeastLoadedScheduler(db), NewPortAllocator(db, portMin, portMax), func(baseURL, apiKey string) worker.Client {
		return mockWorker
	})

	srv, err := svc.CreateServer(context.Background(), user.ID, "FSWorld", plan.ID)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	openURL, err := svc.OpenFileManagerURL(context.Background(), user.ID, srv.ID, false)
	if err != nil {
		t.Fatalf("OpenFileManagerURL: %v", err)
	}

	expectedPrefix := fakeFS.URL + "/login?"
	if len(openURL) <= len(expectedPrefix) || openURL[:len(expectedPrefix)] != expectedPrefix {
		t.Fatalf("unexpected openURL: %s", openURL)
	}
	expectedCalls := []string{"ensure_user", "allow", "login_token"}
	if len(callOrder) != len(expectedCalls) {
		t.Fatalf("expected call order %v, got %v", expectedCalls, callOrder)
	}
	for i := range expectedCalls {
		if callOrder[i] != expectedCalls[i] {
			t.Errorf("call %d: expected %s, got %s", i, expectedCalls[i], callOrder[i])
		}
	}

	// Non-owner without allowNonOwner must be rejected.
	if _, err := svc.OpenFileManagerURL(context.Background(), adminUser.ID, srv.ID, false); err == nil {
		t.Fatalf("expected unauthorized error when allowNonOwner is false")
	}

	// Staff/admin with allowNonOwner=true must provision and grant access to the admin user, not the server owner.
	callOrder = nil
	adminOpenURL, err := svc.OpenFileManagerURL(context.Background(), adminUser.ID, srv.ID, true)
	if err != nil {
		t.Fatalf("OpenFileManagerURL (admin): %v", err)
	}
	if len(adminOpenURL) <= len(expectedPrefix) || adminOpenURL[:len(expectedPrefix)] != expectedPrefix {
		t.Fatalf("unexpected admin openURL: %s", adminOpenURL)
	}
	if lastEnsureReq.ID != adminUser.ID || lastEnsureReq.Email != adminUser.Email {
		t.Errorf("expected fileserver EnsureUser for admin (%d, %s), got (%d, %s)", adminUser.ID, adminUser.Email, lastEnsureReq.ID, lastEnsureReq.Email)
	}
	if lastPermReq.UserID != adminUser.ID || lastPermReq.ServerID != srv.ID {
		t.Errorf("expected fileserver Allow(%d, %d), got (%d, %d)", adminUser.ID, srv.ID, lastPermReq.UserID, lastPermReq.ServerID)
	}
	expectedTokenPath := fmt.Sprintf("/api/v1/users/%d/login-token", adminUser.ID)
	if lastTokenPath != expectedTokenPath {
		t.Errorf("expected login token path %s, got %s", expectedTokenPath, lastTokenPath)
	}

	// Verify DeleteServer revokes fileserver permissions and tolerates fileserver errors.
	if err := svc.DeleteServer(context.Background(), user.ID, srv.ID); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	expectedRevokePath := fmt.Sprintf("/api/v1/servers/%d/permissions", srv.ID)
	if revokedServerID != expectedRevokePath {
		t.Errorf("expected revoke path %s, got %s", expectedRevokePath, revokedServerID)
	}

	// Second server: fail revoke must not fail DeleteServer.
	srv2, err := svc.CreateServer(context.Background(), user.ID, "FSWorld2", plan.ID)
	if err != nil {
		t.Fatalf("CreateServer 2: %v", err)
	}
	failRevoke = true
	if err := svc.DeleteServer(context.Background(), user.ID, srv2.ID); err != nil {
		t.Fatalf("DeleteServer should succeed even when fileserver revoke fails: %v", err)
	}

	db.Delete(&host)
	db.Delete(&user)
	db.Delete(&adminUser)
}
