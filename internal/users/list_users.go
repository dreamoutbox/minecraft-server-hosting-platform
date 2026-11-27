package users

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ListUsers renders the admin-only user role management page.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard/users
func (h *Handler) ListUsers(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "User role management is restricted to administrator accounts.") {
		return
	}

	var users []domain.User
	_ = h.db.WithContext(c.Request.Context()).Order("id asc").Find(&users).Error

	h.view.RenderAdminView(c, "users.html", map[string]interface{}{
		"Users": users,
		"Roles": []domain.UserRole{domain.UserRoleUser, domain.UserRoleStaff, domain.UserRoleAdmin},
	})
}
