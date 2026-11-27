package users

import (
	"github.com/dreamoutbox/go-authkit/session"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// Handler serves authentication and user management dashboard routes.
type Handler struct {
	db           *gorm.DB
	authsessions *session.Manager
	view         *view.Renderer
}

// NewHandler constructs a dashboard user/auth route handler.
//
// Arguments:
//   - db: Database handle for user queries and updates.
//   - authsessions: Session manager for cookie login and logout.
//   - renderer: Shared dashboard template renderer.
//
// Example usage:
//
//	usersHandler := users.NewHandler(db, authsessions, renderer)
func NewHandler(db *gorm.DB, authsessions *session.Manager, renderer *view.Renderer) *Handler {
	return &Handler{
		db:           db,
		authsessions: authsessions,
		view:         renderer,
	}
}

// RegisterPublicRoutes mounts unauthenticated login, registration, and logout routes.
//
// Arguments:
//   - r: Root Gin engine.
//
// Example usage:
//
//	usersHandler.RegisterPublicRoutes(r)
func (h *Handler) RegisterPublicRoutes(r *gin.Engine) {
	r.GET("/login", h.ShowLogin)
	r.POST("/login", h.ProcessLogin)
	r.GET("/register", h.ShowRegister)
	r.POST("/register", h.ProcessRegister)
	r.GET("/logout", h.Logout)
}

// RegisterDashboardRoutes mounts authenticated user role management routes under /dashboard.
//
// Arguments:
//   - dash: Authenticated /dashboard router group.
//
// Example usage:
//
//	usersHandler.RegisterDashboardRoutes(dash)
func (h *Handler) RegisterDashboardRoutes(dash *gin.RouterGroup) {
	dash.GET("/users", h.ListUsers)
	dash.POST("/users/:id/role", h.UpdateUserRole)
}
