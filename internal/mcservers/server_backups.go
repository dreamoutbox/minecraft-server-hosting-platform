package mcservers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowServerBackups renders the dedicated World Backups page for a server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/backups
func (h *Handler) ShowServerBackups(c *gin.Context) {
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

	backups, _ := h.backupSvc.ListBackups(c.Request.Context(), server.ID)
	var backupJobs []domain.ServerJob
	if h.jobSvc != nil {
		allJobs, _ := h.jobSvc.ListServerJobs(c.Request.Context(), server.ID, 20)
		for _, j := range allJobs {
			if j.Type == domain.JobTypeBackup || j.Type == domain.JobTypeRestore {
				backupJobs = append(backupJobs, j)
			}
		}
	}

	h.view.RenderView(c, "server_backups.html", map[string]interface{}{
		"ActiveTab": "backups",
		"Server":    server,
		"Backups":   backups,
		"Jobs":      backupJobs,
	})
}
