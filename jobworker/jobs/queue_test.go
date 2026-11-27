package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dreamoutbox/go-authkit"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/storage"
)

type queueTestWorker struct {
	mu            sync.Mutex
	failCreateVol int
	failExportVol int
	volumes       map[string][]byte
	containers    map[string]nodemanagerclient.CreateContainerRequest
}

func newQueueTestWorker() *queueTestWorker {
	return &queueTestWorker{
		volumes:    make(map[string][]byte),
		containers: make(map[string]nodemanagerclient.CreateContainerRequest),
	}
}

func (q *queueTestWorker) GetStatus(ctx context.Context) (*nodemanagerclient.HostStatusResponse, error) {
	return &nodemanagerclient.HostStatusResponse{Hostname: "queue-node", MaxServers: 10}, nil
}

func (q *queueTestWorker) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*nodemanagerclient.CreateVolumeResponse, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failCreateVol > 0 {
		q.failCreateVol--
		return nil, errors.New("simulated transient volume failure")
	}
	id := fmt.Sprintf("%d", serverID)
	q.volumes[id] = []byte("initial-world-data")
	return &nodemanagerclient.CreateVolumeResponse{MountPath: "/var/lib/mc-platform/mounts/" + id}, nil
}

func (q *queueTestWorker) DeleteVolume(ctx context.Context, volumeID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.volumes, volumeID)
	return nil
}

func (q *queueTestWorker) ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failExportVol > 0 {
		q.failExportVol--
		return nil, errors.New("simulated export failure")
	}
	return io.NopCloser(bytes.NewReader(q.volumes[volumeID])), nil
}

func (q *queueTestWorker) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	b, _ := io.ReadAll(r)
	q.volumes[volumeID] = b
	return nil
}

func (q *queueTestWorker) UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (*nodemanagerclient.UploadVolumeBackupResponse, error) {
	q.mu.Lock()
	if q.failExportVol > 0 {
		q.failExportVol--
		q.mu.Unlock()
		return nil, errors.New("simulated export failure")
	}
	data := append([]byte(nil), q.volumes[volumeID]...)
	q.mu.Unlock()

	sizeBytes, err := nodemanagerclient.PutPresignedBytes(ctx, presignedURL, data, "application/gzip")
	if err != nil {
		return nil, err
	}
	return &nodemanagerclient.UploadVolumeBackupResponse{SizeBytes: sizeBytes}, nil
}

func (q *queueTestWorker) RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error {
	b, err := nodemanagerclient.GetPresignedBytes(ctx, presignedURL)
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.volumes[volumeID] = b
	q.mu.Unlock()
	return nil
}

func (q *queueTestWorker) UploadImageBackup(ctx context.Context, volumeID string, req nodemanagerclient.UploadImageBackupRequest) (*nodemanagerclient.UploadImageBackupResponse, error) {
	q.mu.Lock()
	data := append([]byte(nil), q.volumes[volumeID]...)
	q.mu.Unlock()
	if len(data) == 0 {
		data = []byte("mock-ext4-image-bytes")
	}
	n, err := nodemanagerclient.PutPresignedBytes(ctx, req.PresignedURLs[0], data, "application/gzip")
	if err != nil {
		return nil, err
	}
	return &nodemanagerclient.UploadImageBackupResponse{
		PartsUploaded: 1,
		PartSizes:     []int64{n},
		TotalBytes:    n,
	}, nil
}

func (q *queueTestWorker) RestoreImageBackup(ctx context.Context, volumeID string, presignedURLs []string) (*nodemanagerclient.RestoreImageBackupResponse, error) {
	var merged bytes.Buffer
	for _, u := range presignedURLs {
		b, err := nodemanagerclient.GetPresignedBytes(ctx, u)
		if err != nil {
			return nil, err
		}
		merged.Write(b)
	}
	q.mu.Lock()
	q.volumes[volumeID] = merged.Bytes()
	q.mu.Unlock()
	return &nodemanagerclient.RestoreImageBackupResponse{SizeBytes: int64(merged.Len())}, nil
}

