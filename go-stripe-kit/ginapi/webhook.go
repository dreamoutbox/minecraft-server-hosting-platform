package ginapi

import (
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Webhook verifies the Stripe-Signature header on the raw body and dispatches the event via BillingService.
//
// Arguments:
//   - c: Active Gin request context carrying Stripe-Signature header and raw JSON payload.
//
// Example usage:
//
//	POST /api/v1/billing/webhook
func (h *Handler) Webhook(c *gin.Context) {
	sigHeader := c.GetHeader("Stripe-Signature")
	if sigHeader == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing Stripe-Signature header"})
		return
	}

	bodyReader := http.MaxBytesReader(c.Writer, c.Request.Body, h.maxBodyBytes)
	payload, err := io.ReadAll(bodyReader)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed reading request body"})
		return
	}

	if _, err := h.service.HandleWebhook(c.Request.Context(), payload, sigHeader); err != nil {
		log.Printf("[WARN] stripe webhook failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}
