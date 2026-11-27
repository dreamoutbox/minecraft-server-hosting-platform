package mcservers

import (
	"net/http"
	"strconv"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// TriggerMigration moves the server to a different host. Restricted to staff and admin roles.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form field 'destination_host_id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/migrate
func (h *Handler) TriggerMigration(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		h.view.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Host migration is restricted to staff and admin accounts.",
		})
		return
	}

	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}
	destHostID, _ := strconv.ParseUint(c.PostForm("destination_host_id"), 10, 64)

	err := h.migrationSvc.MigrateServer(c.Request.Context(), 0, serverIDVal, uint(destHostID))
	if err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/host", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/host", "success", "Server migrated successfully"))
}
