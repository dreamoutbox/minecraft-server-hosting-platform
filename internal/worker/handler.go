package worker

import (
	"context"
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

// UploadVolumeBackup compresses volume files to tar.gz and uploads to the provided presigned S3 PUT URL.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and UploadVolumeBackupRequest JSON body.
//
// Example usage:
//
//	POST /volumes/:id/backup-upload
func (h *Handler) UploadVolumeBackup(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	var req UploadVolumeBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backup upload request: " + err.Error()})
		return
	}

	sizeBytes, err := h.volumes.UploadVolumeBackup(c.Request.Context(), id, req.PresignedURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, UploadVolumeBackupResponse{SizeBytes: sizeBytes})
}

// RestoreVolumeBackup downloads a backup archive from the provided presigned S3 GET URL and unpacks it.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and RestoreVolumeBackupRequest JSON body.
//
// Example usage:
//
//	POST /volumes/:id/backup-restore
func (h *Handler) RestoreVolumeBackup(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	var req RestoreVolumeBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backup restore request: " + err.Error()})
		return
	}

	if err := h.volumes.RestoreVolumeBackup(c.Request.Context(), id, req.PresignedURL); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "restored", "volume_id": id})
}

// UploadImageBackup quiesces a running server (save-off, save-all, sync), tar.gz-splits its ext4
// .img file into <= 1 GiB parts, and uploads each part to the provided presigned S3 PUT URLs.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and UploadImageBackupRequest JSON body.
//
// Example usage:
//
//	POST /volumes/:id/image-backup
func (h *Handler) UploadImageBackup(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	var req UploadImageBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image backup request: " + err.Error()})
		return
	}

	ctx := c.Request.Context()
	if req.ContainerID != "" {
		// Pause world saves and flush pending chunks before reading the live ext4 image.
		if _, err := h.containers.ExecCommand(ctx, req.ContainerID, "1000", "save-off"); err != nil {
			log.Printf("[WARN] save-off failed for container %s before image backup: %v", req.ContainerID, err)
		} else {
			defer func() {
				if _, err := h.containers.ExecCommand(context.Background(), req.ContainerID, "1000", "save-on"); err != nil {
					log.Printf("[WARN] save-on failed for container %s after image backup: %v", req.ContainerID, err)
				}
			}()
		}
		if _, err := h.containers.ExecCommand(ctx, req.ContainerID, "1000", "save-all"); err != nil {
			log.Printf("[WARN] save-all failed for container %s before image backup: %v", req.ContainerID, err)
		}
	}
	unix.Sync()

	res, err := h.volumes.UploadImageBackup(ctx, id, req.PresignedURLs, req.MaxPartBytes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, res)
}

// RestoreImageBackup downloads ordered tar.gz parts from presigned GET URLs, merges and extracts
// them, and atomically replaces the server's ext4 .img file.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and RestoreImageBackupRequest JSON body.
//
// Example usage:
//
//	POST /volumes/:id/image-restore
func (h *Handler) RestoreImageBackup(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing volume id"})
		return
	}

	var req RestoreImageBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image restore request: " + err.Error()})
		return
	}

	sizeBytes, err := h.volumes.RestoreImageBackup(c.Request.Context(), id, req.PresignedURLs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, RestoreImageBackupResponse{SizeBytes: sizeBytes})
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
		authorized.POST("/volumes/:id/backup-upload", h.UploadVolumeBackup)
		authorized.POST("/volumes/:id/backup-restore", h.RestoreVolumeBackup)
		authorized.POST("/volumes/:id/image-backup", h.UploadImageBackup)
		authorized.POST("/volumes/:id/image-restore", h.RestoreImageBackup)
	}
}
