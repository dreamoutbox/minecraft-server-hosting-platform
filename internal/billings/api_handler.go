package billings

import (
	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/dreamoutbox/go-stripe-kit/ginapi"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/gin_middlewares"
)

// APIHandler serves JSON API endpoints for user orders, payment history, and Stripe webhooks.
type APIHandler struct {
	service   *Service
	stripeGin *ginapi.Handler
}

// NewAPIHandler instantiates the JSON billing API controller.
//
// Arguments:
//   - service: Platform billing service for order and payment history queries.
//   - stripeGin: Reusable stripekit Gin handler for signature-verified Stripe webhooks.
//
// Example usage:
//
//	apiHandler := billings.NewAPIHandler(billingSvc, stripeGinHandler)
func NewAPIHandler(service *Service, stripeGin *ginapi.Handler) *APIHandler {
	return &APIHandler{
		service:   service,
		stripeGin: stripeGin,
	}
}

// RegisterRoutes mounts /api/v1/billing webhook and user history endpoints onto the router engine.
//
// Arguments:
//   - r: Gin engine.
//   - jwtSecret: Secret key used to configure AuthMiddleware.
//
// Example usage:
//
//	billingAPIHandler.RegisterRoutes(router, cfg.JWTSecret)
func (h *APIHandler) RegisterRoutes(r *gin.Engine, jwtSecret string) {
	billingGroup := r.Group("/api/v1/billing")
	billingGroup.Use(gin_middlewares.RateLimitMiddleware(10, 20))

	// Webhook endpoint (unauthenticated, signature-protected via go-stripe-kit/ginapi)
	h.stripeGin.RegisterWebhookRoute(billingGroup)

	// User-authenticated billing history queries
	authenticated := billingGroup.Group("")
	authenticated.Use(authmw.AuthMiddleware(jwtSecret))
	{
		authenticated.GET("/orders", h.ListOrders)
		authenticated.GET("/payments", h.ListPayments)
	}
}
