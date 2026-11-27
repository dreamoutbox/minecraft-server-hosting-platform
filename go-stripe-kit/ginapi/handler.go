package ginapi

import (
	"context"

	stripekit "github.com/dreamoutbox/go-stripe-kit"
	"github.com/gin-gonic/gin"
)

// DefaultMaxWebhookBodyBytes caps incoming Stripe webhook payloads to protect memory.
const DefaultMaxWebhookBodyBytes int64 = 1 << 20 // 1 MiB

// BillingService defines the core stripekit operations invoked by Gin HTTP handlers.
type BillingService interface {
	HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) (*stripekit.WebhookEvent, error)
	Refund(ctx context.Context, req stripekit.RefundRequest) (*stripekit.RefundResult, error)
}

// Handler adapts core stripekit operations to Gin HTTP routes without embedding business logic.
type Handler struct {
	service      BillingService
	maxBodyBytes int64
}

// NewHandler constructs a Gin HTTP handler for Stripe webhooks and refunds.
//
// Arguments:
//   - service: Core stripekit service implementing BillingService.
//
// Example usage:
//
//	h := ginapi.NewHandler(stripeSvc)
func NewHandler(service BillingService) *Handler {
	return &Handler{
		service:      service,
		maxBodyBytes: DefaultMaxWebhookBodyBytes,
	}
}

// RegisterWebhookRoute mounts POST /webhook onto the supplied router group.
//
// Arguments:
//   - rg: Target Gin router group (e.g. /api/v1/billing with caller-configured rate limiting).
//
// Example usage:
//
//	stripeGin.RegisterWebhookRoute(billingGroup)
func (h *Handler) RegisterWebhookRoute(rg *gin.RouterGroup) {
	rg.POST("/webhook", h.Webhook)
}

// RegisterRefundRoute mounts POST /refund onto the supplied router group.
// Callers are responsible for attaching authentication and authorization middleware to rg.
//
// Arguments:
//   - rg: Target Gin router group protected by caller-supplied admin/auth middleware.
//
// Example usage:
//
//	stripeGin.RegisterRefundRoute(adminBillingGroup)
func (h *Handler) RegisterRefundRoute(rg *gin.RouterGroup) {
	rg.POST("/refund", h.Refund)
}