func (q *queueTestWorker) CreateContainer(ctx context.Context, req nodemanagerclient.CreateContainerRequest) (*nodemanagerclient.CreateContainerResponse, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	id := fmt.Sprintf("ctr-q-%d", req.ServerID)
	q.containers[id] = req
	return &nodemanagerclient.CreateContainerResponse{ContainerID: id, Port: req.Port, MountPath: req.MountPath}, nil
}

func (q *queueTestWorker) StopContainer(ctx context.Context, containerID string) error {
	return nil
}

func (q *queueTestWorker) DeleteContainer(ctx context.Context, containerID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.containers, containerID)
	return nil
}

func (q *queueTestWorker) GetContainerStatus(ctx context.Context, containerID string) (*nodemanagerclient.ContainerStatusResponse, error) {
	return &nodemanagerclient.ContainerStatusResponse{ContainerID: containerID, Status: nodemanagerclient.ContainerStatusRunning}, nil
}

func (q *queueTestWorker) GetContainerLogs(ctx context.Context, containerID string, tail int) (*nodemanagerclient.ContainerLogsResponse, error) {
	return &nodemanagerclient.ContainerLogsResponse{ContainerID: containerID, Logs: ""}, nil
}

func (q *queueTestWorker) ExecContainerCommand(ctx context.Context, containerID string, command string) (*nodemanagerclient.ExecCommandResponse, error) {
	return &nodemanagerclient.ExecCommandResponse{ContainerID: containerID, Command: command, Output: ""}, nil
}

