package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminListOrders renders all Stripe checkout orders across all users with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/orders?q=fulfilled&page=1
func (h *Handler) AdminListOrders(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin order history is restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := view.ParseSearchAndPage(c)
	query := h.db.WithContext(c.Request.Context()).
		Model(&domain.Order{}).
		Preload("Plan")
	query = view.ApplySearchFilter(
		query,
		search,
		"LOWER(server_name) LIKE ?",
		"LOWER(stripe_session_id) LIKE ?",
		"LOWER(order_type) LIKE ?",
		"LOWER(status) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"plan_id IN (SELECT id FROM plans WHERE LOWER(name) LIKE ?)",
	)

	var orders []domain.Order
	pagination, err := view.PaginateQuery(query, search, page, pageSize, "/dashboard/admin/orders", "id desc", &orders)
	if err != nil {
		h.view.RenderAdminView(c, "admin_orders.html", map[string]interface{}{
			"FlashError": "Failed loading orders: " + err.Error(),
			"Pagination": view.Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/orders"},
		})
		return
	}

	userIDs := make([]uint, 0, len(orders))
	for _, o := range orders {
		userIDs = append(userIDs, o.UserID)
	}

	h.view.RenderAdminView(c, "admin_orders.html", map[string]interface{}{
		"Orders":     orders,
		"UserEmails": view.LoadUserEmailMap(c.Request.Context(), h.db, userIDs),
		"Pagination": pagination,
	})
}
