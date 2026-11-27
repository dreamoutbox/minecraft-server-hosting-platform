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
	"github.com/dreamoutbox/authkit/session"
	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/jobs"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/storage"
	worker "github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
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

func (d *dashboardTestWorker) UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (*worker.UploadVolumeBackupResponse, error) {
	d.mu.Lock()
	data := append([]byte(nil), d.volumes[volumeID]...)
	d.mu.Unlock()

	sizeBytes, err := worker.PutPresignedBytes(ctx, presignedURL, data, "application/gzip")
	if err != nil {
		return nil, err
	}
	return &worker.UploadVolumeBackupResponse{SizeBytes: sizeBytes}, nil
}

func (d *dashboardTestWorker) RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error {
	data, err := worker.GetPresignedBytes(ctx, presignedURL)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.volumes[volumeID] = data
	d.mu.Unlock()
	return nil
}

func (d *dashboardTestWorker) UploadImageBackup(ctx context.Context, volumeID string, req worker.UploadImageBackupRequest) (*worker.UploadImageBackupResponse, error) {
	return &worker.UploadImageBackupResponse{}, nil
}

func (d *dashboardTestWorker) RestoreImageBackup(ctx context.Context, volumeID string, presignedURLs []string) (*worker.RestoreImageBackupResponse, error) {
	return &worker.RestoreImageBackupResponse{}, nil
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

func (d *dashboardTestWorker) GetContainerLogs(ctx context.Context, containerID string, tail int) (*worker.ContainerLogsResponse, error) {
	return &worker.ContainerLogsResponse{
		ContainerID: containerID,
		Logs:        "[12:00:00] [Server thread/INFO]: Done (1.5s)! For help, type \"help\"\n",
	}, nil
}

func (d *dashboardTestWorker) ExecContainerCommand(ctx context.Context, containerID string, command string) (*worker.ExecCommandResponse, error) {
	return &worker.ExecCommandResponse{
		ContainerID: containerID,
		Command:     command,
		Output:      "There are 0 of a max of 20 players online:",
	}, nil
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
	billingSvc := billing.NewService(db, paymentProvider, "whsec_test")

	fakeFS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/users":
			_ = json.NewEncoder(w).Encode(fsclient.RegisterUserResponse{ID: 1, Email: "dash@example.com", Created: true})
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
	h1 := domain.Host{Hostname: "dash-node-1", PublicDomain: "play1.dash.example.com", PublicIP: "203.0.113.11", WorkerBaseURL: "http://dash-node-1:9090", FileServerBaseURL: fakeFS.URL, APIKey: "k1", APIKeyHash: "h1", MaxServers: 10, Status: domain.HostStatusActive}
	h2 := domain.Host{Hostname: "dash-node-2", PublicDomain: "play2.dash.example.com", PublicIP: "203.0.113.12", WorkerBaseURL: "http://dash-node-2:9090", FileServerBaseURL: fakeFS.URL, APIKey: "k2", APIKeyHash: "h2", MaxServers: 10, Status: domain.HostStatusActive}
	_ = db.Where("hostname = ?", h1.Hostname).FirstOrCreate(&h1)
	_ = db.Where("hostname = ?", h2.Hostname).FirstOrCreate(&h2)
	db.Model(&domain.Host{}).Where("hostname = ?", h1.Hostname).Updates(map[string]interface{}{
		"status":               domain.HostStatusActive,
		"file_server_base_url": fakeFS.URL,
		"public_domain":        h1.PublicDomain,
		"public_ip":            h1.PublicIP,
	})
	db.Model(&domain.Host{}).Where("hostname = ?", h2.Hostname).Updates(map[string]interface{}{
		"status":               domain.HostStatusActive,
		"file_server_base_url": fakeFS.URL,
		"public_domain":        h2.PublicDomain,
		"public_ip":            h2.PublicIP,
	})

	cookieCfg := session.DefaultCookieConfig(session.DefaultCookieName, time.Hour, false)
	authsessions := session.NewManager(db, cookieCfg)
	dashService := NewService(db, hostingSvc, backupSvc, billingSvc, migrationSvc, jobSvc, authsessions)

	r := gin.New()
	dashService.RegisterRoutes(r)

	return r, dashService, fakeFS.URL
}

func extractSessionCookie(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == session.DefaultCookieName {
			return c.Value
		}
	}
	return ""
}

