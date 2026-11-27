package mcservers

import (
	"net/http"
	"strconv"
	"strings"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowCreateServer displays plan selection and direct server creation form (staff/admin only).
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard/servers/new
func (h *Handler) ShowCreateServer(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		h.view.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Direct server creation is restricted to staff and admin accounts. Please purchase a server via Checkout.",
		})
		return
	}

	var plans []domain.Plan
	_ = h.db.Find(&plans)

	h.view.RenderView(c, "server_create.html", map[string]interface{}{
		"Plans": plans,
	})
}

// ProcessCreateServer queues a new Minecraft game server directly (staff/admin only).
//
// Arguments:
//   - c: Active Gin request context with form fields name and plan_id.
//
// Example usage:
//
//	POST /dashboard/servers/new
func (h *Handler) ProcessCreateServer(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		h.view.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Direct server creation is restricted to staff and admin accounts.",
		})
		return
	}

	userID, _ := authmw.GetUserIDFromContext(c)
	name := strings.TrimSpace(c.PostForm("name"))
	planIDVal, _ := strconv.ParseUint(c.PostForm("plan_id"), 10, 64)

	server, _, err := h.jobSvc.EnqueueCreateServer(
		c.Request.Context(),
		userID,
		name,
		uint(planIDVal),
		domain.ServerTypeVanilla,
		domain.DefaultServerVersion,
	)
	if err != nil {
		var plans []domain.Plan
		_ = h.db.Find(&plans)
		h.view.RenderView(c, "server_create.html", map[string]interface{}{
			"Plans":      plans,
			"FlashError": "Failed queueing server creation: " + err.Error(),
		})
		return
	}

	c.Redirect(http.StatusFound, view.ServerRedirectURL(server.ID, "", "success", "Server creation queued!"))
}
