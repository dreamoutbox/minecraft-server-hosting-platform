package main

import (
	"log"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// SetupWorkerRouter registers HTTP endpoints for the host worker daemon.
//
// Arguments:
//   - apiKey: Configured secret key for authenticating orchestrator requests.
//   - handler: Worker HTTP controller managing containers and volumes.
//
// Example usage:
//   r := SetupWorkerRouter("secret-worker-key", handler)
//   r.Run(":9090")
func SetupWorkerRouter(apiKey string, handler *worker.Handler) *gin.Engine {
	r := gin.Default()
	handler.RegisterRoutes(r, apiKey)
	return r
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

	cm, err := worker.NewDockerContainerManager()
	if err != nil {
		log.Fatalf("failed initializing docker container manager: %v", err)
	}

	vm := worker.NewLocalVolumeManager(baseDir, useLoopMount)
	handler := worker.NewHandler(cm, vm, maxServers, baseDir)

	r := SetupWorkerRouter(apiKey, handler)
	log.Printf("Worker daemon listening on port %s", workerPort)
	if err := r.Run(":" + workerPort); err != nil {
		log.Fatalf("worker daemon exited with error: %v", err)
	}
}
