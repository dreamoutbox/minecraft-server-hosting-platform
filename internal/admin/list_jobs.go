package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminListJobs renders all queued and historical server_jobs with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/jobs?q=failed&page=1
func (h *Handler) AdminListJobs(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin server jobs are restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := view.ParseSearchAndPage(c)
	query := h.db.WithContext(c.Request.Context()).Model(&domain.ServerJob{})
	query = view.ApplySearchFilter(
		query,
		search,
		"LOWER(type) LIKE ?",
		"LOWER(status) LIKE ?",
		"LOWER(COALESCE(last_error, '')) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"CAST(server_id AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"server_id IN (SELECT id FROM servers WHERE LOWER(name) LIKE ?)",
	)

	var jobList []domain.ServerJob
	pagination, err := view.PaginateQuery(query, search, page, pageSize, "/dashboard/admin/jobs", "id desc", &jobList)
	if err != nil {
		h.view.RenderAdminView(c, "admin_jobs.html", map[string]interface{}{
			"FlashError": "Failed loading server jobs: " + err.Error(),
			"Pagination": view.Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/jobs"},
		})
		return
	}

	userIDs := make([]uint, 0, len(jobList))
	serverIDs := make([]uint, 0, len(jobList))
	for _, j := range jobList {
		userIDs = append(userIDs, j.UserID)
		serverIDs = append(serverIDs, j.ServerID)
	}

	h.view.RenderAdminView(c, "admin_jobs.html", map[string]interface{}{
		"Jobs":        jobList,
		"UserEmails":  view.LoadUserEmailMap(c.Request.Context(), h.db, userIDs),
		"ServerNames": view.LoadServerNameMap(c.Request.Context(), h.db, serverIDs),
		"Pagination":  pagination,
	})
}
