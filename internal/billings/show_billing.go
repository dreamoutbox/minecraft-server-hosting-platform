package billings

import (
	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"
)

// ShowBilling displays customer checkout order history and payment records.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard/billing
func (h *Handler) ShowBilling(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)

	orders, _ := h.billingSvc.ListUserOrders(c.Request.Context(), userID)
	payments, _ := h.billingSvc.ListUserPayments(c.Request.Context(), userID)

	h.view.RenderView(c, "billing.html", map[string]interface{}{
		"Orders":   orders,
		"Payments": payments,
	})
}
