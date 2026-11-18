package worker

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"golang.org/x/sys/unix"
)

// Handler serves HTTP endpoints for the host worker daemon.
type Handler struct {
	containers ContainerManager
	volumes    VolumeManager
	maxServers int
	baseDir    string
}

// NewHandler constructs a worker HTTP controller with injected container and volume managers.
//
// Arguments:
//   - containers: Container manager implementation.
//   - volumes: Volume manager implementation.
//   - maxServers: Capacity ceiling for simultaneous servers hosted on this node.
//   - baseDir: Storage directory on node used for disk capacity statistics.
//
// Example usage:
//
//	h := worker.NewHandler(cm, vm, 50, "/var/lib/mc-platform")
func NewHandler(containers ContainerManager, volumes VolumeManager, maxServers int, baseDir string) *Handler {
	return &Handler{
		containers: containers,
		volumes:    volumes,
		maxServers: maxServers,
		baseDir:    baseDir,
	}
}

// CreateContainer handles container creation and launch.
//
// Arguments:
//   - c: Active Gin request context containing CreateContainerRequest JSON.
//
// Example usage:
//
//	POST /containers
func (h *Handler) CreateContainer(c *gin.Context) {
	var req CreateContainerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container request: " + err.Error()})
		return
	}

	if req.ServerID > 0 {
		volumeID := strconv.FormatUint(uint64(req.ServerID), 10)
		if err := h.volumes.EnsureMounted(c.Request.Context(), volumeID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	resp, err := h.containers.CreateAndStart(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// StopContainer stops an active container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	POST /containers/:id/stop
func (h *Handler) StopContainer(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing container id"})
		return
	}

	if err := h.containers.Stop(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "stopped", "container_id": id})
}

// DeleteContainer tears down and removes a container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	DELETE /containers/:id
func (h *Handler) DeleteContainer(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing container id"})
		return
	}

	if err := h.containers.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted", "container_id": id})
}

// GetContainerStatus reports running or stopped status of a container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	GET /containers/:id/status
func (h *Handler) GetContainerStatus(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing container id"})
		return
	}

	resp, err := h.containers.GetStatus(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetContainerLogs returns trailing console logs for a managed container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and optional query param 'tail'.
//
// Example usage:
//
//	GET /containers/:id/logs?tail=200
func (h *Handler) GetContainerLogs(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing container id"})
		return
	}

	tail := 200
	if rawTail := c.Query("tail"); rawTail != "" {
		if parsed, err := strconv.Atoi(rawTail); err == nil && parsed > 0 {
			tail = parsed
		}
	}

	resp, err := h.containers.GetLogs(c.Request.Context(), id, tail)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// ExecContainerCommand executes a Minecraft console command inside a running container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and ExecCommandRequest JSON body.
//
// Example usage:
//
//	POST /containers/:id/exec
func (h *Handler) ExecContainerCommand(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing container id"})
		return
	}

	var req ExecCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid command request: " + err.Error()})
		return
	}

	resp, err := h.containers.ExecCommand(c.Request.Context(), id, "1000", req.Command)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// CreateVolume allocates and mounts a storage volume.
//
// Arguments:
//   - c: Active Gin request context containing CreateVolumeRequest JSON.
//
// Example usage:
//
//	POST /volumes
func (h *Handler) CreateVolume(c *gin.Context) {
	var req CreateVolumeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid volume request: " + err.Error()})
		return
	}

	volumeID := strconv.FormatUint(uint64(req.ServerID), 10)
	mountPath, imagePath, err := h.volumes.CreateVolume(c.Request.Context(), volumeID, req.DiskLimitGB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, CreateVolumeResponse{
		MountPath: mountPath,
		ImagePath: imagePath,
	})
}

// DeleteVolume unmounts and removes volume data.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	DELETE /volumes/:id
func (h *Handler) DeleteVolume(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	if err := h.volumes.DeleteVolume(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted", "volume_id": id})
}

// ExportVolume streams an uncompressed tar archive of volume files.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	GET /volumes/:id/export
func (h *Handler) ExportVolume(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	c.Header("Content-Type", "application/x-tar")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"volume-%s.tar\"", id))

	if err := h.volumes.ExportVolume(c.Request.Context(), id, c.Writer); err != nil {
		if !c.Writer.Written() {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		log.Printf("export volume %s aborted mid-stream: %v", id, err)
		return
	}
}

// ImportVolume unpacks an incoming tar stream into the volume.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and tar stream body.
//
// Example usage:
//
//	POST /volumes/:id/import
func (h *Handler) ImportVolume(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	if err := h.volumes.ImportVolume(c.Request.Context(), id, c.Request.Body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "imported", "volume_id": id})
}

// GetStatus reports host health, active container count, and disk capacity.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /status
func (h *Handler) GetStatus(c *gin.Context) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}

	activeCount, err := h.containers.CountActive(c.Request.Context())
	if err != nil {
		activeCount = 0
	}

	freeBytes, totalBytes, err := getDiskCapacity(h.baseDir)
	if err != nil {
		freeBytes = 0
		totalBytes = 0
	}

	c.JSON(http.StatusOK, HostStatusResponse{
		Hostname:         hostname,
		ActiveContainers: activeCount,
		MaxServers:       h.maxServers,
		FreeDiskBytes:    freeBytes,
		TotalDiskBytes:   totalBytes,
	})
}

// getDiskCapacity determines available and total bytes for the given filesystem path.
//
// Arguments:
//   - path: Local filesystem path to inspect.
//
// Example usage:
//
//	free, total, err := getDiskCapacity("/var/lib/mc-platform")
func getDiskCapacity(path string) (uint64, uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	totalBytes := stat.Blocks * uint64(stat.Bsize)
	return freeBytes, totalBytes, nil
}

// RegisterRoutes binds worker endpoints to the Gin router with API key protection.
//
// Arguments:
//   - r: Gin engine instance.
//   - apiKey: Configured worker secret key.
//
// Example usage:
//
//	handler.RegisterRoutes(router, "secret-key")
func (h *Handler) RegisterRoutes(r *gin.Engine, apiKey string) {
	authorized := r.Group("")
	authorized.Use(APIKeyMiddleware(apiKey))
	{
		authorized.GET("/status", h.GetStatus)
		authorized.POST("/containers", h.CreateContainer)
		authorized.POST("/containers/:id/stop", h.StopContainer)
		authorized.DELETE("/containers/:id", h.DeleteContainer)
		authorized.GET("/containers/:id/status", h.GetContainerStatus)
		authorized.GET("/containers/:id/logs", h.GetContainerLogs)
		authorized.POST("/containers/:id/exec", h.ExecContainerCommand)

		authorized.POST("/volumes", h.CreateVolume)
		authorized.DELETE("/volumes/:id", h.DeleteVolume)
		authorized.GET("/volumes/:id/export", h.ExportVolume)
		authorized.POST("/volumes/:id/import", h.ImportVolume)
	}
}
