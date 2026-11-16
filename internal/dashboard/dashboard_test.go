package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/storage"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

type dashboardTestWorker struct {
	mu         sync.Mutex
	volumes    map[string][]byte
	containers map[string]worker.CreateContainerRequest
}

func newDashboardTestWorker() *dashboardTestWorker {
	return &dashboardTestWorker{
		volumes:    make(map[string][]byte),
		containers: make(map[string]worker.CreateContainerRequest),
	}
}

func (d *dashboardTestWorker) GetStatus(ctx context.Context) (*worker.HostStatusResponse, error) {
	return &worker.HostStatusResponse{Hostname: "dash-node", MaxServers: 10}, nil
}

func (d *dashboardTestWorker) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*worker.CreateVolumeResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.volumes[fmt.Sprintf("%d", serverID)] = []byte("initial-chunk")
	return &worker.CreateVolumeResponse{MountPath: "/var/lib/mounts/dash"}, nil
}

func (d *dashboardTestWorker) DeleteVolume(ctx context.Context, volumeID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.volumes, volumeID)
	return nil
}

func (d *dashboardTestWorker) ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data := d.volumes[volumeID]
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *dashboardTestWorker) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, _ := io.ReadAll(r)
	d.volumes[volumeID] = data
	return nil
}

func (d *dashboardTestWorker) CreateContainer(ctx context.Context, req worker.CreateContainerRequest) (*worker.CreateContainerResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := fmt.Sprintf("ctr-dash-%d", req.ServerID)
	d.containers[id] = req
	return &worker.CreateContainerResponse{ContainerID: id, Port: req.Port, MountPath: req.MountPath}, nil
}

func (d *dashboardTestWorker) StopContainer(ctx context.Context, containerID string) error {
	return nil
}

func (d *dashboardTestWorker) DeleteContainer(ctx context.Context, containerID string) error {
	return nil
}

func (d *dashboardTestWorker) GetContainerStatus(ctx context.Context, containerID string) (*worker.ContainerStatusResponse, error) {
	return &worker.ContainerStatusResponse{ContainerID: containerID, Status: worker.ContainerStatusRunning}, nil
}

func setupDashboardTestEnv(t *testing.T) (*gin.Engine, *Service, string) {
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil, nil, ""
	}
	_ = domain.AutoMigrate(db)
	_ = domain.SeedPlans(db)

	mockWorker := newDashboardTestWorker()
	workerFactory := func(baseURL, apiKey string) worker.Client {
		return mockWorker
	}

	jwtSecret := "dashboard-test-jwt-secret"
	scheduler := hosting.NewLeastLoadedScheduler(db)
	portMin := 25700
	portMax := 25750
	db.Where("port >= ? AND port <= ?", portMin, portMax).Delete(&domain.Server{})
	portAllocator := hosting.NewPortAllocator(db, portMin, portMax)
	hostingSvc := hosting.NewService(db, scheduler, portAllocator, workerFactory)

	memStorage := storage.NewMemoryStorageProvider()
	backupSvc := backup.NewService(db, memStorage, workerFactory)
	migrationSvc := hosting.NewMigrationService(hostingSvc, backupSvc, memStorage, workerFactory)

	paymentProvider := billing.NewMockPaymentProvider()
	billingSvc := billing.NewService(db, paymentProvider, hostingSvc, "whsec_test")

	fakeFS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/users":
			_ = json.NewEncoder(w).Encode(fsclient.EnsureUserResponse{ID: 1, Email: "dash@example.com", Created: true})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/permissions":
			_ = json.NewEncoder(w).Encode(fsclient.PermissionResponse{Allowed: true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/login-token"):
			_ = json.NewEncoder(w).Encode(fsclient.LoginTokenResponse{Token: "dash-login-tok", ExpiresAt: time.Now().Add(time.Minute)})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fakeFS.Close)

	// Ensure at least two active hosts for migration
	h1 := domain.Host{Hostname: "dash-node-1", WorkerBaseURL: "http://dash-node-1:9090", FileServerBaseURL: fakeFS.URL, APIKey: "k1", APIKeyHash: "h1", MaxServers: 10, Status: domain.HostStatusActive}
	h2 := domain.Host{Hostname: "dash-node-2", WorkerBaseURL: "http://dash-node-2:9090", FileServerBaseURL: fakeFS.URL, APIKey: "k2", APIKeyHash: "h2", MaxServers: 10, Status: domain.HostStatusActive}
	_ = db.Where("hostname = ?", h1.Hostname).FirstOrCreate(&h1)
	_ = db.Where("hostname = ?", h2.Hostname).FirstOrCreate(&h2)
	db.Model(&domain.Host{}).Where("hostname IN (?)", []string{h1.Hostname, h2.Hostname}).Updates(map[string]interface{}{
		"status":               domain.HostStatusActive,
		"file_server_base_url": fakeFS.URL,
	})

	dashService := NewService(db, hostingSvc, backupSvc, billingSvc, migrationSvc, jwtSecret)

	r := gin.New()
	dashService.RegisterRoutes(r)

	return r, dashService, fakeFS.URL
}

func extractTokenCookie(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "token" {
			return c.Value
		}
	}
	return ""
}

