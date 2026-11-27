package mcservers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowServerConsole renders the live container console logs and RCON command form for a server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and optional query param 'tail'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/console?tail=200
func (h *Handler) ShowServerConsole(c *gin.Context) {
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

	tail := 200
	if rawTail := c.Query("tail"); rawTail != "" {
		if parsed, convErr := strconv.Atoi(rawTail); convErr == nil && parsed > 0 {
			tail = parsed
		}
	}

	var consoleLogs string
	var logsErr string
	if server.Status == domain.ServerStatusRunning && server.ContainerID != "" {
		logsResp, fetchErr := h.hostingSvc.GetServerConsoleLogs(c.Request.Context(), userID, server.ID, tail)
		if fetchErr != nil {
			logsErr = fetchErr.Error()
		} else if logsResp != nil {
			consoleLogs = logsResp.Logs
		}
	}

	h.view.RenderView(c, "server_console.html", map[string]interface{}{
		"ActiveTab":   "console",
		"Server":      server,
		"ConsoleLogs": consoleLogs,
		"LogsError":   logsErr,
		"Tail":        tail,
	})
}
