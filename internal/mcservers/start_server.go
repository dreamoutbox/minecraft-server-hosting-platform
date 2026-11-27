package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// StartServer resumes a stopped server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/start
func (h *Handler) StartServer(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, _ := view.ParseIDParam(c, "id")

	_, err := h.hostingSvc.StartServer(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "", "success", "Server started"))
}