func TestDashboardEndToEndBrowserFlow(t *testing.T) {
	r, dashSvc, fakeFSURL := setupDashboardTestEnv(t)
	if r == nil {
		return
	}

	uniqueEmail := fmt.Sprintf("dash-browser-%d@example.com", time.Now().UnixNano())
	password := "SecurePassword123!"

	// 1. Visit /login
	wLogin := httptest.NewRecorder()
	reqLogin, _ := http.NewRequest(http.MethodGet, "/login", nil)
	r.ServeHTTP(wLogin, reqLogin)
	if wLogin.Code != http.StatusOK || !strings.Contains(wLogin.Body.String(), "Sign In") {
		t.Fatalf("login page failed: %d", wLogin.Code)
	}

	// 2. Register: POST /register
	formReg := url.Values{}
	formReg.Set("email", uniqueEmail)
	formReg.Set("password", password)
	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/register", strings.NewReader(formReg.Encode()))
	reqReg.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(wReg, reqReg)

	if wReg.Code != http.StatusFound {
		t.Fatalf("register failed: %d", wReg.Code)
	}
	cookieToken := extractTokenCookie(wReg.Result())
	if cookieToken == "" {
		t.Fatal("expected session token cookie on registration")
	}

	authCookie := &http.Cookie{Name: "token", Value: cookieToken}

	// 3. Add payment method: POST /dashboard/billing/payment-methods
	formPM := url.Values{}
	formPM.Set("payment_method_id", "pm_card_mock_visa")
	wPM := httptest.NewRecorder()
	reqPM, _ := http.NewRequest(http.MethodPost, "/dashboard/billing/payment-methods", strings.NewReader(formPM.Encode()))
	reqPM.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqPM.AddCookie(authCookie)
	r.ServeHTTP(wPM, reqPM)

	if wPM.Code != http.StatusFound {
		t.Fatalf("save payment method failed: %d", wPM.Code)
	}

	// 4. View /dashboard/servers/new
	wNewSrv := httptest.NewRecorder()
	reqNewSrv, _ := http.NewRequest(http.MethodGet, "/dashboard/servers/new", nil)
	reqNewSrv.AddCookie(authCookie)
	r.ServeHTTP(wNewSrv, reqNewSrv)
	if wNewSrv.Code != http.StatusOK || !strings.Contains(wNewSrv.Body.String(), "Create Minecraft Server") {
		t.Fatalf("create server page failed: %d", wNewSrv.Code)
	}

	// 5. Create Server: POST /dashboard/servers/new
	var plan domain.Plan
	_ = dashSvc.db.First(&plan)

	srvName := fmt.Sprintf("DashboardSurvival-%d", time.Now().UnixNano())
	formSrv := url.Values{}
	formSrv.Set("name", srvName)
	formSrv.Set("plan_id", fmt.Sprintf("%d", plan.ID))
	wCreateSrv := httptest.NewRecorder()
	reqCreateSrv, _ := http.NewRequest(http.MethodPost, "/dashboard/servers/new", strings.NewReader(formSrv.Encode()))
	reqCreateSrv.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqCreateSrv.AddCookie(authCookie)
	r.ServeHTTP(wCreateSrv, reqCreateSrv)

	if wCreateSrv.Code != http.StatusFound {
		t.Fatalf("create server post failed: %d, body: %s", wCreateSrv.Code, wCreateSrv.Body.String())
	}

	// Find the created server
	var server domain.Server
	if err := dashSvc.db.Where("name = ?", srvName).First(&server).Error; err != nil {
		t.Fatalf("failed finding created server: %v", err)
	}

	// 6. View Server Detail: GET /dashboard/servers/:id
	wDetail := httptest.NewRecorder()
	reqDetail, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d", server.ID), nil)
	reqDetail.AddCookie(authCookie)
	r.ServeHTTP(wDetail, reqDetail)
	if wDetail.Code != http.StatusOK || !strings.Contains(wDetail.Body.String(), srvName) {
		t.Fatalf("server detail failed: %d", wDetail.Code)
	}

	// Ensure the assigned host points to fakeFS even if parallel hosting tests created another host.
	dashSvc.db.Model(&domain.Host{}).Where("id = ?", server.HostID).Update("file_server_base_url", fakeFSURL)

	// 7. Manage Files: GET /dashboard/servers/:id/files/open redirects to node fileserver
	wOpenFS := httptest.NewRecorder()
	reqOpenFS, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/files/open", server.ID), nil)
	reqOpenFS.AddCookie(authCookie)
	r.ServeHTTP(wOpenFS, reqOpenFS)

	if wOpenFS.Code != http.StatusFound {
		t.Fatalf("open file manager failed: %d", wOpenFS.Code)
	}
	loc := wOpenFS.Header().Get("Location")
	if !strings.Contains(loc, "/login?") || !strings.Contains(loc, "token=dash-login-tok") || !strings.Contains(loc, "target=") {
		t.Fatalf("unexpected file manager redirect URL: %s", loc)
	}

	// 9. Trigger Manual Backup: POST /dashboard/servers/:id/backups/now
	wBackup := httptest.NewRecorder()
	reqBackup, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/backups/now", server.ID), nil)
	reqBackup.AddCookie(authCookie)
	r.ServeHTTP(wBackup, reqBackup)
	if wBackup.Code != http.StatusFound {
		t.Fatalf("manual backup trigger failed: %d", wBackup.Code)
	}

	// 10. Trigger Migration: POST /dashboard/servers/:id/migrate
	formMig := url.Values{}
	formMig.Set("destination_host_id", "0") // let scheduler choose
	wMig := httptest.NewRecorder()
	reqMig, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/migrate", server.ID), strings.NewReader(formMig.Encode()))
	reqMig.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqMig.AddCookie(authCookie)
	r.ServeHTTP(wMig, reqMig)
	if wMig.Code != http.StatusFound {
		t.Fatalf("migration trigger failed: %d", wMig.Code)
	}

	// 11. View /dashboard server list
	wList := httptest.NewRecorder()
	reqList, _ := http.NewRequest(http.MethodGet, "/dashboard", nil)
	reqList.AddCookie(authCookie)
	r.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK || !strings.Contains(wList.Body.String(), "DashboardSurvival") {
		t.Fatalf("servers list page failed: %d", wList.Code)
	}
}
