package billings

import (
	"context"
	"time"

	stripekit "github.com/dreamoutbox/go-stripe-kit"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// JobEnqueuer enqueues async server provisioning jobs after checkout payment completes.
type JobEnqueuer interface {
	EnqueueCreateServerWithExpiry(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string, expiresAt *time.Time) (*domain.Server, *domain.ServerJob, error)
}

// Service manages Minecraft server hosting checkout orders, payment history, and webhook fulfillment.
type Service struct {
	db            *gorm.DB
	stripeSvc     *stripekit.Service
	jobQueue      JobEnqueuer
	appBaseURL    string
	retentionDays int
}

// NewService instantiates the platform billing service and registers its webhook fulfillment handler on stripeSvc.
//
// Arguments:
//   - db: Active database handle.
//   - stripeSvc: Configured reusable stripekit service.
//
// Example usage:
//
//	svc := billings.NewService(db, stripeSvc)
func NewService(db *gorm.DB, stripeSvc *stripekit.Service) *Service {
	svc := &Service{
		db:            db,
		stripeSvc:     stripeSvc,
		appBaseURL:    "http://localhost:8080",
		retentionDays: 7,
	}
	if stripeSvc != nil {
		stripeSvc.OnWebhook(svc.HandleWebhookEvent)
	}
	return svc
}

// StripeService returns the underlying reusable stripekit service.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	stripeSvc := billingSvc.StripeService()
func (s *Service) StripeService() *stripekit.Service {
	return s.stripeSvc
}

// SetJobQueue wires the async job queue used to provision servers after Stripe Checkout completion.
//
// Arguments:
//   - jq: Async server creation job enqueuer.
//
// Example usage:
//
//	billingSvc.SetJobQueue(jobService)
func (s *Service) SetJobQueue(jq JobEnqueuer) {
	s.jobQueue = jq
}

// SetAppBaseURL sets the external base URL used for Stripe Checkout success/cancel redirects.
//
// Arguments:
//   - baseURL: Public dashboard URL prefix (e.g. "https://panel.example.com").
//
// Example usage:
//
//	billingSvc.SetAppBaseURL(cfg.AppBaseURL)
func (s *Service) SetAppBaseURL(baseURL string) {
	if baseURL != "" {
		s.appBaseURL = baseURL
	}
}

// SetRetentionDays configures the post-expiration grace window in days during which extensions are allowed.
//
// Arguments:
//   - days: Positive number of grace-period days after server expiration.
//
// Example usage:
//
//	billingSvc.SetRetentionDays(cfg.RetentionDays)
func (s *Service) SetRetentionDays(days int) {
	if days > 0 {
		s.retentionDays = days
	}
}

// HandleWebhook verifies the signature and dispatches the webhook through the configured stripekit service.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - payload: Raw HTTP webhook body.
//   - signatureHeader: Stripe-Signature header value.
//
// Example usage:
//
//	err := billingSvc.HandleWebhook(ctx, body, sigHeader)
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) error {
	_, err := s.stripeSvc.HandleWebhook(ctx, payload, signatureHeader)
	return err
}

func mapStripePaymentStatus(status stripekit.PaymentStatus) domain.PaymentStatus {
	switch status {
	case stripekit.PaymentStatusSucceeded:
		return domain.PaymentStatusSucceeded
	case stripekit.PaymentStatusFailed:
		return domain.PaymentStatusFailed
	case stripekit.PaymentStatusRefunded:
		return domain.PaymentStatusRefunded
	default:
		return domain.PaymentStatusPending
	}
}
