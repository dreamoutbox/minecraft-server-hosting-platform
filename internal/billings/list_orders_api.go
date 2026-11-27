package billings

import (
	"net/http"

	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"
)

// ListOrders lists all checkout orders belonging to the authenticated user.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /api/v1/billing/orders
func (h *APIHandler) ListOrders(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	orders, err := h.service.ListUserOrders(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"orders": orders})
}
