package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
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

func (m *mockContainerManager) GetLogs(ctx context.Context, containerID string, tail int) (*ContainerLogsResponse, error) {
	return &ContainerLogsResponse{
		ContainerID: containerID,
		Logs:        "[Server thread/INFO]: Done (1.234s)! For help, type \"help\"\n",
	}, nil
}

func (m *mockContainerManager) ExecCommand(ctx context.Context, containerID string, user string, command string) (*ExecCommandResponse, error) {
	return &ExecCommandResponse{
		ContainerID: containerID,
		Command:     command,
		Output:      "Executed: " + command,
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

func (m *mockContainerManager) CleanupAll(ctx context.Context) error {
	for id := range m.containers {
		delete(m.containers, id)
	}
	return nil
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

func TestDeleteVolumeRemovesFilesAndImageAcrossRestart(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worker-vol-del-*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	ctx := context.Background()
	vm1 := NewLocalVolumeManager(tempDir, false)
	mountPath, imagePath, err := vm1.CreateVolume(ctx, "77", 1)
	if err != nil {
		t.Fatalf("CreateVolume failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(mountPath, "server.properties"), []byte("motd=test\n"), 0644); err != nil {
		t.Fatalf("failed writing server file: %v", err)
	}

	// Simulate worker restart with a fresh LocalVolumeManager that has empty in-memory maps.
	vm2 := NewLocalVolumeManager(tempDir, false)
	if err := vm2.DeleteVolume(ctx, "77"); err != nil {
		t.Fatalf("DeleteVolume failed: %v", err)
	}

	if _, err := os.Stat(mountPath); !os.IsNotExist(err) {
		t.Errorf("expected mount dir %s to be removed, got err=%v", mountPath, err)
	}
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		t.Errorf("expected image file %s to be deleted, got err=%v", imagePath, err)
	}
}

func TestIsManagedContainer(t *testing.T) {
	tests := []struct {
		name string
		ctr  types.Container
		want bool
	}{
		{
			name: "platform label",
			ctr:  types.Container{Labels: map[string]string{"platform": managedPlatformLabel}},
			want: true,
		},
		{
			name: "mc-server name prefix",
			ctr:  types.Container{Names: []string{"/mc-server-12"}},
			want: true,
		},
		{
			name: "empty names and no label",
			ctr:  types.Container{},
			want: false,
		},
		{
			name: "unrelated container",
			ctr:  types.Container{Names: []string{"/mc_postgres"}, Labels: map[string]string{"com.docker.compose.service": "postgres"}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isManagedContainer(tc.ctr); got != tc.want {
				t.Errorf("isManagedContainer(%+v) = %v, want %v", tc.ctr, got, tc.want)
			}
		})
	}
}

func TestIsMountPoint(t *testing.T) {
	mountedRoot, err := isMountPoint("/")
	if err != nil {
		t.Fatalf("isMountPoint('/') returned error: %v", err)
	}
	if !mountedRoot {
		t.Error("expected '/' to be reported as a mountpoint")
	}

	tempDir, err := os.MkdirTemp("", "worker-mp-*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	mountedTemp, err := isMountPoint(tempDir)
	if err != nil {
		t.Fatalf("isMountPoint(tempDir) returned error: %v", err)
	}
	if mountedTemp {
		t.Errorf("expected %s not to be a mountpoint", tempDir)
	}
}

func TestDockerStopAndDeleteIgnoreNotFound(t *testing.T) {
	statusCode := http.StatusNotFound
	fakeDocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(`{"message":"No such container: missing-ctr"}`))
	}))
	defer fakeDocker.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost(fakeDocker.URL),
		client.WithHTTPClient(fakeDocker.Client()),
		client.WithVersion("1.45"),
	)
	if err != nil {
		t.Fatalf("failed creating docker client: %v", err)
	}
	cm := &DockerContainerManager{cli: cli}

	if err := cm.Stop(context.Background(), "missing-ctr"); err != nil {
		t.Fatalf("expected Stop to ignore 404 NotFound, got: %v", err)
	}
	if err := cm.Delete(context.Background(), "missing-ctr"); err != nil {
		t.Fatalf("expected Delete to ignore 404 NotFound, got: %v", err)
	}

	statusCode = http.StatusInternalServerError
	if err := cm.Stop(context.Background(), "err-ctr"); err == nil {
		t.Fatal("expected Stop to return error on 500 response")
	}
	if err := cm.Delete(context.Background(), "err-ctr"); err == nil {
		t.Fatal("expected Delete to return error on 500 response")
	}
}

