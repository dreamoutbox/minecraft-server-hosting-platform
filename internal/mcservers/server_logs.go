package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowServerLogs renders PostgreSQL operation logs for a server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/logs
func (h *Handler) ShowServerLogs(c *gin.Context) {
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

	logs, _ := domain.ListServerLogs(c.Request.Context(), h.db, server.ID, 50)
	h.view.RenderView(c, "server_logs.html", map[string]interface{}{
		"ActiveTab": "logs",
		"Server":    server,
		"Logs":      logs,
	})
}
