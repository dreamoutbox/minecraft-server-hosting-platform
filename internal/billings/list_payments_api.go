package billings

import (
	"net/http"

	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"
)

// ListPayments lists all payment records belonging to the authenticated user.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /api/v1/billing/payments
func (h *APIHandler) ListPayments(c *gin.Context) {
	userID, err := authmw.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	payments, err := h.service.ListUserPayments(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"payments": payments})
}