func TestExportAndImportSkipLostAndFoundAndCheckCleanup(t *testing.T) {
	tempBaseDir, err := os.MkdirTemp("", "worker-vol-laf-*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempBaseDir) }()

	vm := NewLocalVolumeManager(tempBaseDir, false)
	ctx := context.Background()

	mountPath, _, err := vm.CreateVolume(ctx, "1", 1)
	if err != nil {
		t.Fatalf("CreateVolume failed: %v", err)
	}

	lostAndFound := filepath.Join(mountPath, "lost+found")
	if err := os.MkdirAll(lostAndFound, 0700); err != nil {
		t.Fatalf("failed creating lost+found: %v", err)
	}
	// Make lost+found non-readable when running non-root so filepath.Walk would fail if not skipped
	if os.Geteuid() != 0 {
		if err := os.Chmod(lostAndFound, 0000); err != nil {
			t.Fatalf("failed chmodding lost+found: %v", err)
		}
		defer func() { _ = os.Chmod(lostAndFound, 0700) }()
	}

	if err := os.WriteFile(filepath.Join(mountPath, "server.properties"), []byte("motd=test\n"), 0644); err != nil {
		t.Fatalf("failed writing server.properties: %v", err)
	}

	var tarBuf bytes.Buffer
	if err := vm.ExportVolume(ctx, "1", &tarBuf); err != nil {
		t.Fatalf("ExportVolume should skip lost+found, got error: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(tarBuf.Bytes()))
	found := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading exported tar failed: %v", err)
		}
		found[hdr.Name] = true
	}
	if found["lost+found"] {
		t.Fatal("expected lost+found to be omitted from exported archive")
	}
	if !found["server.properties"] {
		t.Fatalf("expected server.properties in exported archive, found: %+v", found)
	}

	// Create stale file and verify ImportVolume removes it while keeping lost+found
	stalePath := filepath.Join(mountPath, "stale.txt")
	if err := os.WriteFile(stalePath, []byte("old"), 0644); err != nil {
		t.Fatalf("failed writing stale file: %v", err)
	}
	if err := vm.ImportVolume(ctx, "1", bytes.NewReader(tarBuf.Bytes())); err != nil {
		t.Fatalf("ImportVolume failed: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale.txt to be cleaned up during ImportVolume, stat err=%v", err)
	}
	if _, err := os.Stat(lostAndFound); err != nil {
		t.Fatalf("expected lost+found directory to be preserved, stat err=%v", err)
	}
}

func TestParseLoopDeviceIndex(t *testing.T) {
	tests := []struct {
		devPath string
		wantIdx int
		wantOK  bool
	}{
		{devPath: "/dev/loop0", wantIdx: 0, wantOK: true},
		{devPath: "/dev/loop14", wantIdx: 14, wantOK: true},
		{devPath: "/dev/loop-control", wantIdx: 0, wantOK: false},
		{devPath: "/dev/loop", wantIdx: 0, wantOK: false},
		{devPath: "/dev/loop9p1", wantIdx: 0, wantOK: false},
		{devPath: "/dev/sda1", wantIdx: 0, wantOK: false},
	}

	for _, tc := range tests {
		gotIdx, gotOK := parseLoopDeviceIndex(tc.devPath)
		if gotOK != tc.wantOK || gotIdx != tc.wantIdx {
			t.Errorf("parseLoopDeviceIndex(%q) = (%d, %v), want (%d, %v)", tc.devPath, gotIdx, gotOK, tc.wantIdx, tc.wantOK)
		}
	}
}

func TestParseMountInfoLine(t *testing.T) {
	line := `412 105 7:14 / /var/lib/mc-platform/mounts/14 rw,relatime shared:348 master:1 - ext4 /dev/loop14 rw`
	entry, ok := parseMountInfoLine(line)
	if !ok {
		t.Fatal("expected valid mountinfo line to parse")
	}
	if entry.mountPoint != "/var/lib/mc-platform/mounts/14" {
		t.Errorf("mountPoint = %q, want %q", entry.mountPoint, "/var/lib/mc-platform/mounts/14")
	}
	if entry.source != "/dev/loop14" {
		t.Errorf("source = %q, want %q", entry.source, "/dev/loop14")
	}

	escapedLine := `10 1 7:2 / /mnt/my\040dir rw - ext4 /dev/loop2 rw`
	escapedEntry, ok := parseMountInfoLine(escapedLine)
	if !ok {
		t.Fatal("expected escaped mountinfo line to parse")
	}
	if escapedEntry.mountPoint != "/mnt/my dir" || escapedEntry.source != "/dev/loop2" {
		t.Errorf("got %+v, want mountPoint=/mnt/my dir source=/dev/loop2", escapedEntry)
	}

	if _, ok := parseMountInfoLine("short line"); ok {
		t.Error("expected malformed mountinfo line to be rejected")
	}
}

func TestPresignedVolumeBackupUploadAndRestore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	tmpDir := t.TempDir()

	vm := NewLocalVolumeManager(tmpDir, false)
	cm := newMockContainerManager()
	handler := NewHandler(cm, vm, 10, tmpDir)
	router := gin.New()
	handler.RegisterRoutes(router, "worker-secret")

	workerSrv := httptest.NewServer(router)
	defer workerSrv.Close()
	wClient := NewClient(workerSrv.URL, "worker-secret")

	// Prepare volume directory with Minecraft files
	mountDir := filepath.Join(tmpDir, "mounts", "42")
	if err := os.MkdirAll(filepath.Join(mountDir, "world"), 0755); err != nil {
		t.Fatalf("failed creating mount dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "server.properties"), []byte("motd=PresignedWorld\n"), 0644); err != nil {
		t.Fatalf("failed writing server.properties: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "world", "level.dat"), []byte("chunk-data-123"), 0644); err != nil {
		t.Fatalf("failed writing level.dat: %v", err)
	}

	// Mock presigned S3 endpoint verifying Content-Type and exact Content-Length
	var mu sync.Mutex
	objects := make(map[string][]byte)
	var lastContentType string
	var lastContentLength int64
	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			lastContentType = r.Header.Get("Content-Type")
			lastContentLength = r.ContentLength
			objects[r.URL.Path] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer s3Srv.Close()

	// 1. Upload .tar.gz backup via presigned PUT
	putURL := s3Srv.URL + "/backups/server-42/snap1.tar.gz"
	upResp, err := wClient.UploadVolumeBackup(ctx, "42", putURL)
	if err != nil {
		t.Fatalf("UploadVolumeBackup failed: %v", err)
	}
	if upResp.SizeBytes <= 0 {
		t.Fatalf("expected positive uploaded size, got %d", upResp.SizeBytes)
	}

	mu.Lock()
	uploaded := objects["/backups/server-42/snap1.tar.gz"]
	ct := lastContentType
	cl := lastContentLength
	mu.Unlock()

	if ct != "application/gzip" {
		t.Errorf("expected Content-Type application/gzip, got %q", ct)
	}
	if cl != int64(len(uploaded)) || cl != upResp.SizeBytes {
		t.Errorf("Content-Length mismatch: header=%d actual=%d resp=%d", cl, len(uploaded), upResp.SizeBytes)
	}
	if len(uploaded) < 2 || uploaded[0] != gzipMagicByte0 || uploaded[1] != gzipMagicByte1 {
		t.Fatal("uploaded backup is not gzip-compressed")
	}
	// Sanity-check the uploaded tar.gz is readable
	gzr, err := gzip.NewReader(bytes.NewReader(uploaded))
	if err != nil {
		t.Fatalf("uploaded archive is invalid gzip: %v", err)
	}
	_ = gzr.Close()

	// 2. Corrupt volume and add stale file
	_ = os.WriteFile(filepath.Join(mountDir, "server.properties"), []byte("motd=Corrupted\n"), 0644)
	_ = os.WriteFile(filepath.Join(mountDir, "stale.txt"), []byte("remove-me"), 0644)

	// 3. Restore .tar.gz via presigned GET
	if err := wClient.RestoreVolumeBackup(ctx, "42", putURL); err != nil {
		t.Fatalf("RestoreVolumeBackup (.tar.gz) failed: %v", err)
	}
	gotProps, err := os.ReadFile(filepath.Join(mountDir, "server.properties"))
	if err != nil || string(gotProps) != "motd=PresignedWorld\n" {
		t.Fatalf("restored server.properties = %q (err=%v), want motd=PresignedWorld", string(gotProps), err)
	}
	if _, err := os.Stat(filepath.Join(mountDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected stale.txt to be removed during restore")
	}

	// 4. Verify legacy uncompressed .tar restore still works
	var plainTar bytes.Buffer
	tw := tar.NewWriter(&plainTar)
	legacyContent := []byte("motd=LegacyPlainTar\n")
	_ = tw.WriteHeader(&tar.Header{Name: "server.properties", Mode: 0644, Size: int64(len(legacyContent))})
	_, _ = tw.Write(legacyContent)
	_ = tw.Close()

	mu.Lock()
	objects["/backups/server-42/legacy.tar"] = plainTar.Bytes()
	mu.Unlock()

	if err := wClient.RestoreVolumeBackup(ctx, "42", s3Srv.URL+"/backups/server-42/legacy.tar"); err != nil {
		t.Fatalf("RestoreVolumeBackup (legacy .tar) failed: %v", err)
	}
	gotLegacy, err := os.ReadFile(filepath.Join(mountDir, "server.properties"))
	if err != nil || string(gotLegacy) != string(legacyContent) {
		t.Fatalf("legacy restored server.properties = %q (err=%v), want %q", string(gotLegacy), err, string(legacyContent))
	}
}
