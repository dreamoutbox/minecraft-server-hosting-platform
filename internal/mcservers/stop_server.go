package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// StopServer halts a running server container.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/stop
func (h *Handler) StopServer(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, _ := view.ParseIDParam(c, "id")

	_, err := h.hostingSvc.StopServer(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "", "success", "Server stopped"))
}
