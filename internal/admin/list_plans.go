package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminListPlans renders all hosting plans with search, pagination, and CRUD controls.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/plans?q=small&page=1
func (h *Handler) AdminListPlans(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := view.ParseSearchAndPage(c)
	query := h.db.WithContext(c.Request.Context()).Model(&domain.Plan{})
	query = view.ApplySearchFilter(
		query,
		search,
		"LOWER(name) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
	)

	var plans []domain.Plan
	pagination, err := view.PaginateQuery(query, search, page, pageSize, "/dashboard/admin/plans", "id asc", &plans)
	if err != nil {
		h.view.RenderAdminView(c, "admin_plans.html", map[string]interface{}{
			"FlashError": "Failed loading plans: " + err.Error(),
			"Pagination": view.Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/plans"},
		})
		return
	}

	h.view.RenderAdminView(c, "admin_plans.html", map[string]interface{}{
		"Plans":      plans,
		"Pagination": pagination,
	})
}
