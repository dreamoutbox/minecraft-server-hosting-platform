package backup

import (
	"context"
	"net/http"
	"strconv"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

// BackupJobQueue enqueues asynchronous server backup and restore jobs.
type BackupJobQueue interface {
	EnqueueBackup(ctx context.Context, userID uint, serverID uint) (*domain.BackupRecord, *domain.ServerJob, error)
	EnqueueRestore(ctx context.Context, userID uint, serverID uint, backupID uint) (*domain.ServerJob, error)
	EnqueueImageBackup(ctx context.Context, userID uint, serverID uint) (*domain.ServerImageBackup, *domain.ServerJob, error)
	EnqueueAllImageBackups(ctx context.Context) ([]*domain.ServerJob, error)
	EnqueueImageRestore(ctx context.Context, userID uint, serverID uint, imageBackupID uint) (*domain.ServerJob, error)
}

// Handler serves HTTP endpoints for backup generation, listing, and restoration.
type Handler struct {
	service  *Service
	jobQueue BackupJobQueue
}

// NewHandler instantiates a backup HTTP controller.
//
// Arguments:
//   - service: Domain backup service.
//
// Example usage:
//
//	h := backup.NewHandler(svc)
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// SetJobQueue attaches an async queue used for backup and restore requests.
//
// Arguments:
//   - q: Implementation of BackupJobQueue.
//
// Example usage:
//
//	backupHandler.SetJobQueue(jobSvc)
func (h *Handler) SetJobQueue(q BackupJobQueue) {
	h.jobQueue = q
}

// CreateBackup triggers an on-demand snapshot of the server's world storage.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	POST /api/v1/servers/:id/backups
func (h *Handler) CreateBackup(c *gin.Context) {
	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}

	if h.jobQueue != nil {
		userID, _ := authkit.GetUserIDFromContext(c)
		record, job, err := h.jobQueue.EnqueueBackup(c.Request.Context(), userID, serverID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"backup":  record,
			"job":     job,
			"message": "backup queued",
		})
		return
	}

	record, err := h.service.BackupServer(c.Request.Context(), serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, record)
}

// ListBackups enumerates all recorded backups for the specified server.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	GET /api/v1/servers/:id/backups
func (h *Handler) ListBackups(c *gin.Context) {
	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}

	records, err := h.service.ListBackups(c.Request.Context(), serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"backups": records})
}

// RestoreBackup reverts server storage to the state recorded in the specified backup snapshot.
//
// Arguments:
//   - c: Active Gin request context with URL params 'id' and 'backup_id'.
//
// Example usage:
//
//	POST /api/v1/servers/:id/backups/:backup_id/restore
func (h *Handler) RestoreBackup(c *gin.Context) {
	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}

	backupID, err := parseIDParam(c, "backup_id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backup id"})
		return
	}

	if h.jobQueue != nil {
		userID, _ := authkit.GetUserIDFromContext(c)
		job, err := h.jobQueue.EnqueueRestore(c.Request.Context(), userID, serverID, backupID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"job":     job,
			"message": "restore queued",
		})
		return
	}

	if err := h.service.RestoreServer(c.Request.Context(), serverID, backupID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "server restored successfully from backup"})
}

// ListImageBackups returns recorded ext4 image backups for the specified server (admin-only).
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	GET /api/v1/admin/servers/:id/image-backups
func (h *Handler) ListImageBackups(c *gin.Context) {
	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}

	records, err := h.service.ListImageBackups(c.Request.Context(), serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"image_backups": records})
}

// CreateImageBackup triggers an ext4 disk image backup for the specified server (admin-only).
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	POST /api/v1/admin/servers/:id/image-backups
func (h *Handler) CreateImageBackup(c *gin.Context) {
	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}

	userID, _ := authkit.GetUserIDFromContext(c)
	if h.jobQueue != nil {
		record, job, err := h.jobQueue.EnqueueImageBackup(c.Request.Context(), userID, serverID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if job == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "image backup is already queued or running for this server"})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"image_backup": record,
			"job":          job,
			"message":      "image backup queued",
		})
		return
	}

	pending, err := h.service.CreatePendingImageBackup(c.Request.Context(), serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	record, err := h.service.ExecuteImageBackupRecord(c.Request.Context(), userID, serverID, pending.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, record)
}

// CreateAllImageBackups enqueues immediate ext4 disk image backups for all provisioned (running/stopped) servers (admin-only).
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	POST /api/v1/admin/image-backups
func (h *Handler) CreateAllImageBackups(c *gin.Context) {
	if h.jobQueue != nil {
		queuedJobs, err := h.jobQueue.EnqueueAllImageBackups(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if queuedJobs == nil {
			queuedJobs = []*domain.ServerJob{}
		}
		c.JSON(http.StatusAccepted, gin.H{
			"jobs":     queuedJobs,
			"enqueued": len(queuedJobs),
			"message":  "image backups queued",
		})
		return
	}

	userID, _ := authkit.GetUserIDFromContext(c)
	records, err := h.service.BackupAllServerImages(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"image_backups": records,
		"count":         len(records),
	})
}

// RestoreImageBackup restores a server's ext4 disk image from a completed ServerImageBackup (admin-only).
//
// Arguments:
//   - c: Active Gin request context with URL params 'id' and 'backup_id'.
//
// Example usage:
//
//	POST /api/v1/admin/servers/:id/image-backups/:backup_id/restore
func (h *Handler) RestoreImageBackup(c *gin.Context) {
	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}

	imageBackupID, err := parseIDParam(c, "backup_id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image backup id"})
		return
	}

	if h.jobQueue != nil {
		userID, _ := authkit.GetUserIDFromContext(c)
		job, err := h.jobQueue.EnqueueImageRestore(c.Request.Context(), userID, serverID, imageBackupID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"job":     job,
			"message": "image restore queued",
		})
		return
	}

	if err := h.service.RestoreImageBackup(c.Request.Context(), serverID, imageBackupID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "server ext4 image restored successfully"})
}

func requireAdminRole() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authkit.GetUserRoleFromContext(c).IsAdmin() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin role required"})
			return
		}
		c.Next()
	}
}

// parseIDParam extracts an unsigned integer from named URL route parameter.
func parseIDParam(c *gin.Context, paramName string) (uint, error) {
	raw := c.Param(paramName)
	val, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(val), nil
}

// RegisterRoutes binds backup management endpoints onto the router group.
//
// Arguments:
//   - r: Gin engine instance.
//   - jwtSecret: Secret key to configure AuthMiddleware.
//
// Example usage:
//
//	backupHandler.RegisterRoutes(router, cfg.JWTSecret)
func (h *Handler) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	backups := r.Group("/api/v1/servers/:id/backups")
	backups.Use(authkit.AuthMiddleware(jwtSecret))
	{
		backups.POST("", h.CreateBackup)
		backups.GET("", h.ListBackups)
		backups.POST("/:backup_id/restore", h.RestoreBackup)
	}

	adminAllImageBackups := r.Group("/api/v1/admin/image-backups")
	adminAllImageBackups.Use(authkit.AuthMiddleware(jwtSecret), requireAdminRole())
	{
		adminAllImageBackups.POST("", h.CreateAllImageBackups)
	}

	adminImageBackups := r.Group("/api/v1/admin/servers/:id/image-backups")
	adminImageBackups.Use(authkit.AuthMiddleware(jwtSecret), requireAdminRole())
	{
		adminImageBackups.GET("", h.ListImageBackups)
		adminImageBackups.POST("", h.CreateImageBackup)
		adminImageBackups.POST("/:backup_id/restore", h.RestoreImageBackup)
	}
}
