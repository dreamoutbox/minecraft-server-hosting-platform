package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// RestoreBackup queues a job reverting server storage to the chosen snapshot.
//
// Arguments:
//   - c: Active Gin request context with URL parameters 'id' and 'backup_id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/backups/:backup_id/restore
func (h *Handler) RestoreBackup(c *gin.Context) {
	userID := view.EffectiveServerUserID(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}
	backupIDVal, ok := view.ParseIDParam(c, "backup_id")
	if !ok {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/backups", "error", "Invalid backup ID"))
		return
	}

	if _, err := h.jobSvc.EnqueueRestore(c.Request.Context(), userID, serverIDVal, backupIDVal); err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/backups", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/backups", "success", "Restore queued"))
}