func TestAsyncQueueRetryAndOperationLogs(t *testing.T) {
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

	mockWorker := newQueueTestWorker()
	workerFactory := func(baseURL, apiKey string) nodemanagerclient.Client {
		return mockWorker
	}

	// Ensure deterministic active host and clean test port range
	db.Model(&domain.Host{}).Where("1 = 1").Update("status", domain.HostStatusDisabled)
	host := domain.Host{
		Hostname:      fmt.Sprintf("queue-host-%d", time.Now().UnixNano()),
		WorkerBaseURL: "http://queue-host:9090",
		APIKey:        "q-key",
		APIKeyHash:    "q-hash",
		MaxServers:    20,
		Status:        domain.HostStatusActive,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer db.Delete(&host)

	user := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("queue-user-%d@example.com", time.Now().UnixNano()),
			PasswordHash: "hashed",
			Role:         domain.UserRoleUser,
		},
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer db.Delete(&user)

	var plan domain.Plan
	_ = db.First(&plan)

	portMin, portMax := 25800, 25820
	db.Where("port >= ? AND port <= ?", portMin, portMax).Delete(&domain.Server{})

	hostingSvc := hosting.NewService(db, hosting.NewLeastLoadedScheduler(db), hosting.NewPortAllocator(db, portMin, portMax), workerFactory)
	backupSvc := backup.NewService(db, storage.NewMemoryStorageProvider(), workerFactory)
	jobSvc := NewService(db, hostingSvc, backupSvc)
	jobSvc.SetTimings(20*time.Millisecond, 0) // Zero retry backoff for deterministic immediate testing

	ctx := context.Background()

	// Clear any stale pending jobs from prior runs so ProcessNext tests ours cleanly
	db.Model(&domain.ServerJob{}).Where("status = ?", domain.JobStatusPending).Update("status", domain.JobStatusFailed)

	// 1. Force 4 consecutive volume failures so initial 3 auto-retries exhaust -> StatusFailed + ServerStatusError
	mockWorker.failCreateVol = 4

	srv, createJob, err := jobSvc.EnqueueCreateServer(ctx, user.ID, "AsyncServer", plan.ID, domain.ServerTypePaper, "1.21.4")
	if err != nil {
		t.Fatalf("EnqueueCreateServer failed: %v", err)
	}
	if srv.Status != domain.ServerStatusPending {
		t.Fatalf("expected immediate pending status without blocking, got %s", srv.Status)
	}
	if createJob.Status != domain.JobStatusPending {
		t.Fatalf("expected pending create_server job, got %s", createJob.Status)
	}

	// Process 3 attempts -> should fail and put server in error state
	for i := 0; i < 3; i++ {
		processed, procErr := jobSvc.ProcessNext(ctx)
		if !processed || procErr == nil {
			t.Fatalf("attempt %d: expected processed=true and error, got (%v, %v)", i+1, processed, procErr)
		}
	}

	var updatedJob domain.ServerJob
	_ = db.First(&updatedJob, createJob.ID)
	if updatedJob.Status != domain.JobStatusFailed || updatedJob.Attempts != 3 {
		t.Fatalf("expected failed job after 3 attempts, got status=%s attempts=%d", updatedJob.Status, updatedJob.Attempts)
	}
	var updatedSrv domain.Server
	_ = db.First(&updatedSrv, srv.ID)
	if updatedSrv.Status != domain.ServerStatusError {
		t.Fatalf("expected server status error after exhausted retries, got %s", updatedSrv.Status)
	}

	// 2. Manual Retry succeeds once failCreateVol has drained (failCreateVol went 4 -> 1 remaining, so let's reset to 0)
	mockWorker.mu.Lock()
	mockWorker.failCreateVol = 0
	mockWorker.mu.Unlock()

	if _, err := jobSvc.RetryJob(ctx, user.ID, false, createJob.ID); err != nil {
		t.Fatalf("RetryJob failed: %v", err)
	}

	processed, procErr := jobSvc.ProcessNext(ctx)
	if !processed || procErr != nil {
		t.Fatalf("expected retried create_server to succeed, got (%v, %v)", processed, procErr)
	}
	_ = db.First(&updatedSrv, srv.ID)
	if updatedSrv.Status != domain.ServerStatusRunning {
		t.Fatalf("expected server running after retry, got %s", updatedSrv.Status)
	}
	if ctr := mockWorker.containers[updatedSrv.ContainerID]; ctr.ServerType != "PAPER" || ctr.ServerVersion != "1.21.4" {
		t.Fatalf("expected PAPER 1.21.4 passed to container, got %+v", ctr)
	}

	// 3. Stop server synchronously -> logs 'stop' success
	if _, err := hostingSvc.StopServer(ctx, user.ID, srv.ID); err != nil {
		t.Fatalf("StopServer failed: %v", err)
	}

	// 4. EnqueueBackup -> succeeds cleanly and logs 'backup' success
	rec, backupJob, err := jobSvc.EnqueueBackup(ctx, user.ID, srv.ID)
	if err != nil {
		t.Fatalf("EnqueueBackup failed: %v", err)
	}
	if rec.Status != domain.BackupStatusPending {
		t.Errorf("expected pending backup record, got %s", rec.Status)
	}
	if processed, err := jobSvc.ProcessNext(ctx); !processed || err != nil {
		t.Fatalf("backup ProcessNext failed: (%v, %v)", processed, err)
	}
	var updatedBackupJob domain.ServerJob
	_ = db.First(&updatedBackupJob, backupJob.ID)
	if updatedBackupJob.Status != domain.JobStatusCompleted {
		t.Errorf("expected completed backup job, got %s", updatedBackupJob.Status)
	}

	// 5. EnqueueRestore -> succeeds cleanly and logs 'restore' success
	restoreJob, err := jobSvc.EnqueueRestore(ctx, user.ID, srv.ID, rec.ID)
	if err != nil {
		t.Fatalf("EnqueueRestore failed: %v", err)
	}
	if processed, err := jobSvc.ProcessNext(ctx); !processed || err != nil {
		t.Fatalf("restore ProcessNext failed: (%v, %v)", processed, err)
	}
	var updatedRestoreJob domain.ServerJob
	_ = db.First(&updatedRestoreJob, restoreJob.ID)
	if updatedRestoreJob.Status != domain.JobStatusCompleted {
		t.Errorf("expected completed restore job, got %s", updatedRestoreJob.Status)
	}

	// Verify GetJob returns completed job
	fetchedJob, err := jobSvc.GetJob(ctx, user.ID, false, restoreJob.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if fetchedJob.Status != domain.JobStatusCompleted {
		t.Errorf("expected completed status from GetJob, got %s", fetchedJob.Status)
	}

	// 6. EnqueueDeleteServer -> deletes server and logs 'delete' success
	delJob, err := jobSvc.EnqueueDeleteServer(ctx, user.ID, srv.ID)
	if err != nil {
		t.Fatalf("EnqueueDeleteServer failed: %v", err)
	}
	if processed, err := jobSvc.ProcessNext(ctx); !processed || err != nil {
		t.Fatalf("delete ProcessNext failed: (%v, %v)", processed, err)
	}
	var updatedDelJob domain.ServerJob
	_ = db.First(&updatedDelJob, delJob.ID)
	if updatedDelJob.Status != domain.JobStatusCompleted {
		t.Errorf("expected completed delete job, got %s", updatedDelJob.Status)
	}

	// 7. Verify PostgreSQL ServerOperationLog contains error + success create, stop, backup, restore, delete logs
	logs, err := domain.ListServerLogs(ctx, db, srv.ID, 50)
	if err != nil {
		t.Fatalf("ListServerLogs failed: %v", err)
	}
	hasLog := func(op domain.ServerOperationType, status domain.ServerLogStatus) bool {
		for _, l := range logs {
			if l.Operation == op && l.Status == status {
				return true
			}
		}
		return false
	}
	checks := []struct {
		op     domain.ServerOperationType
		status domain.ServerLogStatus
	}{
		{domain.ServerOpCreate, domain.ServerLogStatusError},
		{domain.ServerOpCreate, domain.ServerLogStatusSuccess},
		{domain.ServerOpStop, domain.ServerLogStatusSuccess},
		{domain.ServerOpBackup, domain.ServerLogStatusSuccess},
		{domain.ServerOpRestore, domain.ServerLogStatusSuccess},
		{domain.ServerOpDelete, domain.ServerLogStatusSuccess},
	}
	for _, chk := range checks {
		if !hasLog(chk.op, chk.status) {
			t.Errorf("missing expected PostgreSQL log entry operation=%s status=%s", chk.op, chk.status)
		}
	}
}

func TestLifecycleExpirationAndRetentionSweep(t *testing.T) {
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

	host := domain.Host{
		Hostname:      fmt.Sprintf("lifecycle-host-%d", time.Now().UnixNano()),
		WorkerBaseURL: "http://lifecycle-host:9090",
		APIKey:        "k",
		APIKeyHash:    fmt.Sprintf("h-life-%d", time.Now().UnixNano()),
		MaxServers:    20,
		Status:        domain.HostStatusActive,
	}
	_ = db.Create(&host).Error

	user := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("lifecycle-%d@example.com", time.Now().UnixNano()),
			PasswordHash: "hashed",
			Role:         domain.UserRoleUser,
		},
	}
	_ = db.Create(&user).Error

	var plan domain.Plan
	_ = db.First(&plan)

	tw := &queueTestWorker{}
	workerFactory := func(baseURL string, apiKey string) nodemanagerclient.Client { return tw }
	scheduler := hosting.NewLeastLoadedScheduler(db)
	portAlloc := hosting.NewPortAllocator(db, 26700, 26800)
	hostingSvc := hosting.NewService(db, scheduler, portAlloc, workerFactory)
	backupSvc := backup.NewService(db, storage.NewMemoryStorageProvider(), workerFactory)
	jobSvc := NewService(db, hostingSvc, backupSvc)

	ctx := context.Background()
	now := time.Now()

	// 1. Server expired 1 hour ago (still running, within 7-day retention) -> should be stopped, NOT deleted
	expRecent := now.Add(-1 * time.Hour)
	srvRecent := domain.Server{
		UserID:      user.ID,
		PlanID:      plan.ID,
		HostID:      host.ID,
		Name:        "ExpiredRecent",
		Port:        int(26700 + now.UnixNano()%50),
		Status:      domain.ServerStatusRunning,
		ContainerID: "ctr-expired-recent",
		ExpiresAt:   &expRecent,
	}
	if err := db.Create(&srvRecent).Error; err != nil {
		t.Fatalf("failed creating expired running server: %v", err)
	}

	// 2. Server expired 8 days ago (past 7-day retention) -> should be deleted
	expOld := now.Add(-8 * 24 * time.Hour)
	srvOld := domain.Server{
		UserID:      user.ID,
		PlanID:      plan.ID,
		HostID:      host.ID,
		Name:        "ExpiredOld",
		Port:        int(26751 + now.UnixNano()%49),
		Status:      domain.ServerStatusStopped,
		ContainerID: "ctr-expired-old",
		ExpiresAt:   &expOld,
	}
	if err := db.Create(&srvOld).Error; err != nil {
		t.Fatalf("failed creating retention-expired server: %v", err)
	}

	stopped, err := jobSvc.ProcessExpiredServers(ctx, now)
	if err != nil {
		t.Fatalf("ProcessExpiredServers failed: %v", err)
	}
	if stopped < 1 {
		t.Errorf("expected at least 1 stopped server, got %d", stopped)
	}
	var reloadedRecent domain.Server
	_ = db.First(&reloadedRecent, srvRecent.ID)
	if reloadedRecent.Status != domain.ServerStatusStopped {
		t.Errorf("expected ExpiredRecent status stopped, got %s", reloadedRecent.Status)
	}

	deleted, err := jobSvc.CleanupRetentionServers(ctx, now, 7)
	if err != nil {
		t.Fatalf("CleanupRetentionServers failed: %v", err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted server, got %d", deleted)
	}
	var countOld int64
	_ = db.Model(&domain.Server{}).Where("id = ?", srvOld.ID).Count(&countOld)
	if countOld != 0 {
		t.Errorf("expected ExpiredOld server %d to be deleted, still exists", srvOld.ID)
	}
	var countRecent int64
	_ = db.Model(&domain.Server{}).Where("id = ?", srvRecent.ID).Count(&countRecent)
	if countRecent != 1 {
		t.Errorf("expected ExpiredRecent server %d to remain during 7-day retention", srvRecent.ID)
	}

	_ = db.Delete(&reloadedRecent).Error
}

