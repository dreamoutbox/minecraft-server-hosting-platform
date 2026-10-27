package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/auth"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/storage"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// mockWorkerForBackup tracks volume exports and imports in memory for backup verification.
type mockWorkerForBackup struct {
	mu           sync.Mutex
	volumeData   map[string][]byte
	stoppedCtrs  map[string]bool
	createdCtrs  map[string]worker.CreateContainerRequest
	exportedCall int
	importedCall int
}

func newMockWorkerForBackup() *mockWorkerForBackup {
	return &mockWorkerForBackup{
		volumeData:  make(map[string][]byte),
		stoppedCtrs: make(map[string]bool),
		createdCtrs: make(map[string]worker.CreateContainerRequest),
	}
}

func (m *mockWorkerForBackup) GetStatus(ctx context.Context) (*worker.HostStatusResponse, error) {
	return &worker.HostStatusResponse{Hostname: "backup-node", MaxServers: 10}, nil
}

func (m *mockWorkerForBackup) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*worker.CreateVolumeResponse, error) {
	return &worker.CreateVolumeResponse{MountPath: "/data"}, nil
}

func (m *mockWorkerForBackup) DeleteVolume(ctx context.Context, volumeID string) error {
	return nil
}

func (m *mockWorkerForBackup) ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exportedCall++
	data := m.volumeData[volumeID]
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockWorkerForBackup) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.importedCall++
	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.volumeData[volumeID] = buf
	return nil
}

func (m *mockWorkerForBackup) CreateContainer(ctx context.Context, req worker.CreateContainerRequest) (*worker.CreateContainerResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("ctr-%d", req.ServerID)
	m.createdCtrs[id] = req
	return &worker.CreateContainerResponse{ContainerID: id, Port: req.Port, MountPath: req.MountPath}, nil
}

func (m *mockWorkerForBackup) StopContainer(ctx context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stoppedCtrs[containerID] = true
	return nil
}

func (m *mockWorkerForBackup) DeleteContainer(ctx context.Context, containerID string) error {
	return nil
}

func (m *mockWorkerForBackup) GetContainerStatus(ctx context.Context, containerID string) (*worker.ContainerStatusResponse, error) {
	return &worker.ContainerStatusResponse{ContainerID: containerID, Status: worker.ContainerStatusRunning}, nil
}

func setupBackupTestEnv(t *testing.T) (*Service, *mockWorkerForBackup, storage.StorageProvider, *domain.Server, *domain.User, string) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil, nil, nil, nil, nil, ""
	}
	_ = domain.AutoMigrate(db)
	_ = domain.SeedPlans(db)

	ctx := context.Background()
	var sp storage.StorageProvider
	storageProvider, err := storage.NewS3StorageProvider(ctx, storage.S3Config{
		Endpoint:     "http://localhost:8333",
		Region:       "us-east-1",
		Bucket:       fmt.Sprintf("backup-test-%d", time.Now().UnixNano()),
		AccessKey:    "dev",
		SecretKey:    "dev",
		UsePathStyle: true,
	})
	if err == nil {
		sp = storageProvider
	} else {
		sp = storage.NewMemoryStorageProvider()
	}

	mockWorker := newMockWorkerForBackup()
	service := NewService(db, sp, func(baseURL, apiKey string) worker.Client {
		return mockWorker
	})

	user := domain.User{
		Email:        fmt.Sprintf("backup-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hashed",
		Role:         domain.UserRoleUser,
	}
	_ = db.Create(&user)

	host := domain.Host{
		Hostname:      "backup-host",
		WorkerBaseURL: "http://127.0.0.1:9090",
		APIKey:        "test-key",
		APIKeyHash:    "hash",
		MaxServers:    10,
		Status:        domain.HostStatusActive,
	}
	_ = db.Create(&host)

	var plan domain.Plan
	_ = db.First(&plan)

	server := domain.Server{
		UserID:      user.ID,
		PlanID:      plan.ID,
		HostID:      host.ID,
		Name:        "BackupTargetServer",
		Port:        25588,
		Status:      domain.ServerStatusRunning,
		ContainerID: "ctr-target",
		MountPath:   "/var/lib/mounts/target",
	}
	// Clear any conflict on port 25588
	db.Where("port = ?", 25588).Delete(&domain.Server{})
	_ = db.Create(&server)

	jwtSecret := "backup-secret-key"
	tokens, _ := auth.GenerateTokenPair(user.ID, user.Email, user.Role, jwtSecret)

	return service, mockWorker, sp, &server, &user, tokens.AccessToken
}

