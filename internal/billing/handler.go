package billing

import (
	"io"
	"log"
	"net/http"

	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/gin_middlewares"
)

// Handler serves HTTP endpoints for billing orders, payment history, and payment webhooks.
type Handler struct {
	service *Service
}

// NewHandler instantiates a billing HTTP controller.
//
// Arguments:
//   - service: Domain billing service.
//
// Example usage:
//
//	h := billing.NewHandler(svc)
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// ListOrders lists all checkout orders belonging to the user.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /api/v1/billing/orders
func (h *Handler) ListOrders(c *gin.Context) {
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

// ListPayments lists all payment records belonging to the user.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /api/v1/billing/payments
func (h *Handler) ListPayments(c *gin.Context) {
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

// Webhook processes asynchronous payment notifications with mandatory signature verification.
//
// Arguments:
//   - c: Active Gin request context containing raw payload and Stripe-Signature header.
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

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed reading request body"})
		return
	}

	if err := h.service.HandleWebhook(c.Request.Context(), payload, sigHeader); err != nil {
		log.Printf("[WARN] billing webhook failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// RegisterRoutes registers billing and webhook routes onto the router engine.
//
// Arguments:
//   - r: Gin engine.
//   - jwtSecret: Secret key to configure AuthMiddleware.
//
// Example usage:
//
//	billingHandler.RegisterRoutes(router, cfg.JWTSecret)
func (h *Handler) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	billingGroup := r.Group("/api/v1/billing")
	billingGroup.Use(gin_middlewares.RateLimitMiddleware(10, 20))

	// Webhook endpoint (unauthenticated, signature-protected)
	billingGroup.POST("/webhook", h.Webhook)

	// User-authenticated billing history queries
	authenticated := billingGroup.Group("")
	authenticated.Use(authmw.AuthMiddleware(jwtSecret))
	{
		authenticated.GET("/orders", h.ListOrders)
		authenticated.GET("/payments", h.ListPayments)
	}
}
