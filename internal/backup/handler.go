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
//   h := backup.NewHandler(svc)
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
//   POST /api/v1/servers/:id/backups
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
//   GET /api/v1/servers/:id/backups
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
//   POST /api/v1/servers/:id/backups/:backup_id/restore
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
//   - rg: Server-scoped router group (e.g. /api/v1/servers/:id/backups).
//   - jwtSecret: Secret key to configure AuthMiddleware.
//
// Example usage:
//   backupHandler.RegisterRoutes(router, cfg.JWTSecret)
func (h *Handler) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	backups := r.Group("/api/v1/servers/:id/backups")
	backups.Use(authkit.AuthMiddleware(jwtSecret))
	{
		backups.POST("", h.CreateBackup)
		backups.GET("", h.ListBackups)
		backups.POST("/:backup_id/restore", h.RestoreBackup)
	}
}
