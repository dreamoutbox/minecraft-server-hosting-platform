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

	"github.com/dreamoutbox/authkit"
	"github.com/dreamoutbox/authkit/authkitjwt"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/storage"
)

// mockWorkerForBackup tracks volume exports and imports in memory for backup verification.
type mockWorkerForBackup struct {
	mu           sync.Mutex
	volumeData   map[string][]byte
	stoppedCtrs  map[string]bool
	createdCtrs  map[string]nodemanagerclient.CreateContainerRequest
	exportedCall int
	importedCall int
}

func newMockWorkerForBackup() *mockWorkerForBackup {
	return &mockWorkerForBackup{
		volumeData:  make(map[string][]byte),
		stoppedCtrs: make(map[string]bool),
		createdCtrs: make(map[string]nodemanagerclient.CreateContainerRequest),
	}
}

func (m *mockWorkerForBackup) GetStatus(ctx context.Context) (*nodemanagerclient.HostStatusResponse, error) {
	return &nodemanagerclient.HostStatusResponse{Hostname: "backup-node", MaxServers: 10}, nil
}

func (m *mockWorkerForBackup) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*nodemanagerclient.CreateVolumeResponse, error) {
	return &nodemanagerclient.CreateVolumeResponse{MountPath: "/data"}, nil
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

func (m *mockWorkerForBackup) UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (*nodemanagerclient.UploadVolumeBackupResponse, error) {
	m.mu.Lock()
	m.exportedCall++
	data := append([]byte(nil), m.volumeData[volumeID]...)
	m.mu.Unlock()

	sizeBytes, err := nodemanagerclient.PutPresignedBytes(ctx, presignedURL, data, backupContentType)
	if err != nil {
		return nil, err
	}
	return &nodemanagerclient.UploadVolumeBackupResponse{SizeBytes: sizeBytes}, nil
}

func (m *mockWorkerForBackup) RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error {
	buf, err := nodemanagerclient.GetPresignedBytes(ctx, presignedURL)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.importedCall++
	m.volumeData[volumeID] = buf
	m.mu.Unlock()
	return nil
}

func (m *mockWorkerForBackup) UploadImageBackup(ctx context.Context, volumeID string, req nodemanagerclient.UploadImageBackupRequest) (*nodemanagerclient.UploadImageBackupResponse, error) {
	m.mu.Lock()
	data := append([]byte(nil), m.volumeData[volumeID]...)
	m.mu.Unlock()

	partCap := req.MaxPartBytes
	if partCap <= 0 {
		partCap = int64(len(data))
		if partCap == 0 {
			partCap = 1
		}
	}

	var partSizes []int64
	var totalBytes int64
	for offset, partIdx := 0, 0; offset < len(data) || (len(data) == 0 && partIdx == 0); partIdx++ {
		if partIdx >= len(req.PresignedURLs) {
			return nil, fmt.Errorf("not enough presigned URLs (%d)", len(req.PresignedURLs))
		}
		end := offset + int(partCap)
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		n, err := nodemanagerclient.PutPresignedBytes(ctx, req.PresignedURLs[partIdx], chunk, backupContentType)
		if err != nil {
			return nil, err
		}
		partSizes = append(partSizes, n)
		totalBytes += n
		offset = end
		if len(data) == 0 {
			break
		}
	}

	return &nodemanagerclient.UploadImageBackupResponse{
		PartsUploaded: len(partSizes),
		PartSizes:     partSizes,
		TotalBytes:    totalBytes,
	}, nil
}

func (m *mockWorkerForBackup) RestoreImageBackup(ctx context.Context, volumeID string, presignedURLs []string) (*nodemanagerclient.RestoreImageBackupResponse, error) {
	var merged bytes.Buffer
	for _, u := range presignedURLs {
		partBytes, err := nodemanagerclient.GetPresignedBytes(ctx, u)
		if err != nil {
			return nil, err
		}
		merged.Write(partBytes)
	}
	m.mu.Lock()
	m.volumeData[volumeID] = merged.Bytes()
	m.mu.Unlock()
	return &nodemanagerclient.RestoreImageBackupResponse{SizeBytes: int64(merged.Len())}, nil
}

func (m *mockWorkerForBackup) CreateContainer(ctx context.Context, req nodemanagerclient.CreateContainerRequest) (*nodemanagerclient.CreateContainerResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("ctr-%d", req.ServerID)
	m.createdCtrs[id] = req
	return &nodemanagerclient.CreateContainerResponse{ContainerID: id, Port: req.Port, MountPath: req.MountPath}, nil
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

func (m *mockWorkerForBackup) GetContainerStatus(ctx context.Context, containerID string) (*nodemanagerclient.ContainerStatusResponse, error) {
	return &nodemanagerclient.ContainerStatusResponse{ContainerID: containerID, Status: nodemanagerclient.ContainerStatusRunning}, nil
}

func (m *mockWorkerForBackup) GetContainerLogs(ctx context.Context, containerID string, tail int) (*nodemanagerclient.ContainerLogsResponse, error) {
	return &nodemanagerclient.ContainerLogsResponse{ContainerID: containerID, Logs: ""}, nil
}

func (m *mockWorkerForBackup) ExecContainerCommand(ctx context.Context, containerID string, command string) (*nodemanagerclient.ExecCommandResponse, error) {
	return &nodemanagerclient.ExecCommandResponse{ContainerID: containerID, Command: command, Output: ""}, nil
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
	service := NewService(db, sp, func(baseURL, apiKey string) nodemanagerclient.Client {
		return mockWorker
	})

	user := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("backup-user-%d@example.com", time.Now().UnixNano()),
			PasswordHash: "hashed",
			Role:         domain.UserRoleUser,
		},
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
	tokens, _ := authkitjwt.GenerateTokenPair(user.ID, user.Email, user.Role, jwtSecret)

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

func TestImageBackupMultiPartRestoreAndPruning(t *testing.T) {
	service, mockWorker, sp, server, _, _ := setupBackupTestEnv(t)
	if service == nil {
		return
	}
	service.SetMaxBackups(8)
	service.SetMaxImagePartBytes(10) // Force multi-part uploads on 25-byte payloads

	ctx := context.Background()
	volID := fmt.Sprintf("%d", server.ID)

	var created []*domain.ServerImageBackup
	for i := 1; i <= 10; i++ {
		payload := []byte(fmt.Sprintf("ext4-img-payload-num-%04d", i))
		mockWorker.volumeData[volID] = payload

		pending, err := service.CreatePendingImageBackup(ctx, server.ID)
		if err != nil {
			t.Fatalf("CreatePendingImageBackup #%d failed: %v", i, err)
		}
		rec, err := service.ExecuteImageBackupRecord(ctx, 0, server.ID, pending.ID)
		if err != nil {
			t.Fatalf("ExecuteImageBackupRecord #%d failed: %v", i, err)
		}
		if rec.PartCount != 3 {
			t.Fatalf("expected 3 parts for 25-byte payload with 10-byte cap, got %d", rec.PartCount)
		}
		created = append(created, rec)
		time.Sleep(2 * time.Millisecond)
	}

	remaining, err := service.ListImageBackups(ctx, server.ID)
	if err != nil {
		t.Fatalf("ListImageBackups failed: %v", err)
	}
	if len(remaining) != 8 {
		t.Fatalf("expected 8 image backups retained, got %d", len(remaining))
	}

	// All parts of the oldest 2 pruned image backups must be deleted from storage
	for _, old := range created[:2] {
		oldKeys, err := old.GetPartKeys()
		if err != nil {
			t.Fatalf("GetPartKeys failed: %v", err)
		}
		for _, k := range oldKeys {
			if _, err := sp.Get(ctx, k); err == nil {
				t.Errorf("expected pruned image backup part %s to be deleted from storage", k)
			}
		}
	}

	// Restore from newest multi-part image backup and verify exact bytes restored
	newest := created[len(created)-1]
	mockWorker.volumeData[volID] = []byte("corrupted-img")
	if err := service.RestoreImageBackup(ctx, server.ID, newest.ID); err != nil {
		t.Fatalf("RestoreImageBackup failed: %v", err)
	}
	if got, want := string(mockWorker.volumeData[volID]), "ext4-img-payload-num-0010"; got != want {
		t.Errorf("restored image payload = %q, want %q", got, want)
	}
}

func TestBackupRetentionPrunesOldestBeyondMaxCount(t *testing.T) {
	service, mockWorker, sp, server, _, _ := setupBackupTestEnv(t)
	if service == nil {
		return
	}
	service.SetMaxBackups(8)

	ctx := context.Background()
	volID := fmt.Sprintf("%d", server.ID)

	var created []*domain.BackupRecord
	for i := 1; i <= 10; i++ {
		mockWorker.volumeData[volID] = []byte(fmt.Sprintf("snapshot-%02d", i))
		rec, err := service.BackupServer(ctx, server.ID)
		if err != nil {
			t.Fatalf("BackupServer #%d failed: %v", i, err)
		}
		created = append(created, rec)
		time.Sleep(2 * time.Millisecond)
	}

	remaining, err := service.ListBackups(ctx, server.ID)
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(remaining) != 8 {
		t.Fatalf("expected 8 backups retained, got %d", len(remaining))
	}

	// Oldest 2 backups must be pruned from both DB and storage
	for _, old := range created[:2] {
		if _, err := sp.Get(ctx, old.StorageKey); err == nil {
			t.Errorf("expected pruned backup key %s to be deleted from storage", old.StorageKey)
		}
	}
	// Newest 8 backups must remain accessible in storage
	for _, kept := range created[2:] {
		if _, err := sp.Get(ctx, kept.StorageKey); err != nil {
			t.Errorf("expected retained backup key %s to exist in storage: %v", kept.StorageKey, err)
		}
	}
}

func TestAdminImageBackupHTTPRoutes(t *testing.T) {
	service, mockWorker, _, server, user, userToken := setupBackupTestEnv(t)
	if service == nil {
		return
	}

	volID := fmt.Sprintf("%d", server.ID)
	mockWorker.volumeData[volID] = []byte("admin-ext4-image-bytes")

	jwtSecret := "backup-secret-key"
	adminTokens, err := authkitjwt.GenerateTokenPair(user.ID, "admin@example.com", domain.UserRoleAdmin, jwtSecret)
	if err != nil {
		t.Fatalf("failed generating admin token: %v", err)
	}

	handler := NewHandler(service)
	r := gin.New()
	handler.RegisterRoutes(r, jwtSecret)

	// Non-admin must receive 403 Forbidden on all admin image-backup routes
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/admin/image-backups"},
		{http.MethodGet, fmt.Sprintf("/api/v1/admin/servers/%d/image-backups", server.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/servers/%d/image-backups", server.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/servers/%d/image-backups/1/restore", server.ID)},
	} {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+userToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("non-admin %s %s expected 403, got %d (%s)", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// Admin POST creates image backup
	wCreate := httptest.NewRecorder()
	reqCreate, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/admin/servers/%d/image-backups", server.ID), nil)
	reqCreate.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
	r.ServeHTTP(wCreate, reqCreate)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("admin create image backup failed: %d (%s)", wCreate.Code, wCreate.Body.String())
	}
	var created domain.ServerImageBackup
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil || created.ID == 0 {
		t.Fatalf("unexpected create image backup response: %s (err=%v)", wCreate.Body.String(), err)
	}

	// Admin GET lists image backups
	wList := httptest.NewRecorder()
	reqList, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/servers/%d/image-backups", server.ID), nil)
	reqList.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
	r.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("admin list image backups failed: %d (%s)", wList.Code, wList.Body.String())
	}

	// Admin POST restores image backup
	mockWorker.volumeData[volID] = []byte("corrupted")
	wRestore := httptest.NewRecorder()
	reqRestore, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/admin/servers/%d/image-backups/%d/restore", server.ID, created.ID), nil)
	reqRestore.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
	r.ServeHTTP(wRestore, reqRestore)
	if wRestore.Code != http.StatusOK {
		t.Fatalf("admin restore image backup failed: %d (%s)", wRestore.Code, wRestore.Body.String())
	}
	if got := string(mockWorker.volumeData[volID]); got != "admin-ext4-image-bytes" {
		t.Errorf("restored bytes = %q, want %q", got, "admin-ext4-image-bytes")
	}

	// Admin POST /api/v1/admin/image-backups backs up all provisioned servers
	wAll := httptest.NewRecorder()
	reqAll, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/image-backups", nil)
	reqAll.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
	r.ServeHTTP(wAll, reqAll)
	if wAll.Code != http.StatusCreated {
		t.Fatalf("admin create all image backups failed: %d (%s)", wAll.Code, wAll.Body.String())
	}
}