func mustCreateSessionCookie(t *testing.T, dashSvc *Service, userID uint) *http.Cookie {
	t.Helper()
	rawToken, _, err := dashSvc.authsessions.Store().Create(context.Background(), userID, time.Hour)
	if err != nil {
		t.Fatalf("failed creating test session for user %d: %v", userID, err)
	}
	t.Cleanup(func() {
		_ = dashSvc.authsessions.Store().Delete(context.Background(), rawToken)
	})
	return &http.Cookie{Name: dashSvc.authsessions.CookieConfig().Name, Value: rawToken}
}

func mustCreateUserWithSession(t *testing.T, dashSvc *Service, email string, role domain.UserRole) (*domain.User, *http.Cookie) {
	t.Helper()
	user := domain.User{
		User: authkit.User{
			Email:        email,
			PasswordHash: "hash",
			Role:         role,
		},
	}
	if err := dashSvc.db.Create(&user).Error; err != nil {
		t.Fatalf("failed creating test user %s: %v", email, err)
	}
	t.Cleanup(func() {
		_ = dashSvc.authsessions.Store().DeleteByUserID(context.Background(), user.ID)
		dashSvc.db.Delete(&domain.User{}, user.ID)
	})
	return &user, mustCreateSessionCookie(t, dashSvc, user.ID)
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

	// 2. Register: POST /register redirects to /login without issuing a session cookie
	formReg := url.Values{}
	formReg.Set("email", uniqueEmail)
	formReg.Set("password", password)
	wReg := httptest.NewRecorder()
	reqReg, _ := http.NewRequest(http.MethodPost, "/register", strings.NewReader(formReg.Encode()))
	reqReg.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(wReg, reqReg)

	if wReg.Code != http.StatusFound || !strings.HasPrefix(wReg.Header().Get("Location"), "/login") {
		t.Fatalf("register expected redirect to /login, got %d (%s)", wReg.Code, wReg.Header().Get("Location"))
	}
	if extractSessionCookie(wReg.Result()) != "" {
		t.Fatal("expected no session cookie on registration")
	}

	// 2b. Login: POST /login establishes session cookie
	wLoginPost := httptest.NewRecorder()
	reqLoginPost, _ := http.NewRequest(http.MethodPost, "/login", strings.NewReader(formReg.Encode()))
	reqLoginPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(wLoginPost, reqLoginPost)
	if wLoginPost.Code != http.StatusFound || wLoginPost.Header().Get("Location") != "/dashboard" {
		t.Fatalf("login failed: %d (%s)", wLoginPost.Code, wLoginPost.Header().Get("Location"))
	}
	cookieToken := extractSessionCookie(wLoginPost.Result())
	if cookieToken == "" {
		t.Fatal("expected session token cookie on login")
	}

	authCookie := &http.Cookie{Name: session.DefaultCookieName, Value: cookieToken}

	// 3. Billing page displays Order History and Payment History without Saved Payment Method
	wBilling := httptest.NewRecorder()
	reqBilling, _ := http.NewRequest(http.MethodGet, "/dashboard/billing", nil)
	reqBilling.AddCookie(authCookie)
	r.ServeHTTP(wBilling, reqBilling)

	if wBilling.Code != http.StatusOK {
		t.Fatalf("billing page failed: %d", wBilling.Code)
	}
	billingHTML := wBilling.Body.String()
	for _, expected := range []string{"Billing & Orders", "Order History", "Payment History"} {
		if !strings.Contains(billingHTML, expected) {
			t.Errorf("billing page missing %q", expected)
		}
	}
	if strings.Contains(billingHTML, "Saved Payment Method") {
		t.Errorf("billing page should no longer display Saved Payment Method panel")
	}

	// 4. Regular user cannot access /dashboard/servers/new (staff/admin only), but can access /dashboard/servers/buy
	wNewForbidden := httptest.NewRecorder()
	reqNewForbidden, _ := http.NewRequest(http.MethodGet, "/dashboard/servers/new", nil)
	reqNewForbidden.AddCookie(authCookie)
	r.ServeHTTP(wNewForbidden, reqNewForbidden)
	if wNewForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /dashboard/servers/new for regular user, got %d", wNewForbidden.Code)
	}

	wBuyPage := httptest.NewRecorder()
	reqBuyPage, _ := http.NewRequest(http.MethodGet, "/dashboard/servers/buy", nil)
	reqBuyPage.AddCookie(authCookie)
	r.ServeHTTP(wBuyPage, reqBuyPage)
	if wBuyPage.Code != http.StatusOK || !strings.Contains(wBuyPage.Body.String(), "Purchase Minecraft Server") {
		t.Fatalf("buy server page failed: %d", wBuyPage.Code)
	}

	var plan domain.Plan
	_ = dashSvc.db.First(&plan)

	buySrvName := fmt.Sprintf("BoughtWorld-%d", time.Now().UnixNano())
	buyForm := url.Values{}
	buyForm.Set("name", buySrvName)
	buyForm.Set("plan_id", fmt.Sprintf("%d", plan.ID))
	buyForm.Set("server_type", "VANILLA")
	buyForm.Set("server_version", "LATEST")
	wBuyPost := httptest.NewRecorder()
	reqBuyPost, _ := http.NewRequest(http.MethodPost, "/dashboard/servers/buy", strings.NewReader(buyForm.Encode()))
	reqBuyPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqBuyPost.AddCookie(authCookie)
	r.ServeHTTP(wBuyPost, reqBuyPost)
	if wBuyPost.Code != http.StatusSeeOther || wBuyPost.Header().Get("Location") == "" {
		t.Fatalf("buy server post failed: code=%d location=%s", wBuyPost.Code, wBuyPost.Header().Get("Location"))
	}

	// Verify created checkout order renders on /dashboard/billing
	wBillingOrder := httptest.NewRecorder()
	reqBillingOrder, _ := http.NewRequest(http.MethodGet, "/dashboard/billing", nil)
	reqBillingOrder.AddCookie(authCookie)
	r.ServeHTTP(wBillingOrder, reqBillingOrder)
	if wBillingOrder.Code != http.StatusOK || !strings.Contains(wBillingOrder.Body.String(), buySrvName) {
		t.Fatalf("expected created order %q on billing page, code=%d", buySrvName, wBillingOrder.Code)
	}

	// 5. Staff/Admin can access /dashboard/servers/new and queue direct server creation
	_, staffCreateCookie := mustCreateUserWithSession(t, dashSvc, fmt.Sprintf("staff-create-%d@example.com", time.Now().UnixNano()), domain.UserRoleStaff)

	wNewSrv := httptest.NewRecorder()
	reqNewSrv, _ := http.NewRequest(http.MethodGet, "/dashboard/servers/new", nil)
	reqNewSrv.AddCookie(staffCreateCookie)
	r.ServeHTTP(wNewSrv, reqNewSrv)
	if wNewSrv.Code != http.StatusOK || !strings.Contains(wNewSrv.Body.String(), "Create Minecraft Server") {
		t.Fatalf("create server page failed for staff: %d", wNewSrv.Code)
	}

	// Create Server for test user via EnqueueCreateServer (simulating webhook fulfillment)
	srvName := fmt.Sprintf("DashboardSurvival-%d", time.Now().UnixNano())
	var userRecord domain.User
	_ = dashSvc.db.Where("email = ?", uniqueEmail).First(&userRecord)
	exp := time.Now().Add(30 * 24 * time.Hour)
	createdSrv, _, createErr := dashSvc.jobSvc.EnqueueCreateServerWithExpiry(
		context.Background(),
		userRecord.ID,
		srvName,
		plan.ID,
		domain.ServerTypeVanilla,
		domain.DefaultServerVersion,
		&exp,
	)
	if createErr != nil || createdSrv == nil {
		t.Fatalf("failed queueing test server: %v", createErr)
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
	for _, unexpected := range []string{"World Backups", "Host Worker Status", "Host Migration", "Danger Zone", "Current Host", "Background Job Queue", "Operation Logs", "/host\">Host", "/jobs\">Job Queue"} {
		if strings.Contains(detailHTML, unexpected) {
			t.Errorf("overview page must not contain %q after refactor", unexpected)
		}
	}
	if !strings.Contains(detailHTML, "/backups\"") || !strings.Contains(detailHTML, "/settings\"") || !strings.Contains(detailHTML, "/logs\"") {
		t.Errorf("expected sub-navbar links to Backups, Logs, and Settings on overview page")
	}
	if !strings.Contains(detailHTML, "Connect IP Address") {
		t.Errorf("expected Connect IP Address card on overview page")
	}
	var loadedServer domain.Server
	if err := dashSvc.db.Preload("Host").First(&loadedServer, server.ID).Error; err != nil {
		t.Fatalf("failed reloading server with host: %v", err)
	}
	if !strings.Contains(detailHTML, loadedServer.ConnectAddress()) {
		t.Errorf("expected connect address %q on overview page", loadedServer.ConnectAddress())
	}
	if !strings.Contains(detailHTML, "copyConnectAddress(this,") {
		t.Errorf("expected copy address button on overview page")
	}

	// 6b. Dedicated Backups & Logs Pages
	wBackupsPage := httptest.NewRecorder()
	reqBackupsPage, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/backups", server.ID), nil)
	reqBackupsPage.AddCookie(authCookie)
	r.ServeHTTP(wBackupsPage, reqBackupsPage)
	if wBackupsPage.Code != http.StatusOK || !strings.Contains(wBackupsPage.Body.String(), `<h5 class="fw-bold mb-0">Backups</h5>`) {
		t.Fatalf("backups page failed: %d", wBackupsPage.Code)
	}

	wLogsPage := httptest.NewRecorder()
	reqLogsPage, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/logs", server.ID), nil)
	reqLogsPage.AddCookie(authCookie)
	r.ServeHTTP(wLogsPage, reqLogsPage)
	if wLogsPage.Code != http.StatusOK || !strings.Contains(wLogsPage.Body.String(), "Operation Logs") {
		t.Fatalf("logs page failed: %d", wLogsPage.Code)
	}

	// 6b2. Dedicated Console Page + Send Command
	wConsolePage := httptest.NewRecorder()
	reqConsolePage, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/console", server.ID), nil)
	reqConsolePage.AddCookie(authCookie)
	r.ServeHTTP(wConsolePage, reqConsolePage)
	if wConsolePage.Code != http.StatusOK || !strings.Contains(wConsolePage.Body.String(), "Server Console") || !strings.Contains(wConsolePage.Body.String(), "Done (1.5s)") {
		t.Fatalf("console page failed: code=%d body=%s", wConsolePage.Code, wConsolePage.Body.String())
	}

	cmdForm := url.Values{}
	cmdForm.Set("command", "list")
	cmdForm.Set("tail", "200")
	wSendCmd := httptest.NewRecorder()
	reqSendCmd, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/servers/%d/console/command", server.ID), strings.NewReader(cmdForm.Encode()))
	reqSendCmd.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqSendCmd.Header.Set("Accept", "application/json")
	reqSendCmd.AddCookie(authCookie)
	r.ServeHTTP(wSendCmd, reqSendCmd)
	if wSendCmd.Code != http.StatusOK || !strings.Contains(wSendCmd.Body.String(), `"ok":true`) {
		t.Fatalf("send console command failed: code=%d body=%s", wSendCmd.Code, wSendCmd.Body.String())
	}

	wConsoleLogsJSON := httptest.NewRecorder()
	reqConsoleLogsJSON, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/console/logs?tail=200", server.ID), nil)
	reqConsoleLogsJSON.AddCookie(authCookie)
	r.ServeHTTP(wConsoleLogsJSON, reqConsoleLogsJSON)
	if wConsoleLogsJSON.Code != http.StatusOK || !strings.Contains(wConsoleLogsJSON.Body.String(), "Done (1.5s)") {
		t.Fatalf("console logs JSON polling failed: code=%d body=%s", wConsoleLogsJSON.Code, wConsoleLogsJSON.Body.String())
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
	staffCookie := staffCreateCookie

	wHostStaff := httptest.NewRecorder()
	reqHostStaff, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/host", server.ID), nil)
	reqHostStaff.AddCookie(staffCookie)
	r.ServeHTTP(wHostStaff, reqHostStaff)
	if wHostStaff.Code != http.StatusOK {
		t.Fatalf("expected 200 on /host for staff role, got %d", wHostStaff.Code)
	}
	hostHTML := wHostStaff.Body.String()
	if !strings.Contains(hostHTML, "Host Worker Status") || !strings.Contains(hostHTML, "Host Migration") || !strings.Contains(hostHTML, "Current Host:") || !strings.Contains(hostHTML, "/host\">Host") {
		t.Errorf("expected Host Worker Status, Host Migration, Current Host, and sub-navbar Host tab for staff role")
	}

	// Role enforcement for Jobs page: regular user gets 403, staff user succeeds
	wJobsForbidden := httptest.NewRecorder()
	reqJobsForbidden, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/jobs", server.ID), nil)
	reqJobsForbidden.AddCookie(authCookie)
	r.ServeHTTP(wJobsForbidden, reqJobsForbidden)
	if wJobsForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /jobs for regular user, got %d", wJobsForbidden.Code)
	}

	wJobsStaff := httptest.NewRecorder()
	reqJobsStaff, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/jobs", server.ID), nil)
	reqJobsStaff.AddCookie(staffCookie)
	r.ServeHTTP(wJobsStaff, reqJobsStaff)
	if wJobsStaff.Code != http.StatusOK || !strings.Contains(wJobsStaff.Body.String(), "Background Job Queue") {
		t.Fatalf("expected 200 with Background Job Queue on /jobs for staff role, got %d", wJobsStaff.Code)
	}

	// Role enforcement for Admin Users page: staff gets 403, admin updates user role
	wUsersForbidden := httptest.NewRecorder()
	reqUsersForbidden, _ := http.NewRequest(http.MethodGet, "/dashboard/users", nil)
	reqUsersForbidden.AddCookie(staffCookie)
	r.ServeHTTP(wUsersForbidden, reqUsersForbidden)
	if wUsersForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /dashboard/users for staff role, got %d", wUsersForbidden.Code)
	}

	_, adminCookie := mustCreateUserWithSession(t, dashSvc, fmt.Sprintf("admin-dash-%d@example.com", time.Now().UnixNano()), domain.UserRoleAdmin)

	wUsersAdmin := httptest.NewRecorder()
	reqUsersAdmin, _ := http.NewRequest(http.MethodGet, "/dashboard/users", nil)
	reqUsersAdmin.AddCookie(adminCookie)
	r.ServeHTTP(wUsersAdmin, reqUsersAdmin)
	if wUsersAdmin.Code != http.StatusOK || !strings.Contains(wUsersAdmin.Body.String(), "User Management") {
		t.Fatalf("expected 200 on /dashboard/users for admin role, got %d", wUsersAdmin.Code)
	}

	roleForm := url.Values{}
	roleForm.Set("role", string(domain.UserRoleStaff))
	wRoleUpdate := httptest.NewRecorder()
	reqRoleUpdate, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/users/%d/role", server.UserID), strings.NewReader(roleForm.Encode()))
	reqRoleUpdate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqRoleUpdate.AddCookie(adminCookie)
	r.ServeHTTP(wRoleUpdate, reqRoleUpdate)
	if wRoleUpdate.Code != http.StatusFound {
		t.Fatalf("expected 302 on role update, got %d", wRoleUpdate.Code)
	}
	var updatedUser domain.User
	_ = dashSvc.db.First(&updatedUser, server.UserID)
	if updatedUser.Role != domain.UserRoleStaff {
		t.Errorf("expected user role updated to staff, got %s", updatedUser.Role)
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

func TestExtendServerPanelInSettings(t *testing.T) {
	r, dashSvc, _ := setupDashboardTestEnv(t)
	if r == nil {
		return
	}

	suffix := time.Now().UnixNano()
	owner := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("extend-panel-%d@example.com", suffix),
			PasswordHash: "hash",
			Role:         domain.UserRoleUser,
		},
	}
	if err := dashSvc.db.Create(&owner).Error; err != nil {
		t.Fatalf("failed creating user: %v", err)
	}
	var plan domain.Plan
	if err := dashSvc.db.Order("id asc").First(&plan).Error; err != nil {
		t.Fatalf("expected seeded plan: %v", err)
	}
	var host domain.Host
	if err := dashSvc.db.First(&host).Error; err != nil {
		t.Fatalf("expected seeded host: %v", err)
	}

	expiredAt := time.Now().Add(-24 * time.Hour)
	srv := domain.Server{
		UserID:        owner.ID,
		PlanID:        plan.ID,
		HostID:        host.ID,
		Name:          fmt.Sprintf("ExtendPanelSrv-%d", suffix),
		Status:        domain.ServerStatusStopped,
		Port:          25756,
		ServerType:    domain.ServerTypeVanilla,
		ServerVersion: "LATEST",
		ExpiresAt:     &expiredAt,
	}
	if err := dashSvc.db.Create(&srv).Error; err != nil {
		t.Fatalf("failed creating server: %v", err)
	}
	t.Cleanup(func() {
		dashSvc.db.Delete(&domain.Server{}, srv.ID)
		dashSvc.db.Delete(&domain.User{}, owner.ID)
	})

	cookie := mustCreateSessionCookie(t, dashSvc, owner.ID)

	// Overview page must not have the inline Extend 30 Days form, and must link to settings#extend-server.
	wDetail := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/servers/%d", srv.ID), cookie, nil)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("expected 200 on server detail, got %d", wDetail.Code)
	}
	detailBody := wDetail.Body.String()
	extendAction := fmt.Sprintf(`action="/dashboard/servers/%d/extend"`, srv.ID)
	if strings.Contains(detailBody, extendAction) {
		t.Errorf("expected Extend 30 Days form removed from server overview page")
	}
	expectedAnchorLink := fmt.Sprintf(`/dashboard/servers/%d/settings#extend-server`, srv.ID)
	if !strings.Contains(detailBody, expectedAnchorLink) {
		t.Errorf("expected expired overview banner to link to %s", expectedAnchorLink)
	}

	// Settings page must render the #extend-server panel with the Extend 30 Days form.
	wSettings := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/servers/%d/settings", srv.ID), cookie, nil)
	if wSettings.Code != http.StatusOK {
		t.Fatalf("expected 200 on server settings, got %d", wSettings.Code)
	}
	settingsBody := wSettings.Body.String()
	if !strings.Contains(settingsBody, `id="extend-server"`) {
		t.Errorf("expected settings page to contain id=\"extend-server\" panel")
	}
	if !strings.Contains(settingsBody, extendAction) || !strings.Contains(settingsBody, "Extend 30 Days") {
		t.Errorf("expected settings page to render Extend 30 Days form pointing to %s", extendAction)
	}
}

