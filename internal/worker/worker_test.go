package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// mockContainerManager simulates container operations in testing environments.
type mockContainerManager struct {
	containers map[string]ContainerStatus
}

func newMockContainerManager() *mockContainerManager {
	return &mockContainerManager{
		containers: make(map[string]ContainerStatus),
	}
}

func (m *mockContainerManager) CreateAndStart(ctx context.Context, req CreateContainerRequest) (*CreateContainerResponse, error) {
	id := fmt.Sprintf("mock-ctr-%d", req.ServerID)
	m.containers[id] = ContainerStatusRunning
	return &CreateContainerResponse{
		ContainerID: id,
		Port:        req.Port,
		MountPath:   req.MountPath,
	}, nil
}

func (m *mockContainerManager) Stop(ctx context.Context, containerID string) error {
	m.containers[containerID] = ContainerStatusStopped
	return nil
}

func (m *mockContainerManager) Delete(ctx context.Context, containerID string) error {
	delete(m.containers, containerID)
	return nil
}

func (m *mockContainerManager) GetStatus(ctx context.Context, containerID string) (*ContainerStatusResponse, error) {
	status, exists := m.containers[containerID]
	if !exists {
		return &ContainerStatusResponse{
			ContainerID: containerID,
			Status:      ContainerStatusUnknown,
		}, nil
	}
	return &ContainerStatusResponse{
		ContainerID: containerID,
		Status:      status,
	}, nil
}

func (m *mockContainerManager) CountActive(ctx context.Context) (int, error) {
	active := 0
	for _, s := range m.containers {
		if s == ContainerStatusRunning {
			active++
		}
	}
	return active, nil
}

func setupWorkerTestRouter(t *testing.T, apiKey string) (*gin.Engine, *LocalVolumeManager, *mockContainerManager, string) {
	gin.SetMode(gin.TestMode)
	tempDir, err := os.MkdirTemp("", "worker-test-*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}

	cm := newMockContainerManager()
	vm := NewLocalVolumeManager(tempDir, false) // loop mount disabled for unprivileged test runners
	handler := NewHandler(cm, vm, 25, tempDir)

	r := gin.New()
	handler.RegisterRoutes(r, apiKey)
	return r, vm, cm, tempDir
}

func TestWorkerAuthMiddleware(t *testing.T) {
	apiKey := "test-secret-worker-key"
	r, _, _, tempDir := setupWorkerTestRouter(t, apiKey)
	defer func() { _ = os.RemoveAll(tempDir) }()

	// 1. Missing key returns 401
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/status", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing key, got %d", w.Code)
	}

	// 2. Wrong key returns 401
	wBad := httptest.NewRecorder()
	reqBad, _ := http.NewRequest(http.MethodGet, "/status", nil)
	reqBad.Header.Set(APIKeyHeader, "wrong-key")
	r.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid key, got %d", wBad.Code)
	}

	// 3. Valid key in header succeeds
	wGood := httptest.NewRecorder()
	reqGood, _ := http.NewRequest(http.MethodGet, "/status", nil)
	reqGood.Header.Set(APIKeyHeader, apiKey)
	r.ServeHTTP(wGood, reqGood)
	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid key, got %d", wGood.Code)
	}

	// 4. Valid key via Bearer authorization succeeds
	wBearer := httptest.NewRecorder()
	reqBearer, _ := http.NewRequest(http.MethodGet, "/status", nil)
	reqBearer.Header.Set("Authorization", "Bearer "+apiKey)
	r.ServeHTTP(wBearer, reqBearer)
	if wBearer.Code != http.StatusOK {
		t.Fatalf("expected 200 with Bearer auth, got %d", wBearer.Code)
	}
}

