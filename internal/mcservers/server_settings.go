package mcservers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowServerSettings renders the server settings page (server jar/version selector and Danger Zone).
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/settings
func (h *Handler) ShowServerSettings(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := h.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	isExpired, canExtend := view.ServerExpiryFlags(server, time.Now())
	h.view.RenderView(c, "server_settings.html", map[string]interface{}{
		"ActiveTab":     "settings",
		"Server":        server,
		"ServerTypes":   domain.AllServerTypes(),
		"IsExpired":     isExpired,
		"CanExtend":     canExtend,
		"RetentionDays": view.DefaultServerRetentionDays,
	})
}

// UpdateServerSettings persists user-chosen server jar type and version from the settings page.
//
// Arguments:
//   - c: Active Gin request context with form fields 'server_type' and 'server_version'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/settings
func (h *Handler) UpdateServerSettings(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	serverType := c.PostForm("server_type")
	serverVersion := c.PostForm("server_version")

	if _, err := h.hostingSvc.UpdateServerSettings(c.Request.Context(), userID, serverIDVal, serverType, serverVersion); err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/settings", "error", err.Error()))
		return
	}

	c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/settings", "success", "Settings saved! Start or restart the server to apply."))
}
