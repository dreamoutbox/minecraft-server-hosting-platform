package mcservers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// ShowServerDetail renders the server overview page.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id
func (h *Handler) ShowServerDetail(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, liveStatus, err := h.hostingSvc.GetServer(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	now := time.Now()
	isExpired, canExtend := view.ServerExpiryFlags(server, now)
	h.view.RenderView(c, "server_detail.html", map[string]interface{}{
		"ActiveTab":      "overview",
		"Server":         server,
		"LiveStatus":     liveStatus,
		"IsExpired":      isExpired,
		"IsExpiringSoon": server.IsExpiringSoon(now, view.ServerExpiringSoonWindow),
		"CanExtend":      canExtend,
		"RetentionDays":  view.DefaultServerRetentionDays,
	})
}
