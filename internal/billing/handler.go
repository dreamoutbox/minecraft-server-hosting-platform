package billing

import (
	"io"
	"net/http"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/middleware"
)

// Handler serves HTTP endpoints for billing, subscriptions, and payment webhooks.
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

// SavePaymentMethodRequest holds payment method token to attach.
type SavePaymentMethodRequest struct {
	PaymentMethodID string `json:"payment_method_id" binding:"required"`
}

// CreateSubscriptionRequest holds plan and server details for a new subscription.
type CreateSubscriptionRequest struct {
	PlanID   uint  `json:"plan_id" binding:"required"`
	ServerID *uint `json:"server_id"`
}

// SavePaymentMethod stores a customer's payment method token.
//
// Arguments:
//   - c: Active Gin request context containing SavePaymentMethodRequest JSON.
//
// Example usage:
//
//	POST /api/v1/billing/payment-methods
func (h *Handler) SavePaymentMethod(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req SavePaymentMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payment method request: " + err.Error()})
		return
	}

	if err := h.service.SavePaymentMethod(c.Request.Context(), userID, req.PaymentMethodID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "payment method saved successfully"})
}

// CreateSubscription provisions a recurring subscription and charges the first month.
//
// Arguments:
//   - c: Active Gin request context containing CreateSubscriptionRequest JSON.
//
// Example usage:
//
//	POST /api/v1/billing/subscriptions
func (h *Handler) CreateSubscription(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req CreateSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subscription request: " + err.Error()})
		return
	}

	sub, err := h.service.CreateSubscription(c.Request.Context(), userID, req.PlanID, req.ServerID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, sub)
}

// ListSubscriptions lists all subscriptions belonging to the user.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /api/v1/billing/subscriptions
func (h *Handler) ListSubscriptions(c *gin.Context) {
	userID, err := authkit.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	subs, err := h.service.ListUserSubscriptions(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"subscriptions": subs})
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
	billingGroup.Use(middleware.RateLimitMiddleware(10, 20))

	// Webhook endpoint (unauthenticated, signature-protected)
	billingGroup.POST("/webhook", h.Webhook)

	// User-authenticated billing management
	authenticated := billingGroup.Group("")
	authenticated.Use(authkit.AuthMiddleware(jwtSecret))
	{
		authenticated.POST("/payment-methods", h.SavePaymentMethod)
		authenticated.POST("/subscriptions", h.CreateSubscription)
		authenticated.GET("/subscriptions", h.ListSubscriptions)
	}
}
