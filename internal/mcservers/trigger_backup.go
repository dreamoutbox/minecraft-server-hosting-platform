package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// TriggerBackup queues an on-demand snapshot backup job for the server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/backups/now
func (h *Handler) TriggerBackup(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	if _, _, err := h.jobSvc.EnqueueBackup(c.Request.Context(), userID, serverIDVal); err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/backups", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/backups", "success", "Backup queued"))
}