func TestScheduledImageBackupCronAndQueueExecution(t *testing.T) {
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

	host := domain.Host{
		Hostname:      fmt.Sprintf("imgcron-host-%d", time.Now().UnixNano()),
		WorkerBaseURL: "http://imgcron-host:9090",
		APIKey:        "k",
		APIKeyHash:    fmt.Sprintf("h-imgcron-%d", time.Now().UnixNano()),
		MaxServers:    20,
		Status:        domain.HostStatusActive,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("create host: %v", err)
	}

	user := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("imgcron-%d@example.com", time.Now().UnixNano()),
			PasswordHash: "hashed",
			Role:         domain.UserRoleAdmin,
		},
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	var plan domain.Plan
	_ = db.First(&plan)

	basePort := 26810
	db.Where("port >= ? AND port <= ?", basePort, basePort+10).Delete(&domain.Server{})
	db.Model(&domain.ServerJob{}).Where("status = ?", domain.JobStatusPending).Update("status", domain.JobStatusFailed)

	statuses := []domain.ServerStatus{
		domain.ServerStatusRunning,
		domain.ServerStatusStopped,
		domain.ServerStatusPending,
		domain.ServerStatusProvisioning,
		domain.ServerStatusMigrating,
		domain.ServerStatusError,
	}
	serversByStatus := make(map[domain.ServerStatus]domain.Server)
	for i, st := range statuses {
		srv := domain.Server{
			UserID:      user.ID,
			PlanID:      plan.ID,
			HostID:      host.ID,
			Name:        fmt.Sprintf("ImgCron-%s", st),
			Port:        basePort + i,
			Status:      st,
			ContainerID: fmt.Sprintf("ctr-%s", st),
		}
		if err := db.Create(&srv).Error; err != nil {
			t.Fatalf("create server (%s): %v", st, err)
		}
		serversByStatus[st] = srv
	}
	defer func() {
		for _, srv := range serversByStatus {
			db.Delete(&domain.Server{}, srv.ID)
		}
	}()

	tw := newQueueTestWorker()
	runningID := serversByStatus[domain.ServerStatusRunning].ID
	stoppedID := serversByStatus[domain.ServerStatusStopped].ID
	tw.volumes[fmt.Sprintf("%d", runningID)] = []byte("running-ext4-img-content")
	tw.volumes[fmt.Sprintf("%d", stoppedID)] = []byte("stopped-ext4-img-content")

	workerFactory := func(baseURL, apiKey string) nodemanagerclient.Client { return tw }
	hostingSvc := hosting.NewService(db, hosting.NewLeastLoadedScheduler(db), hosting.NewPortAllocator(db, 26850, 26890), workerFactory)
	memStore := storage.NewMemoryStorageProvider()
	defer func() { _ = memStore.Close() }()
	backupSvc := backup.NewService(db, memStore, workerFactory)
	jobSvc := NewService(db, hostingSvc, backupSvc)

	ctx := context.Background()

	enqueued, err := jobSvc.EnqueueScheduledImageBackups(ctx)
	if err != nil {
		t.Fatalf("EnqueueScheduledImageBackups failed: %v", err)
	}
	if enqueued < 2 {
		t.Fatalf("expected at least 2 enqueued image backup jobs, got %d", enqueued)
	}

	for _, st := range []domain.ServerStatus{domain.ServerStatusPending, domain.ServerStatusProvisioning, domain.ServerStatusMigrating, domain.ServerStatusError} {
		var cnt int64
		_ = db.Model(&domain.ServerJob{}).Where("server_id = ? AND type = ?", serversByStatus[st].ID, domain.JobTypeBackupImage).Count(&cnt)
		if cnt != 0 {
			t.Errorf("expected 0 image backup jobs for status %s, got %d", st, cnt)
		}
	}
	if _, dupJob, err := jobSvc.EnqueueImageBackup(ctx, 0, runningID); err != nil || dupJob != nil {
		t.Errorf("expected duplicate pending image backup to be skipped, got job=%+v err=%v", dupJob, err)
	}

	for i := 0; i < 50; i++ {
		processed, procErr := jobSvc.ProcessNext(ctx)
		if procErr != nil {
			t.Fatalf("ProcessNext failed: %v", procErr)
		}
		if !processed {
			break
		}
	}

	runBackups, err := backupSvc.ListImageBackups(ctx, runningID)
	if err != nil || len(runBackups) != 1 || runBackups[0].Status != domain.BackupStatusCompleted {
		t.Fatalf("expected 1 completed image backup for running server, got %+v (err=%v)", runBackups, err)
	}
	stopBackups, err := backupSvc.ListImageBackups(ctx, stoppedID)
	if err != nil || len(stopBackups) != 1 || stopBackups[0].Status != domain.BackupStatusCompleted {
		t.Fatalf("expected 1 completed image backup for stopped server, got %+v (err=%v)", stopBackups, err)
	}

	tw.volumes[fmt.Sprintf("%d", runningID)] = []byte("corrupted")
	restoreJob, err := jobSvc.EnqueueImageRestore(ctx, user.ID, runningID, runBackups[0].ID)
	if err != nil {
		t.Fatalf("EnqueueImageRestore failed: %v", err)
	}
	if restoreJob.Type != domain.JobTypeRestoreImage {
		t.Fatalf("expected job type %s, got %s", domain.JobTypeRestoreImage, restoreJob.Type)
	}
	processed, procErr := jobSvc.ProcessNext(ctx)
	if !processed || procErr != nil {
		t.Fatalf("restore_image ProcessNext failed: (%v, %v)", processed, procErr)
	}
	if got := string(tw.volumes[fmt.Sprintf("%d", runningID)]); got != "running-ext4-img-content" {
		t.Errorf("restored running server image = %q, want %q", got, "running-ext4-img-content")
	}

	if _, err := jobSvc.StartImageBackupCron(ctx, "invalid-cron"); err == nil {
		t.Error("expected error for invalid cron spec")
	}
	cronCtx, cancelCron := context.WithCancel(ctx)
	stopCron, err := jobSvc.StartImageBackupCron(cronCtx, "0 5 * * *")
	if err != nil {
		t.Fatalf("StartImageBackupCron failed: %v", err)
	}
	cancelCron()
	stopCron()
}
