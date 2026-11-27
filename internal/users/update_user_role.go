package users

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// UpdateUserRole updates a user's platform role (user, staff, or admin).
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form field 'role'.
//
// Example usage:
//
//	POST /dashboard/users/:id/role
func (h *Handler) UpdateUserRole(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "User role management is restricted to administrator accounts.") {
		return
	}

	targetUserID, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/users?error=Invalid+user+ID")
		return
	}

	newRole := domain.UserRole(strings.TrimSpace(c.PostForm("role")))
	if !newRole.IsValid() {
		c.Redirect(http.StatusFound, "/dashboard/users?error=Invalid+role")
		return
	}

	res := h.db.WithContext(c.Request.Context()).Model(&domain.User{}).Where("id = ?", targetUserID).Update("role", newRole)
	if res.Error != nil {
		c.Redirect(http.StatusFound, "/dashboard/users?error="+url.QueryEscape(res.Error.Error()))
		return
	}
	if res.RowsAffected == 0 {
		c.Redirect(http.StatusFound, "/dashboard/users?error=User+not+found")
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/users?success=User+role+updated")
}
