package mcservers

import (
	"net/http"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// ShowServerHost renders Host Worker Status and Host Migration panels exclusively to staff and admin users.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/host
func (h *Handler) ShowServerHost(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		h.view.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Host worker status and migration are only available to staff and admin users.",
		})
		return
	}

	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, liveStatus, err := h.hostingSvc.GetServer(c.Request.Context(), 0, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	hosts, _ := h.hostingSvc.ListHosts(c.Request.Context())
	h.view.RenderView(c, "server_host.html", map[string]interface{}{
		"ActiveTab":      "host",
		"Server":         server,
		"LiveStatus":     liveStatus,
		"AvailableHosts": hosts,
	})
}
