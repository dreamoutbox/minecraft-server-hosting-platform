package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminListPayments renders all payment_records across all users with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/payments?q=succeeded&page=1
func (h *Handler) AdminListPayments(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin payment records are restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := view.ParseSearchAndPage(c)
	query := h.db.WithContext(c.Request.Context()).Model(&domain.PaymentRecord{})
	query = view.ApplySearchFilter(
		query,
		search,
		"LOWER(provider_tx_id) LIKE ?",
		"LOWER(status) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"(order_id IS NOT NULL AND CAST(order_id AS TEXT) LIKE ?)",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
	)

	var payments []domain.PaymentRecord
	pagination, err := view.PaginateQuery(query, search, page, pageSize, "/dashboard/admin/payments", "id desc", &payments)
	if err != nil {
		h.view.RenderAdminView(c, "admin_payments.html", map[string]interface{}{
			"FlashError": "Failed loading payment records: " + err.Error(),
			"Pagination": view.Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/payments"},
		})
		return
	}

	userIDs := make([]uint, 0, len(payments))
	for _, p := range payments {
		userIDs = append(userIDs, p.UserID)
	}

	h.view.RenderAdminView(c, "admin_payments.html", map[string]interface{}{
		"Payments":   payments,
		"UserEmails": view.LoadUserEmailMap(c.Request.Context(), h.db, userIDs),
		"Pagination": pagination,
	})
}
