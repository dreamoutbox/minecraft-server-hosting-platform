package jobs

import (
	"net/http"
	"strconv"

	authmw "github.com/dreamoutbox/go-authkit/middleware"
	jobworkerjobs "github.com/dreamoutbox/minecraft-server-hosting-platform/jobworker/jobs"
	"github.com/gin-gonic/gin"
)

// Handler exposes HTTP API endpoints to inspect queued jobs and manually retry failed jobs.
type Handler struct {
	service *jobworkerjobs.Service
}

// NewHandler constructs a job queue HTTP controller.
//
// Arguments:
//   - service: Active async job queue service.
//
// Example usage:
//
//	jobHandler := jobs.NewHandler(jobSvc)
func NewHandler(service *jobworkerjobs.Service) *Handler {
	return &Handler{service: service}
}

// GetJob returns status and error metadata for a single queued job.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	GET /api/v1/jobs/:id
func (h *Handler) GetJob(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	jobID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || jobID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job id"})
		return
	}

	isStaff := authmw.GetUserRoleFromContext(c).IsStaffOrAdmin()
	job, err := h.service.GetJob(c.Request.Context(), userID, isStaff, uint(jobID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, job)
}

// RetryJob re-queues a failed job for immediate re-execution.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//
//	POST /api/v1/jobs/:id/retry
func (h *Handler) RetryJob(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	jobID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || jobID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job id"})
		return
	}

	isStaff := authmw.GetUserRoleFromContext(c).IsStaffOrAdmin()
	job, err := h.service.RetryJob(c.Request.Context(), userID, isStaff, uint(jobID))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"job":     job,
		"message": "job re-queued",
	})
}

// RegisterRoutes mounts job inspection and retry routes behind Bearer JWT auth.
//
// Arguments:
//   - r: Gin engine.
//   - jwtSecret: HMAC secret for AuthMiddleware.
//
// Example usage:
//
//	jobHandler.RegisterRoutes(router, cfg.JWTSecret)
func (h *Handler) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	group := r.Group("/api/v1/jobs")
	group.Use(authmw.AuthMiddleware(jwtSecret))
	{
		group.GET("/:id", h.GetJob)
		group.POST("/:id/retry", h.RetryJob)
	}
}
