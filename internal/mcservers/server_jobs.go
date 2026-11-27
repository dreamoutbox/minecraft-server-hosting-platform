package mcservers

import (
	"net/http"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowServerJobs renders the background job queue for a server exclusively to staff and admin users.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/jobs
func (h *Handler) ShowServerJobs(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		h.view.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Background job queue is only available to staff and admin users.",
		})
		return
	}

	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := h.hostingSvc.GetServer(c.Request.Context(), 0, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	var jobList []domain.ServerJob
	if h.jobSvc != nil {
		jobList, _ = h.jobSvc.ListServerJobs(c.Request.Context(), server.ID, 30)
	}
	h.view.RenderView(c, "server_jobs.html", map[string]interface{}{
		"ActiveTab": "jobs",
		"Server":    server,
		"Jobs":      jobList,
	})
}
