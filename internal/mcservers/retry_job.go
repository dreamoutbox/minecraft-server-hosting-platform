package mcservers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// RetryJob re-queues a failed background job from the dashboard UI.
//
// Arguments:
//   - c: Active Gin request context with URL params 'id' and 'job_id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/jobs/:job_id/retry
func (h *Handler) RetryJob(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	role := authmw.GetUserRoleFromContext(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}
	jobIDVal, ok := view.ParseIDParam(c, "job_id")
	if !ok {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "", "error", "Invalid job ID"))
		return
	}

	returnTo := c.PostForm("return_to")
	redirectFor := func(kind string, msg string) string {
		if returnTo == "/dashboard/admin/jobs" && role.IsAdmin() {
			return fmt.Sprintf("/dashboard/admin/jobs?%s=%s", kind, url.QueryEscape(msg))
		}
		subpath := "/jobs"
		if strings.HasPrefix(returnTo, fmt.Sprintf("/dashboard/servers/%d/backups", serverIDVal)) {
			subpath = "/backups"
		}
		return view.ServerRedirectURL(serverIDVal, subpath, kind, msg)
	}

	if _, err := h.jobSvc.RetryJob(c.Request.Context(), userID, role.IsStaffOrAdmin(), jobIDVal); err != nil {
		c.Redirect(http.StatusFound, redirectFor("error", err.Error()))
		return
	}

	c.Redirect(http.StatusFound, redirectFor("success", "Job re-queued"))
}