func TestBackupAndRestoreEndToEnd(t *testing.T) {
	service, mockWorker, sp, server, _, _ := setupBackupTestEnv(t)
	if service == nil {
		return
	}

	ctx := context.Background()
	volID := fmt.Sprintf("%d", server.ID)
	initialWorldData := []byte("tar-archive-containing-saved-world-chunks")
	mockWorker.volumeData[volID] = initialWorldData

	// 1. Execute manual backup
	record, err := service.BackupServer(ctx, server.ID)
	if err != nil {
		t.Fatalf("BackupServer failed: %v", err)
	}

	if record.ID == 0 {
		t.Fatal("expected saved BackupRecord ID")
	}
	if record.SizeBytes != int64(len(initialWorldData)) {
		t.Errorf("expected size %d, got %d", len(initialWorldData), record.SizeBytes)
	}

	// Verify object exists in storage
	storedBytes, err := sp.Get(ctx, record.StorageKey)
	if err != nil {
		t.Fatalf("failed retrieving object from storage: %v", err)
	}
	if !bytes.Equal(storedBytes, initialWorldData) {
		t.Error("stored object content mismatch")
	}

	// 2. Mutate volume state on worker (simulating corruption)
	mockWorker.volumeData[volID] = []byte("corrupted-data")

	// 3. Restore server from backup record
	if err := service.RestoreServer(ctx, server.ID, record.ID); err != nil {
		t.Fatalf("RestoreServer failed: %v", err)
	}

	// Verify worker volume data restored to pristine state
	if !bytes.Equal(mockWorker.volumeData[volID], initialWorldData) {
		t.Errorf("restored volume data mismatch: got %s, want %s", string(mockWorker.volumeData[volID]), string(initialWorldData))
	}

	// Verify container stopped and restarted
	if !mockWorker.stoppedCtrs[server.ContainerID] {
		t.Error("expected container to be stopped during restore")
	}
	if len(mockWorker.createdCtrs) == 0 {
		t.Error("expected container to be recreated/restarted")
	}

	// 4. List backups
	records, err := service.ListBackups(ctx, server.ID)
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least 1 backup record")
	}
}

func TestBackupHTTPRoutes(t *testing.T) {
	service, mockWorker, _, server, _, token := setupBackupTestEnv(t)
	if service == nil {
		return
	}

	volID := fmt.Sprintf("%d", server.ID)
	mockWorker.volumeData[volID] = []byte("http-world-tar-data")

	jwtSecret := "backup-secret-key"
	handler := NewHandler(service)
	r := gin.New()
	handler.RegisterRoutes(r, jwtSecret)

	// 1. POST /api/v1/servers/:id/backups (create backup)
	wBackup := httptest.NewRecorder()
	reqBackup, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/servers/%d/backups", server.ID), nil)
	reqBackup.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wBackup, reqBackup)

	if wBackup.Code != http.StatusCreated {
		t.Fatalf("create backup failed: %d: %s", wBackup.Code, wBackup.Body.String())
	}

	var rec domain.BackupRecord
	_ = json.Unmarshal(wBackup.Body.Bytes(), &rec)
	if rec.ID == 0 {
		t.Fatal("expected valid backup record id")
	}

	// 2. GET /api/v1/servers/:id/backups (list backups)
	wList := httptest.NewRecorder()
	reqList, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/servers/%d/backups", server.ID), nil)
	reqList.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list backups failed: %d", wList.Code)
	}

	// 3. POST /api/v1/servers/:id/backups/:backup_id/restore (restore backup)
	wRestore := httptest.NewRecorder()
	reqRestore, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/servers/%d/backups/%d/restore", server.ID, rec.ID), nil)
	reqRestore.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wRestore, reqRestore)

	if wRestore.Code != http.StatusOK {
		t.Fatalf("restore backup failed: %d: %s", wRestore.Code, wRestore.Body.String())
	}
}

func TestCronScheduling(t *testing.T) {
	service, _, _, _, _, _ := setupBackupTestEnv(t)
	if service == nil {
		return
	}
	defer service.StopCron()

	if err := service.ScheduleBackups("@every 1s"); err != nil {
		t.Fatalf("ScheduleBackups failed: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
}
