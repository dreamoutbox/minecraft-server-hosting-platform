package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/nodemanager"
)

const (
	httpShutdownTimeout  = 5 * time.Second
	workerCleanupTimeout = 45 * time.Second
)

// SetupWorkerRouter registers HTTP endpoints for the host worker daemon.
//
// Arguments:
//   - apiKey: Configured secret key for authenticating orchestrator requests.
//   - handler: Worker HTTP controller managing containers and volumes.
//
// Example usage:
//
//	r := SetupWorkerRouter("secret-worker-key", handler)
//	r.Run(":9090")
func SetupWorkerRouter(apiKey string, handler *nodemanager.Handler) *gin.Engine {
	r := gin.Default()
	handler.RegisterRoutes(r, apiKey)
	return r
}

// shutdownWorker stops the HTTP server, removes managed containers, and unmounts ext4 volumes.
//
// Arguments:
//   - srv: Active worker HTTP server instance (may be nil in unit tests).
//   - cm: Container manager used to stop and remove managed server containers.
//   - vm: Volume manager used to unmount active ext4 loop volumes.
//
// Example usage:
//
//	shutdownWorker(srv, cm, vm)
func shutdownWorker(srv *http.Server, cm nodemanager.ContainerManager, vm nodemanager.VolumeManager) {
	if srv != nil {
		httpCtx, cancelHTTP := context.WithTimeout(context.Background(), httpShutdownTimeout)
		if err := srv.Shutdown(httpCtx); err != nil {
			log.Printf("[WARN] worker HTTP shutdown error: %v", err)
		}
		cancelHTTP()
	}

	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), workerCleanupTimeout)
	defer cancelCleanup()

	// Remove containers before unmounting so bind-mounted directories are not busy.
	if err := cm.CleanupAll(cleanupCtx); err != nil {
		log.Printf("[WARN] worker container cleanup error: %v", err)
	}
	if err := vm.UnmountAll(cleanupCtx); err != nil {
		log.Printf("[WARN] worker volume unmount error: %v", err)
	}
}

func main() {
	workerPort := os.Getenv("WORKER_PORT")
	if workerPort == "" {
		workerPort = "9090"
	}

	apiKey := os.Getenv("WORKER_API_KEY")
	if apiKey == "" {
		log.Println("[WARN] WORKER_API_KEY is unset; defaulting to 'dev-worker-key'")
		apiKey = "dev-worker-key"
	}

	baseDir := os.Getenv("WORKER_BASE_DIR")
	if baseDir == "" {
		baseDir = "./data/worker"
	}

	maxServers := 20
	if maxStr := os.Getenv("WORKER_MAX_SERVERS"); maxStr != "" {
		if val, err := strconv.Atoi(maxStr); err == nil && val > 0 {
			maxServers = val
		}
	}

	useLoopMount := os.Getenv("WORKER_LOOP_DEVICE_ENABLED") == "true"

	cm, err := nodemanager.NewDockerContainerManager()
	if err != nil {
		log.Fatalf("failed initializing docker container manager: %v", err)
	}

	vm := nodemanager.NewLocalVolumeManager(baseDir, useLoopMount)
	handler := nodemanager.NewHandler(cm, vm, maxServers, baseDir)

	r := SetupWorkerRouter(apiKey, handler)
	srv := &http.Server{
		Addr:    ":" + workerPort,
		Handler: r,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErrCh := make(chan error, 1)
	go func() {
		log.Printf("Worker daemon listening on port %s", workerPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
		close(serveErrCh)
	}()

	select {
	case serveErr := <-serveErrCh:
		if serveErr != nil {
			log.Fatalf("worker daemon exited with error: %v", serveErr)
		}
	case <-sigCtx.Done():
		log.Println("Shutdown signal received; stopping managed containers and unmounting volumes...")
		shutdownWorker(srv, cm, vm)
		log.Println("Worker shutdown complete.")
	}
}
