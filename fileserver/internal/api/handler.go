package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/authkit"
	"github.com/dreamoutbox/fileserver/client"
	"github.com/dreamoutbox/fileserver/internal/store"
)

// Handler serves the node-local management HTTP API for user provisioning,
// directory permissions, and short-lived login token issuance.
type Handler struct {
	store         *store.Store
	jwtSecret     string
	loginTokenTTL time.Duration
}

// NewHandler constructs a management API handler.
//
// Arguments:
//   - st: Fileserver SQLite store.
//   - jwtSecret: Secret key used to sign login tokens.
//   - loginTokenTTL: Lifetime of issued single-use login tokens.
//
// Example usage:
//
//	h := api.NewHandler(st, cfg.JWTSecret, cfg.LoginTokenTTL)
func NewHandler(st *store.Store, jwtSecret string, loginTokenTTL time.Duration) *Handler {
	return &Handler{
		store:         st,
		jwtSecret:     jwtSecret,
		loginTokenTTL: loginTokenTTL,
	}
}

// RegisterUser idempotently creates or retrieves a fileserver user record.
//
// Arguments:
//   - c: Active Gin request context containing client.EnsureUserRequest JSON.
//
// Example usage:
//
//	PUT /api/v1/users
func (h *Handler) RegisterUser(c *gin.Context) {
	var req client.EnsureUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user payload: " + err.Error()})
		return
	}

	user, created, err := h.store.EnsureUser(c.Request.Context(), req.ID, req.Email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, client.EnsureUserResponse{
		ID:      user.ID,
		Email:   user.Email,
		Created: created,
	})
}

// AllowPermission grants a user permission to manage a server mount directory.
//
// Arguments:
//   - c: Active Gin request context containing client.PermissionRequest JSON.
//
// Example usage:
//
//	PUT /api/v1/permissions
func (h *Handler) AllowPermission(c *gin.Context) {
	var req client.PermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid permission payload: " + err.Error()})
		return
	}

	perm, err := h.store.Allow(c.Request.Context(), req.UserID, req.ServerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, client.PermissionResponse{
		UserID:   perm.UserID,
		ServerID: perm.ServerID,
		DirPath:  perm.DirPath,
		Allowed:  true,
	})
}

// DisallowPermission revokes one user's permission for a server mount directory.
//
// Arguments:
//   - c: Active Gin request context containing client.PermissionRequest JSON.
//
// Example usage:
//
//	DELETE /api/v1/permissions
func (h *Handler) DisallowPermission(c *gin.Context) {
	var req client.PermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid permission payload: " + err.Error()})
		return
	}

	if err := h.store.Disallow(c.Request.Context(), req.UserID, req.ServerID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, client.PermissionResponse{
		UserID:   req.UserID,
		ServerID: req.ServerID,
		DirPath:  h.store.ServerDirPath(req.ServerID),
		Allowed:  false,
	})
}

// RevokeServerPermissions removes all directory grants for a deleted server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'server_id'.
//
// Example usage:
//
//	DELETE /api/v1/servers/:server_id/permissions
func (h *Handler) RevokeServerPermissions(c *gin.Context) {
	serverID, err := parseUintParam(c, "server_id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server_id"})
		return
	}

	if err := h.store.RevokeServer(c.Request.Context(), serverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"revoked": true, "server_id": serverID})
}

// CreateLoginToken mints a short-lived single-use login JWT for the specified user.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /api/v1/users/:id/login-token
func (h *Handler) CreateLoginToken(c *gin.Context) {
	userID, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	user, err := h.store.GetUser(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	tokenStr, expiresAt, _, err := authkit.GenerateLoginToken(user.ID, user.Email, user.Role, h.jwtSecret, h.loginTokenTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating login token"})
		return
	}

	c.JSON(http.StatusOK, client.LoginTokenResponse{
		Token:     tokenStr,
		ExpiresAt: expiresAt,
	})
}

// RegisterRoutes mounts the management endpoints under /api/v1 protected by APIKeyMiddleware.
//
// Arguments:
//   - r: Gin router engine.
//   - apiKey: Expected Bearer API key.
//
// Example usage:
//
//	apiHandler.RegisterRoutes(router, cfg.APIKey)
func (h *Handler) RegisterRoutes(r *gin.Engine, apiKey string) {
	v1 := r.Group("/api/v1")
	v1.Use(APIKeyMiddleware(apiKey))
	{
		v1.PUT("/users", h.RegisterUser)
		v1.PUT("/permissions", h.AllowPermission)
		v1.DELETE("/permissions", h.DisallowPermission)
		v1.DELETE("/servers/:server_id/permissions", h.RevokeServerPermissions)
		v1.POST("/users/:id/login-token", h.CreateLoginToken)
	}
}
