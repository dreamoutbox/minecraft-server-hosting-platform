package mcservers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// SendConsoleCommand executes a Minecraft console command on the running server.
// Returns JSON when requested via fetch/AJAX, or redirects back to /console for plain form submissions.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form field 'command'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/console/command
func (h *Handler) SendConsoleCommand(c *gin.Context) {
	wantsJSON := strings.Contains(c.GetHeader("Accept"), "application/json") || c.GetHeader("X-Requested-With") == "XMLHttpRequest"

	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		if wantsJSON {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid server ID"})
			return
		}
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	command := c.PostForm("command")
	tail := c.PostForm("tail")
	_, err := h.hostingSvc.SendServerCommand(c.Request.Context(), userID, serverIDVal, command)
	if err != nil {
		if wantsJSON {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/console", "error", err.Error()))
		return
	}

	if wantsJSON {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	target := fmt.Sprintf("/dashboard/servers/%d/console?success=%s", serverIDVal, url.QueryEscape("Command sent"))
	if tail != "" {
		target += "&tail=" + url.QueryEscape(tail)
	}
	c.Redirect(http.StatusFound, target)
}