func TestDashboardExpiringServerAlerts(t *testing.T) {
	r, dashSvc, _ := setupDashboardTestEnv(t)
	if r == nil {
		return
	}

	suffix := time.Now().UnixNano()
	owner := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("expiring-alert-%d@example.com", suffix),
			PasswordHash: "hash",
			Role:         domain.UserRoleUser,
		},
	}
	if err := dashSvc.db.Create(&owner).Error; err != nil {
		t.Fatalf("failed creating user: %v", err)
	}
	var plan domain.Plan
	if err := dashSvc.db.Order("id asc").First(&plan).Error; err != nil {
		t.Fatalf("expected seeded plan: %v", err)
	}
	var host domain.Host
	if err := dashSvc.db.First(&host).Error; err != nil {
		t.Fatalf("expected seeded host: %v", err)
	}

	soonAt := time.Now().Add(48 * time.Hour)
	expiredAt := time.Now().Add(-24 * time.Hour)
	farAt := time.Now().Add(30 * 24 * time.Hour)

	srvSoon := domain.Server{
		UserID: owner.ID, PlanID: plan.ID, HostID: host.ID,
		Name: fmt.Sprintf("SoonSrv-%d", suffix), Status: domain.ServerStatusRunning,
		Port: 25757, ServerType: domain.ServerTypeVanilla, ServerVersion: "LATEST",
		ExpiresAt: &soonAt,
	}
	srvExpired := domain.Server{
		UserID: owner.ID, PlanID: plan.ID, HostID: host.ID,
		Name: fmt.Sprintf("ExpiredSrv-%d", suffix), Status: domain.ServerStatusStopped,
		Port: 25758, ServerType: domain.ServerTypeVanilla, ServerVersion: "LATEST",
		ExpiresAt: &expiredAt,
	}
	srvFar := domain.Server{
		UserID: owner.ID, PlanID: plan.ID, HostID: host.ID,
		Name: fmt.Sprintf("FarSrv-%d", suffix), Status: domain.ServerStatusRunning,
		Port: 25759, ServerType: domain.ServerTypeVanilla, ServerVersion: "LATEST",
		ExpiresAt: &farAt,
	}
	srvNoExpiry := domain.Server{
		UserID: owner.ID, PlanID: plan.ID, HostID: host.ID,
		Name: fmt.Sprintf("NoExpirySrv-%d", suffix), Status: domain.ServerStatusRunning,
		Port: 25760, ServerType: domain.ServerTypeVanilla, ServerVersion: "LATEST",
		ExpiresAt: nil,
	}
	for _, s := range []*domain.Server{&srvSoon, &srvExpired, &srvFar, &srvNoExpiry} {
		if err := dashSvc.db.Create(s).Error; err != nil {
			t.Fatalf("failed creating server %s: %v", s.Name, err)
		}
	}
	t.Cleanup(func() {
		dashSvc.db.Delete(&domain.Server{}, []uint{srvSoon.ID, srvExpired.ID, srvFar.ID, srvNoExpiry.ID})
		dashSvc.db.Delete(&domain.User{}, owner.ID)
	})

	cookie := mustCreateSessionCookie(t, dashSvc, owner.ID)

	// Main dashboard list shows alert banner with links for soon + expired servers, not far/no-expiry servers.
	wList := doDashboardReq(r, http.MethodGet, "/dashboard", cookie, nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 on /dashboard, got %d", wList.Code)
	}
	listBody := wList.Body.String()
	if !strings.Contains(listBody, "Server Expiration Notice") {
		t.Fatalf("expected Server Expiration Notice alert on /dashboard")
	}
	soonLink := fmt.Sprintf("/dashboard/servers/%d/settings#extend-server", srvSoon.ID)
	expiredLink := fmt.Sprintf("/dashboard/servers/%d/settings#extend-server", srvExpired.ID)
	farLink := fmt.Sprintf("/dashboard/servers/%d/settings#extend-server", srvFar.ID)
	noExpiryLink := fmt.Sprintf("/dashboard/servers/%d/settings#extend-server", srvNoExpiry.ID)
	if !strings.Contains(listBody, soonLink) {
		t.Errorf("expected expiring-soon server link %s in dashboard alert", soonLink)
	}
	if !strings.Contains(listBody, expiredLink) {
		t.Errorf("expected expired-within-retention server link %s in dashboard alert", expiredLink)
	}
	if strings.Contains(listBody, farLink) {
		t.Errorf("did not expect far-future server link %s in dashboard alert", farLink)
	}
	if strings.Contains(listBody, noExpiryLink) {
		t.Errorf("did not expect no-expiry server link %s in dashboard alert", noExpiryLink)
	}

	// Server detail page also shows warning banner with link for expiring-soon server.
	wDetail := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/servers/%d", srvSoon.ID), cookie, nil)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("expected 200 on expiring-soon server detail, got %d", wDetail.Code)
	}
	if !strings.Contains(wDetail.Body.String(), soonLink) {
		t.Errorf("expected expiring-soon warning link %s on server detail page", soonLink)
	}
}
