package ginapi

import (
	"net/http"

	stripekit "github.com/dreamoutbox/go-stripe-kit"
	"github.com/gin-gonic/gin"
)

// Refund binds a JSON RefundRequest, validates input, and executes a full or partial refund.
//
// Arguments:
//   - c: Active Gin request context with JSON body matching stripekit.RefundRequest.
//
// Example usage:
//
//	POST /api/v1/billing/refund
func (h *Handler) Refund(c *gin.Context) {
	var req stripekit.RefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid refund request payload"})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res, err := h.service.Refund(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, res)
}
