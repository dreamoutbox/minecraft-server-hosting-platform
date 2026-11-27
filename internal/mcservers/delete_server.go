package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// DeleteServer enqueues asynchronous teardown and deletion of a server instance.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/delete
func (h *Handler) DeleteServer(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	if _, err := h.jobSvc.EnqueueDeleteServer(c.Request.Context(), userID, serverIDVal); err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/settings", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/dashboard?success=Server+deletion+queued")
}