func TestVolumeExportImportRoundTrip(t *testing.T) {
	apiKey := "test-secret-worker-key"
	r, vm, _, tempDir := setupWorkerTestRouter(t, apiKey)
	defer func() { _ = os.RemoveAll(tempDir) }()

	ctx := context.Background()
	volumeID1 := "101"

	mountPath1, _, err := vm.CreateVolume(ctx, volumeID1, 1)
	if err != nil {
		t.Fatalf("failed creating source volume: %v", err)
	}

	// Write sample world files inside source volume
	testFile1 := filepath.Join(mountPath1, "server.properties")
	testFile2 := filepath.Join(mountPath1, "world", "level.dat")
	if err := os.MkdirAll(filepath.Dir(testFile2), 0755); err != nil {
		t.Fatalf("failed creating world subdir: %v", err)
	}
	if err := os.WriteFile(testFile1, []byte("motd=HelloAntigravity\npvp=true\n"), 0644); err != nil {
		t.Fatalf("failed writing test file 1: %v", err)
	}
	if err := os.WriteFile(testFile2, []byte("dummy-level-binary-data"), 0644); err != nil {
		t.Fatalf("failed writing test file 2: %v", err)
	}

	// Export volume 1 via GET /volumes/101/export
	wExport := httptest.NewRecorder()
	reqExport, _ := http.NewRequest(http.MethodGet, "/volumes/"+volumeID1+"/export", nil)
	reqExport.Header.Set(APIKeyHeader, apiKey)
	r.ServeHTTP(wExport, reqExport)

	if wExport.Code != http.StatusOK {
		t.Fatalf("export volume failed with status %d: %s", wExport.Code, wExport.Body.String())
	}
	exportedTarBytes := wExport.Body.Bytes()
	if len(exportedTarBytes) == 0 {
		t.Fatal("exported tar is empty")
	}

	// Validate tar structure
	tr := tar.NewReader(bytes.NewReader(exportedTarBytes))
	foundFiles := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar reading failed: %v", err)
		}
		foundFiles[hdr.Name] = true
	}
	if !foundFiles["server.properties"] || !foundFiles["world/level.dat"] {
		t.Fatalf("expected tar entries missing, found: %+v", foundFiles)
	}

	// Create a second volume 102 and import the archive into it
	volumeID2 := "102"
	mountPath2, _, err := vm.CreateVolume(ctx, volumeID2, 1)
	if err != nil {
		t.Fatalf("failed creating destination volume: %v", err)
	}

	wImport := httptest.NewRecorder()
	reqImport, _ := http.NewRequest(http.MethodPost, "/volumes/"+volumeID2+"/import", bytes.NewReader(exportedTarBytes))
	reqImport.Header.Set(APIKeyHeader, apiKey)
	reqImport.Header.Set("Content-Type", "application/x-tar")
	r.ServeHTTP(wImport, reqImport)

	if wImport.Code != http.StatusOK {
		t.Fatalf("import volume failed with status %d: %s", wImport.Code, wImport.Body.String())
	}

	// Verify target files exist and contents match
	importedFile1 := filepath.Join(mountPath2, "server.properties")
	importedFile2 := filepath.Join(mountPath2, "world", "level.dat")

	data1, err := os.ReadFile(importedFile1)
	if err != nil {
		t.Fatalf("failed reading imported file 1: %v", err)
	}
	if string(data1) != "motd=HelloAntigravity\npvp=true\n" {
		t.Errorf("content mismatch in file 1: got %s", string(data1))
	}

	data2, err := os.ReadFile(importedFile2)
	if err != nil {
		t.Fatalf("failed reading imported file 2: %v", err)
	}
	if string(data2) != "dummy-level-binary-data" {
		t.Errorf("content mismatch in file 2: got %s", string(data2))
	}

	// Clean up volumes
	wDelete := httptest.NewRecorder()
	reqDelete, _ := http.NewRequest(http.MethodDelete, "/volumes/"+volumeID1, nil)
	reqDelete.Header.Set(APIKeyHeader, apiKey)
	r.ServeHTTP(wDelete, reqDelete)
	if wDelete.Code != http.StatusOK {
		t.Errorf("delete volume failed: %d", wDelete.Code)
	}
}

func TestContainerEndpoints(t *testing.T) {
	apiKey := "test-secret-worker-key"
	r, _, cm, tempDir := setupWorkerTestRouter(t, apiKey)
	defer func() { _ = os.RemoveAll(tempDir) }()

	// 1. Create container
	createReq := CreateContainerRequest{
		ServerID:    201,
		Port:        25565,
		CPULimit:    2,
		MemLimitMB:  2048,
		DiskLimitGB: 20,
		MountPath:   "/data/mc",
	}
	body, _ := json.Marshal(createReq)
	wCreate := httptest.NewRecorder()
	reqCreate, _ := http.NewRequest(http.MethodPost, "/containers", bytes.NewReader(body))
	reqCreate.Header.Set(APIKeyHeader, apiKey)
	reqCreate.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create container failed: %d: %s", wCreate.Code, wCreate.Body.String())
	}

	var resp CreateContainerResponse
	_ = json.Unmarshal(wCreate.Body.Bytes(), &resp)
	if resp.ContainerID != "mock-ctr-201" {
		t.Errorf("unexpected container id: %s", resp.ContainerID)
	}

	// 2. Inspect status
	wStatus := httptest.NewRecorder()
	reqStatus, _ := http.NewRequest(http.MethodGet, "/containers/"+resp.ContainerID+"/status", nil)
	reqStatus.Header.Set(APIKeyHeader, apiKey)
	r.ServeHTTP(wStatus, reqStatus)
	if wStatus.Code != http.StatusOK {
		t.Fatalf("get container status failed: %d", wStatus.Code)
	}

	var statusResp ContainerStatusResponse
	_ = json.Unmarshal(wStatus.Body.Bytes(), &statusResp)
	if statusResp.Status != ContainerStatusRunning {
		t.Errorf("expected running status, got: %s", statusResp.Status)
	}

	// 3. Stop container
	wStop := httptest.NewRecorder()
	reqStop, _ := http.NewRequest(http.MethodPost, "/containers/"+resp.ContainerID+"/stop", nil)
	reqStop.Header.Set(APIKeyHeader, apiKey)
	r.ServeHTTP(wStop, reqStop)
	if wStop.Code != http.StatusOK {
		t.Fatalf("stop container failed: %d", wStop.Code)
	}

	// 4. Delete container
	wDel := httptest.NewRecorder()
	reqDel, _ := http.NewRequest(http.MethodDelete, "/containers/"+resp.ContainerID, nil)
	reqDel.Header.Set(APIKeyHeader, apiKey)
	r.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete container failed: %d", wDel.Code)
	}

	if _, exists := cm.containers[resp.ContainerID]; exists {
		t.Error("expected container to be deleted")
	}
}
