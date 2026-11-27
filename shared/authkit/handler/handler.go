package handler

import (
	"time"

	"github.com/dreamoutbox/authkit/authkitjwt"
	"github.com/dreamoutbox/authkit/middleware"
	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Handler provides HTTP controller methods for user authentication and profile management.
type Handler struct {
	db        *gorm.DB
	jwtSecret string
	accessTTL time.Duration
}

// NewHandler initializes a new authentication handler.
//
// Arguments:
//   - db: Database connection for user queries and updates.
//   - jwtSecret: Secret key used to sign and verify JWT tokens.
//
// Example usage:
//
//	authHandler := handler.NewHandler(db, cfg.JWTSecret)
func NewHandler(db *gorm.DB, jwtSecret string) *Handler {
	return &Handler{
		db:        db,
		jwtSecret: jwtSecret,
	}
}

// SetAccessTokenTTL overrides the access JWT lifetime used by this handler.
// When unset (or <= 0), authkitjwt.GenerateTokenPair reads JWT_ACCESS_TTL or falls back to DefaultAccessTokenTTL.
//
// Arguments:
//   - ttl: Desired access token lifetime duration.
//
// Example usage:
//
//	authHandler.SetAccessTokenTTL(cfg.JWTAccessTTL)
func (h *Handler) SetAccessTokenTTL(ttl time.Duration) {
	h.accessTTL = ttl
}

func (h *Handler) generateTokens(user *shared.User) (*authkitjwt.TokenPair, error) {
	if h.accessTTL > 0 {
		return authkitjwt.GenerateTokenPairWithTTL(user.ID, user.Email, user.Role, h.jwtSecret, h.accessTTL)
	}
	return authkitjwt.GenerateTokenPair(user.ID, user.Email, user.Role, h.jwtSecret)
}

// UserResponse sanitizes user details for client responses.
type UserResponse struct {
	ID        uint            `json:"id"`
	Email     string          `json:"email"`
	Role      shared.UserRole `json:"role"`
	CreatedAt string          `json:"created_at"`
}

// sanitizeUser converts shared.User into client-safe UserResponse.
func sanitizeUser(user *shared.User) UserResponse {
	return UserResponse{
		ID:        user.ID,
		Email:     user.Email,
		Role:      user.Role,
		CreatedAt: user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// RegisterRoutes binds authentication and self-service profile endpoints onto the router.
//
// Arguments:
//   - rg: Target router group (e.g. /api/v1/auth).
//
// Example usage:
//
//	authHandler.RegisterRoutes(router.Group("/api/v1/auth"))
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/register", h.Register)
	rg.POST("/login", h.Login)
	rg.POST("/refresh", h.Refresh)
	rg.POST("/logout", h.Logout)

	protected := rg.Group("")
	protected.Use(middleware.AuthMiddleware(h.jwtSecret))
	{
		protected.GET("/profile", h.GetProfile)
		protected.PUT("/profile", h.UpdateProfile)
	}
}
