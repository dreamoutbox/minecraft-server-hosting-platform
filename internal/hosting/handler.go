package hosting

import (
	"net/http"
	"strconv"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"
)

// Handler serves HTTP endpoints for server lifecycle and host management.
type Handler struct {
	service      *Service
	migrationSvc *MigrationService
}

// NewHandler instantiates a hosting HTTP controller.
//
// Arguments:
//   - service: Hosting domain service.
//
// Example usage:
//   h := hosting.NewHandler(svc)
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// SetMigrationService sets the migration coordinator for server transfer endpoints.
func (h *Handler) SetMigrationService(m *MigrationService) {
	h.migrationSvc = m
}

// MigrateServerRequest specifies optional destination host parameter.
type MigrateServerRequest struct {
	DestinationHostID uint `json:"destination_host_id"`
}

// RegisterHostRequest holds payload fields for registering a worker node.
type RegisterHostRequest struct {
	Hostname      string `json:"hostname" binding:"required"`
	WorkerBaseURL string `json:"worker_base_url" binding:"required,url"`
	MaxServers    int    `json:"max_servers" binding:"required,gt=0"`
}

// CreateServerRequest holds payload fields for creating a new Minecraft server.
type CreateServerRequest struct {
	Name   string `json:"name" binding:"required,min=3,max=64"`
	PlanID uint   `json:"plan_id" binding:"required"`
}

// RegisterHost enrolls a host node and yields its generated API key.
//
// Arguments:
//   - c: Active Gin request context containing RegisterHostRequest JSON.
//
// Example usage:
//   POST /api/v1/hosts
func (h *Handler) RegisterHost(c *gin.Context) {
	var req RegisterHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid host registration: " + err.Error()})
		return
	}

	host, apiKey, err := h.service.RegisterHost(c.Request.Context(), req.Hostname, req.WorkerBaseURL, req.MaxServers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"host":    host,
		"api_key": apiKey,
		"notice":  "Store this API key securely in WORKER_API_KEY on the worker daemon; it will not be shown again.",
	})
}

// ListHosts lists all registered hosting nodes.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//   GET /api/v1/hosts
func (h *Handler) ListHosts(c *gin.Context) {
	hosts, err := h.service.ListHosts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"hosts": hosts})
}

// RotateHostKey generates and applies a new worker API key for the target host node.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//   POST /api/v1/hosts/:id/rotate-key
func (h *Handler) RotateHostKey(c *gin.Context) {
	hostID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid host id"})
		return
	}

	newKey, err := h.service.RotateHostAPIKey(c.Request.Context(), hostID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"host_id": hostID,
		"api_key": newKey,
		"notice":  "Update WORKER_API_KEY in the host's worker daemon environment immediately.",
	})
}

// CreateServer provisions a new server on an available host.
//
// Arguments:
//   - c: Active Gin request context containing CreateServerRequest JSON.
//
// Example usage:
//   POST /api/v1/servers
func (h *Handler) CreateServer(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server request: " + err.Error()})
		return
	}

	srv, err := h.service.CreateServer(c.Request.Context(), userID, req.Name, req.PlanID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, srv)
}

// ListServers returns all servers belonging to the caller.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//   GET /api/v1/servers
func (h *Handler) ListServers(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	servers, err := h.service.ListServers(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"servers": servers})
}

// GetServer returns server details and live worker state.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//   GET /api/v1/servers/:id
func (h *Handler) GetServer(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	srv, status, err := h.service.GetServer(c.Request.Context(), userID, serverID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"server": srv,
		"status": status,
	})
}

// StartServer resumes a stopped server container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//   POST /api/v1/servers/:id/start
func (h *Handler) StartServer(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	srv, err := h.service.StartServer(c.Request.Context(), userID, serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, srv)
}

// StopServer halts a running server container.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//   POST /api/v1/servers/:id/stop
func (h *Handler) StopServer(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	srv, err := h.service.StopServer(c.Request.Context(), userID, serverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, srv)
}

// DeleteServer unmounts and removes a server.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id'.
//
// Example usage:
//   DELETE /api/v1/servers/:id
func (h *Handler) DeleteServer(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.service.DeleteServer(c.Request.Context(), userID, serverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "server deleted successfully"})
}

// MigrateServer moves the server to a different host node preserving its assigned port.
//
// Arguments:
//   - c: Active Gin request context with URL param 'id' and optional destination JSON.
//
// Example usage:
//   POST /api/v1/servers/:id/migrate
func (h *Handler) MigrateServer(c *gin.Context) {
	if h.migrationSvc == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "migration service not configured"})
		return
	}

	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	serverID, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var req MigrateServerRequest
	_ = c.ShouldBindJSON(&req)

	if err := h.migrationSvc.MigrateServer(c.Request.Context(), userID, serverID, req.DestinationHostID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "server migrated successfully"})
}

// parseIDParam extracts and converts a URL route parameter to an unsigned integer.
func parseIDParam(c *gin.Context, paramName string) (uint, error) {
	raw := c.Param(paramName)
	val, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(val), nil
}

// RegisterRoutes binds server and host management routes to Gin router groups.
//
// Arguments:
//   - r: Gin router engine.
//   - jwtSecret: Secret key to configure AuthMiddleware.
//
// Example usage:
//   hostingHandler.RegisterRoutes(router, cfg.JWTSecret)
func (h *Handler) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	api := r.Group("/api/v1")

	// Host endpoints (admin / cluster management)
	hosts := api.Group("/hosts")
	{
		hosts.POST("", h.RegisterHost)
		hosts.GET("", h.ListHosts)
		hosts.POST("/:id/rotate-key", h.RotateHostKey)
	}

	// Server endpoints (authenticated user self-service)
	servers := api.Group("/servers")
	servers.Use(authkit.AuthMiddleware(jwtSecret))
	{
		servers.POST("", h.CreateServer)
		servers.GET("", h.ListServers)
		servers.GET("/:id", h.GetServer)
		servers.POST("/:id/start", h.StartServer)
		servers.POST("/:id/stop", h.StopServer)
		servers.DELETE("/:id", h.DeleteServer)
		servers.POST("/:id/migrate", h.MigrateServer)
	}
}
