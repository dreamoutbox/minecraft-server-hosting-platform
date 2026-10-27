package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// SetupWorkerRouter registers HTTP endpoints for the host worker daemon.
//
// Arguments:
//   - apiKey: Configured secret key for authenticating orchestrator requests.
//
// Example usage:
//   r := SetupWorkerRouter("secret-worker-key")
//   r.Run(":9090")
func SetupWorkerRouter(apiKey string) *gin.Engine {
	r := gin.Default()

	r.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ready",
		})
	})

	return r
}

func main() {
	workerPort := os.Getenv("WORKER_PORT")
	if workerPort == "" {
		workerPort = "9090"
	}

	apiKey := os.Getenv("WORKER_API_KEY")

	r := SetupWorkerRouter(apiKey)
	log.Printf("Worker daemon listening on port %s", workerPort)
	if err := r.Run(":" + workerPort); err != nil {
		log.Fatalf("worker daemon exited with error: %v", err)
	}
}
