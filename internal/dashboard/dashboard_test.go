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

	"github.com/dreamoutbox/authkit"
	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/jobs"
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
	jobSvc := jobs.NewService(db, hostingSvc, backupSvc)
	jobSvc.SetTimings(20*time.Millisecond, 0)

	// Clear stale pending jobs from other test packages before running dashboard queue assertions
	db.Model(&domain.ServerJob{}).Where("status = ?", domain.JobStatusPending).Update("status", domain.JobStatusFailed)

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

	dashService := NewService(db, hostingSvc, backupSvc, billingSvc, migrationSvc, jobSvc, jwtSecret)

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

	// 5. Create Server: POST /dashboard/servers/new returns immediately with queued pending server
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

	// Verify server was queued with pending status before job runs
	var server domain.Server
	if err := dashSvc.db.Where("name = ?", srvName).First(&server).Error; err != nil {
		t.Fatalf("failed finding queued server: %v", err)
	}
	if server.Status != domain.ServerStatusPending {
		t.Fatalf("expected server status pending immediately after enqueue, got %s", server.Status)
	}

	// Process queued create_server job
	if processed, procErr := dashSvc.jobSvc.ProcessNext(context.Background()); !processed || procErr != nil {
		t.Fatalf("failed processing queued create_server job: processed=%v err=%v", processed, procErr)
	}

	// 6. View Server Detail (Overview): sub-navbar present, moved panels absent, host tab hidden for user role
	wDetail := httptest.NewRecorder()
	reqDetail, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d", server.ID), nil)
	reqDetail.AddCookie(authCookie)
	r.ServeHTTP(wDetail, reqDetail)
	if wDetail.Code != http.StatusOK || !strings.Contains(wDetail.Body.String(), srvName) {
		t.Fatalf("server detail failed: %d", wDetail.Code)
	}
	detailHTML := wDetail.Body.String()
	for _, unexpected := range []string{"World Backups", "Host Worker Status", "Host Migration", "Danger Zone", "/host\">Host"} {
		if strings.Contains(detailHTML, unexpected) {
			t.Errorf("overview page must not contain %q after refactor", unexpected)
		}
	}
	if !strings.Contains(detailHTML, "/backups\"") || !strings.Contains(detailHTML, "/settings\"") {
		t.Errorf("expected sub-navbar links to Backups and Settings on overview page")
	}

	// 6b. Dedicated Backups Page
	wBackupsPage := httptest.NewRecorder()
	reqBackupsPage, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/backups", server.ID), nil)
	reqBackupsPage.AddCookie(authCookie)
	r.ServeHTTP(wBackupsPage, reqBackupsPage)
	if wBackupsPage.Code != http.StatusOK || !strings.Contains(wBackupsPage.Body.String(), "World Backups") {
		t.Fatalf("backups page failed: %d", wBackupsPage.Code)
	}

	// 6c. Dedicated Settings Page + Danger Zone + Server Jar/Version Update
	wSettingsPage := httptest.NewRecorder()
	reqSettingsPage, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/settings", server.ID), nil)
	reqSettingsPage.AddCookie(authCookie)
	r.ServeHTTP(wSettingsPage, reqSettingsPage)
	if wSettingsPage.Code != http.StatusOK {
		t.Fatalf("settings page failed: %d", wSettingsPage.Code)
	}
	settingsHTML := wSettingsPage.Body.String()
	if !strings.Contains(settingsHTML, "Danger Zone") || !strings.Contains(settingsHTML, "Server Jar Type") {
		t.Errorf("expected Danger Zone and Server Jar Type selector on settings page")
	}

	settingsForm := url.Values{}
	settingsForm.Set("server_type", "PAPER")
	settingsForm.Set("server_version", "1.21.4")
	wSaveSettings := httptest.NewRecorder()
	reqSaveSettings, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/settings", server.ID), strings.NewReader(settingsForm.Encode()))
	reqSaveSettings.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqSaveSettings.AddCookie(authCookie)
	r.ServeHTTP(wSaveSettings, reqSaveSettings)
	if wSaveSettings.Code != http.StatusFound {
		t.Fatalf("update server settings failed: %d", wSaveSettings.Code)
	}
	_ = dashSvc.db.First(&server, server.ID)
	if server.ServerType != domain.ServerTypePaper || server.ServerVersion != "1.21.4" {
		t.Fatalf("expected updated server software PAPER 1.21.4, got %s %s", server.ServerType, server.ServerVersion)
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

	// 8. Trigger Manual Backup (queued) and process it
	wBackup := httptest.NewRecorder()
	reqBackup, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/backups/now", server.ID), nil)
	reqBackup.AddCookie(authCookie)
	r.ServeHTTP(wBackup, reqBackup)
	if wBackup.Code != http.StatusFound {
		t.Fatalf("manual backup trigger failed: %d", wBackup.Code)
	}
	if processed, procErr := dashSvc.jobSvc.ProcessNext(context.Background()); !processed || procErr != nil {
		t.Fatalf("failed processing queued backup job: processed=%v err=%v", processed, procErr)
	}

	// 9. Role enforcement for Host & Migration page: regular user gets 403, staff user succeeds
	wHostForbidden := httptest.NewRecorder()
	reqHostForbidden, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/host", server.ID), nil)
	reqHostForbidden.AddCookie(authCookie)
	r.ServeHTTP(wHostForbidden, reqHostForbidden)
	if wHostForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /host for regular user, got %d", wHostForbidden.Code)
	}

	formMig := url.Values{}
	formMig.Set("destination_host_id", "0")
	wMigForbidden := httptest.NewRecorder()
	reqMigForbidden, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/migrate", server.ID), strings.NewReader(formMig.Encode()))
	reqMigForbidden.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqMigForbidden.AddCookie(authCookie)
	r.ServeHTTP(wMigForbidden, reqMigForbidden)
	if wMigForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /migrate for regular user, got %d", wMigForbidden.Code)
	}

	// Issue staff session cookie
	staffTokens, _ := authkit.GenerateTokenPair(999999, "staff@example.com", domain.UserRoleStaff, dashSvc.jwtSecret)
	staffCookie := &http.Cookie{Name: "token", Value: staffTokens.AccessToken}

	wHostStaff := httptest.NewRecorder()
	reqHostStaff, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/host", server.ID), nil)
	reqHostStaff.AddCookie(staffCookie)
	r.ServeHTTP(wHostStaff, reqHostStaff)
	if wHostStaff.Code != http.StatusOK {
		t.Fatalf("expected 200 on /host for staff role, got %d", wHostStaff.Code)
	}
	hostHTML := wHostStaff.Body.String()
	if !strings.Contains(hostHTML, "Host Worker Status") || !strings.Contains(hostHTML, "Host Migration") || !strings.Contains(hostHTML, "/host\">Host") {
		t.Errorf("expected Host Worker Status, Host Migration, and sub-navbar Host tab for staff role")
	}

	// Ensure alternate host exists active right now in case parallel tests disabled hosts
	var activeHosts []domain.Host
	_ = dashSvc.db.Where("status = ?", domain.HostStatusActive).Find(&activeHosts)
	if len(activeHosts) < 2 {
		dashSvc.db.Model(&domain.Host{}).Where("hostname IN (?)", []string{"dash-node-1", "dash-node-2"}).Update("status", domain.HostStatusActive)
	}

	wMigStaff := httptest.NewRecorder()
	reqMigStaff, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/migrate", server.ID), strings.NewReader(formMig.Encode()))
	reqMigStaff.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqMigStaff.AddCookie(staffCookie)
	r.ServeHTTP(wMigStaff, reqMigStaff)
	if wMigStaff.Code != http.StatusFound {
		t.Fatalf("staff migration trigger failed: %d", wMigStaff.Code)
	}

	// 10. View /dashboard server list
	wList := httptest.NewRecorder()
	reqList, _ := http.NewRequest(http.MethodGet, "/dashboard", nil)
	reqList.AddCookie(authCookie)
	r.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK || !strings.Contains(wList.Body.String(), "DashboardSurvival") {
		t.Fatalf("servers list page failed: %d", wList.Code)
	}

	// 11. Delete Server (queued) and verify async execution removes server and records delete log
	wDel := httptest.NewRecorder()
	reqDel, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/delete", server.ID), nil)
	reqDel.AddCookie(authCookie)
	r.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusFound {
		t.Fatalf("queued delete post failed: %d", wDel.Code)
	}
	if processed, procErr := dashSvc.jobSvc.ProcessNext(context.Background()); !processed || procErr != nil {
		t.Fatalf("failed processing queued delete job: processed=%v err=%v", processed, procErr)
	}
	if err := dashSvc.db.First(&server, server.ID).Error; err == nil {
		t.Fatalf("expected server %d deleted after queued job execution", server.ID)
	}
	logs, _ := domain.ListServerLogs(context.Background(), dashSvc.db, server.ID, 20)
	if len(logs) == 0 {
		t.Errorf("expected server operation logs persisted for server %d", server.ID)
	}
}
