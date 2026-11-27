package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminListServers renders all provisioned Minecraft servers across all users with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/servers?q=survival&page=1
func (h *Handler) AdminListServers(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin server management is restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := view.ParseSearchAndPage(c)
	query := h.db.WithContext(c.Request.Context()).
		Model(&domain.Server{}).
		Preload("Plan").
		Preload("Host")
	query = view.ApplySearchFilter(
		query,
		search,
		"LOWER(name) LIKE ?",
		"LOWER(status) LIKE ?",
		"LOWER(server_type) LIKE ?",
		"LOWER(server_version) LIKE ?",
		"LOWER(COALESCE(container_id, '')) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"CAST(port AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"host_id IN (SELECT id FROM hosts WHERE LOWER(hostname) LIKE ?)",
		"plan_id IN (SELECT id FROM plans WHERE LOWER(name) LIKE ?)",
	)

	var servers []domain.Server
	pagination, err := view.PaginateQuery(query, search, page, pageSize, "/dashboard/admin/servers", "id desc", &servers)
	if err != nil {
		h.view.RenderAdminView(c, "admin_servers.html", map[string]interface{}{
			"FlashError": "Failed loading servers: " + err.Error(),
			"Pagination": view.Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/servers"},
		})
		return
	}

	userIDs := make([]uint, 0, len(servers))
	for _, srv := range servers {
		userIDs = append(userIDs, srv.UserID)
	}

	h.view.RenderAdminView(c, "admin_servers.html", map[string]interface{}{
		"Servers":    servers,
		"UserEmails": view.LoadUserEmailMap(c.Request.Context(), h.db, userIDs),
		"Pagination": pagination,
	})
}
