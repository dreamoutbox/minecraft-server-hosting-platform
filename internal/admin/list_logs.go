package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminListLogs renders all server_operation_logs across the cluster with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/logs?q=backup&page=1
func (h *Handler) AdminListLogs(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin operation logs are restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := view.ParseSearchAndPage(c)
	query := h.db.WithContext(c.Request.Context()).Model(&domain.ServerOperationLog{})
	query = view.ApplySearchFilter(
		query,
		search,
		"LOWER(operation) LIKE ?",
		"LOWER(status) LIKE ?",
		"LOWER(message) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"CAST(server_id AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"server_id IN (SELECT id FROM servers WHERE LOWER(name) LIKE ?)",
	)

	var logs []domain.ServerOperationLog
	pagination, err := view.PaginateQuery(query, search, page, pageSize, "/dashboard/admin/logs", "id desc", &logs)
	if err != nil {
		h.view.RenderAdminView(c, "admin_logs.html", map[string]interface{}{
			"FlashError": "Failed loading operation logs: " + err.Error(),
			"Pagination": view.Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/logs"},
		})
		return
	}

	userIDs := make([]uint, 0, len(logs))
	serverIDs := make([]uint, 0, len(logs))
	for _, l := range logs {
		userIDs = append(userIDs, l.UserID)
		serverIDs = append(serverIDs, l.ServerID)
	}

	h.view.RenderAdminView(c, "admin_logs.html", map[string]interface{}{
		"Logs":        logs,
		"UserEmails":  view.LoadUserEmailMap(c.Request.Context(), h.db, userIDs),
		"ServerNames": view.LoadServerNameMap(c.Request.Context(), h.db, serverIDs),
		"Pagination":  pagination,
	})
}
