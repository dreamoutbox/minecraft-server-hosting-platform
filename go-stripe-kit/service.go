package stripekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Service orchestrates payment, checkout, refund, and webhook verification flows independent of any web framework.
type Service struct {
	provider      PaymentProvider
	webhookSecret string

	mu              sync.RWMutex
	eventHandlers   map[EventType][]WebhookHandler
	webhookHandlers []WebhookHandler
	refundHook      RefundHook
}

// NewService constructs a framework-agnostic Stripe billing service.
//
// Arguments:
//   - provider: Payment provider implementation (StripePaymentProvider or MockPaymentProvider).
//   - webhookSecret: Signing secret used to verify incoming Stripe webhook signatures.
//
// Example usage:
//
//	svc := stripekit.NewService(provider, os.Getenv("STRIPE_WEBHOOK_SECRET"))
func NewService(provider PaymentProvider, webhookSecret string) *Service {
	return &Service{
		provider:      provider,
		webhookSecret: webhookSecret,
		eventHandlers: make(map[EventType][]WebhookHandler),
	}
}

// Provider returns the configured underlying PaymentProvider.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	p := svc.Provider()
func (s *Service) Provider() PaymentProvider {
	return s.provider
}

// WebhookSecret returns the configured webhook signing secret.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	secret := svc.WebhookSecret()
func (s *Service) WebhookSecret() string {
	return s.webhookSecret
}

// SetRefundHook registers an optional callback invoked after a provider refund succeeds.
//
// Arguments:
//   - hook: Callback receiving the completed RefundResult (nil clears the hook).
//
// Example usage:
//
//	svc.SetRefundHook(func(ctx context.Context, res *stripekit.RefundResult) error { return nil })
func (s *Service) SetRefundHook(hook RefundHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refundHook = hook
}

// OnEvent registers a webhook handler invoked only for events matching eventType.
//
// Arguments:
//   - eventType: Target Stripe webhook event type.
//   - handler: Callback to invoke when a verified event matches eventType.
//
// Example usage:
//
//	svc.OnEvent(stripekit.EventCheckoutSessionCompleted, handleCheckout)
func (s *Service) OnEvent(eventType EventType, handler WebhookHandler) {
	if handler == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventHandlers[eventType] = append(s.eventHandlers[eventType], handler)
}

// OnWebhook registers a catch-all webhook handler invoked for every verified Stripe event.
//
// Arguments:
//   - handler: Callback to invoke for all verified webhook events.
//
// Example usage:
//
//	svc.OnWebhook(appBilling.HandleWebhookEvent)
func (s *Service) OnWebhook(handler WebhookHandler) {
	if handler == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhookHandlers = append(s.webhookHandlers, handler)
}

// CreatePayment creates a direct payment intent through the configured provider.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - amountCents: Positive charge amount in smallest currency unit.
//   - currency: ISO currency code (defaults to usd when empty).
//   - customerID: Optional customer identifier.
//
// Example usage:
//
//	res, err := svc.CreatePayment(ctx, 1000, "usd", "cus_123")
func (s *Service) CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error) {
	if s.provider == nil {
		return nil, errors.New("payment provider is not configured")
	}
	if amountCents <= 0 {
		return nil, errors.New("amount_cents must be greater than zero")
	}
	return s.provider.CreatePayment(ctx, amountCents, currency, customerID)
}

// GetPayment retrieves payment status and metadata from the configured provider.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - paymentID: PaymentIntent ID.
//
// Example usage:
//
//	res, err := svc.GetPayment(ctx, "pi_123")
func (s *Service) GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error) {
	if s.provider == nil {
		return nil, errors.New("payment provider is not configured")
	}
	if strings.TrimSpace(paymentID) == "" {
		return nil, errors.New("payment_id is required")
	}
	return s.provider.GetPayment(ctx, paymentID)
}

// CreateCheckoutSession validates the request and creates a hosted Stripe Checkout session.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Hosted checkout session configuration.
//
// Example usage:
//
//	cs, err := svc.CreateCheckoutSession(ctx, req)
func (s *Service) CreateCheckoutSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSessionResult, error) {
	if s.provider == nil {
		return nil, errors.New("payment provider is not configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return s.provider.CreateCheckoutSession(ctx, req)
}

// Refund validates the refund request, executes the refund via the provider, and runs the optional RefundHook.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Full (AmountCents == 0) or partial (AmountCents > 0) refund parameters.
//
// Example usage:
//
//	res, err := svc.Refund(ctx, stripekit.RefundRequest{PaymentID: "pi_123", Reason: stripekit.RefundReasonRequestedByCustomer})
func (s *Service) Refund(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	if s.provider == nil {
		return nil, errors.New("payment provider is not configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	res, err := s.provider.Refund(ctx, req)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	hook := s.refundHook
	s.mu.RUnlock()

	if hook != nil {
		if err := hook(ctx, res); err != nil {
			return res, fmt.Errorf("refund hook failed: %w", err)
		}
	}
	return res, nil
}

// HandleWebhook verifies the incoming webhook signature and dispatches the event to registered handlers.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - payload: Raw HTTP request body.
//   - signatureHeader: Stripe-Signature HTTP header value.
//
// Example usage:
//
//	evt, err := svc.HandleWebhook(ctx, rawBody, sigHeader)
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) (*WebhookEvent, error) {
	if s.provider == nil {
		return nil, errors.New("payment provider is not configured")
	}

	event, err := s.provider.VerifyWebhookSignature(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("webhook signature verification failed: %w", err)
	}

	handlers := s.snapshotHandlers(event.Type)
	for _, h := range handlers {
		if err := h(ctx, event); err != nil {
			return event, err
		}
	}

	return event, nil
}

func (s *Service) snapshotHandlers(eventType EventType) []WebhookHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()

	specific := s.eventHandlers[eventType]
	combined := make([]WebhookHandler, 0, len(specific)+len(s.webhookHandlers))
	combined = append(combined, specific...)
	combined = append(combined, s.webhookHandlers...)
	return combined
}
