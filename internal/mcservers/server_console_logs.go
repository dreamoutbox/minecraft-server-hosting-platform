package mcservers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// GetServerConsoleLogsJSON returns trailing container logs as JSON for live polling in the web console.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and optional query param 'tail'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/console/logs?tail=200
func (h *Handler) GetServerConsoleLogsJSON(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid server ID"})
		return
	}

	server, _, err := h.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Server not found"})
		return
	}

	if server.Status != domain.ServerStatusRunning || server.ContainerID == "" {
		c.JSON(http.StatusOK, gin.H{
			"status": server.Status,
			"logs":   "",
		})
		return
	}

	tail := 200
	if rawTail := c.Query("tail"); rawTail != "" {
		if parsed, convErr := strconv.Atoi(rawTail); convErr == nil && parsed > 0 {
			tail = parsed
		}
	}

	logsResp, fetchErr := h.hostingSvc.GetServerConsoleLogs(c.Request.Context(), userID, server.ID, tail)
	if fetchErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"status": server.Status,
			"logs":   "",
			"error":  fetchErr.Error(),
		})
		return
	}

	logs := ""
	if logsResp != nil {
		logs = logsResp.Logs
	}
	c.JSON(http.StatusOK, gin.H{
		"status": server.Status,
		"logs":   logs,
	})
}
