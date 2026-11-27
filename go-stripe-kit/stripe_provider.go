package stripekit

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/client"
	"github.com/stripe/stripe-go/v81/webhook"
)

// StripePaymentProvider executes live payment, checkout, refund, and webhook verification operations against Stripe.
type StripePaymentProvider struct {
	api *client.API
}

// NewStripePaymentProvider constructs a live Stripe payment provider with an isolated API client.
//
// Arguments:
//   - apiKey: Stripe secret API key (sk_live_... or sk_test_...).
//
// Example usage:
//
//	provider := stripekit.NewStripePaymentProvider(os.Getenv("STRIPE_SECRET_KEY"))
func NewStripePaymentProvider(apiKey string) *StripePaymentProvider {
	return &StripePaymentProvider{
		api: client.New(apiKey, nil),
	}
}

// CreatePayment creates a Stripe PaymentIntent for the requested amount and currency.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - amountCents: Charge amount in smallest currency unit (e.g. cents).
//   - currency: ISO 4217 currency code (defaults to usd when empty).
//   - customerID: Optional Stripe customer ID.
//
// Example usage:
//
//	res, err := provider.CreatePayment(ctx, 1000, "usd", "cus_123")
func (s *StripePaymentProvider) CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error) {
	if currency == "" {
		currency = DefaultCurrency
	}
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(currency),
	}
	params.Context = ctx
	if customerID != "" {
		params.Customer = stripe.String(customerID)
	}

	pi, err := s.api.PaymentIntents.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe payment intent creation failed: %w", err)
	}

	return &PaymentResult{
		TransactionID: pi.ID,
		AmountCents:   pi.Amount,
		Currency:      string(pi.Currency),
		Status:        mapStripePaymentStatus(pi.Status),
	}, nil
}

// GetPayment fetches an existing Stripe PaymentIntent by ID.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - paymentID: Stripe PaymentIntent ID (pi_...).
//
// Example usage:
//
//	res, err := provider.GetPayment(ctx, "pi_123")
func (s *StripePaymentProvider) GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error) {
	params := &stripe.PaymentIntentParams{}
	params.Context = ctx

	pi, err := s.api.PaymentIntents.Get(paymentID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe payment intent lookup failed: %w", err)
	}

	return &PaymentResult{
		TransactionID: pi.ID,
		AmountCents:   pi.Amount,
		Currency:      string(pi.Currency),
		Status:        mapStripePaymentStatus(pi.Status),
	}, nil
}

// Refund issues a full or partial refund against a Stripe PaymentIntent.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Refund parameters including payment ID, optional partial amount, reason, metadata, and idempotency key.
//
// Example usage:
//
//	res, err := provider.Refund(ctx, stripekit.RefundRequest{PaymentID: "pi_123", AmountCents: 500})
func (s *StripePaymentProvider) Refund(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(req.PaymentID),
	}
	params.Context = ctx
	if req.AmountCents > 0 {
		params.Amount = stripe.Int64(req.AmountCents)
	}
	if req.Reason != "" {
		params.Reason = stripe.String(string(req.Reason))
	}
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}
	if req.IdempotencyKey != "" {
		params.SetIdempotencyKey(req.IdempotencyKey)
	}

	res, err := s.api.Refunds.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe refund failed: %w", err)
	}

	paymentID := req.PaymentID
	if res.PaymentIntent != nil && res.PaymentIntent.ID != "" {
		paymentID = res.PaymentIntent.ID
	}

	return &RefundResult{
		RefundID:    res.ID,
		PaymentID:   paymentID,
		AmountCents: res.Amount,
		Currency:    string(res.Currency),
		Status:      mapStripeRefundStatus(res.Status),
		Reason:      RefundReason(res.Reason),
	}, nil
}

// CreateCheckoutSession creates a hosted Stripe Checkout session for a one-time payment.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Checkout session line-item, redirect URL, and optional metadata parameters.
//
// Example usage:
//
//	cs, err := provider.CreateCheckoutSession(ctx, req)
func (s *StripePaymentProvider) CreateCheckoutSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSessionResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	currency := req.Currency
	if currency == "" {
		currency = DefaultCurrency
	}

	productData := &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
		Name: stripe.String(req.ProductName),
	}
	if req.Description != "" {
		productData.Description = stripe.String(req.Description)
	}

	params := &stripe.CheckoutSessionParams{
		Mode:       stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL: stripe.String(req.SuccessURL),
		CancelURL:  stripe.String(req.CancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency:    stripe.String(currency),
					UnitAmount:  stripe.Int64(req.AmountCents),
					ProductData: productData,
				},
			},
		},
	}
	params.Context = ctx
	if req.ClientReferenceID != "" {
		params.ClientReferenceID = stripe.String(req.ClientReferenceID)
	}
	if req.CustomerEmail != "" {
		params.CustomerEmail = stripe.String(req.CustomerEmail)
	}
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}

	cs, err := s.api.CheckoutSessions.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe checkout session creation failed: %w", err)
	}

	return &CheckoutSessionResult{
		SessionID:   cs.ID,
		CheckoutURL: cs.URL,
	}, nil
}

// VerifyWebhookSignature validates a Stripe-Signature header and normalizes the event payload.
//
// Arguments:
//   - payload: Raw HTTP webhook request body.
//   - signatureHeader: Value of the Stripe-Signature header.
//   - secret: Stripe webhook endpoint signing secret (whsec_...).
//
// Example usage:
//
//	evt, err := provider.VerifyWebhookSignature(body, sigHeader, webhookSecret)
func (s *StripePaymentProvider) VerifyWebhookSignature(payload []byte, signatureHeader string, secret string) (*WebhookEvent, error) {
	event, err := webhook.ConstructEventWithOptions(payload, signatureHeader, secret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return nil, fmt.Errorf("stripe signature verification failed: %w", err)
	}

	return parseStripeEventObject(EventType(event.Type), event.Data.Object), nil
}

func parseStripeEventObject(eventType EventType, obj map[string]interface{}) *WebhookEvent {
	evt := &WebhookEvent{
		Type:       eventType,
		Currency:   stringField(obj, "currency"),
		CustomerID: stringField(obj, "customer"),
		Metadata:   metadataField(obj, "metadata"),
		Status:     PaymentStatusPending,
	}

	switch eventType {
	case EventCheckoutSessionCompleted:
		evt.Status = PaymentStatusSucceeded
		evt.SessionID = stringField(obj, "id")
		evt.PaymentID = stringField(obj, "payment_intent")
		evt.ClientReferenceID = stringField(obj, "client_reference_id")
		evt.AmountCents = int64Field(obj, "amount_total")
	case EventPaymentIntentSucceeded:
		evt.Status = PaymentStatusSucceeded
		evt.PaymentID = stringField(obj, "id")
		evt.AmountCents = int64Field(obj, "amount")
	case EventPaymentIntentPaymentFailed:
		evt.Status = PaymentStatusFailed
		evt.PaymentID = stringField(obj, "id")
		evt.AmountCents = int64Field(obj, "amount")
	case EventChargeRefunded:
		evt.PaymentID = stringField(obj, "payment_intent")
		if evt.PaymentID == "" {
			evt.PaymentID = stringField(obj, "id")
		}
		evt.AmountCents = int64Field(obj, "amount_refunded")
		if evt.AmountCents == 0 {
			evt.AmountCents = int64Field(obj, "amount")
		}
		evt.RefundID = latestRefundIDField(obj)
		evt.Status = PaymentStatusRefunded
		if fullyRefunded, ok := obj["refunded"].(bool); ok && !fullyRefunded {
			evt.Status = PaymentStatusSucceeded
		}
	}

	return evt
}

func stringField(obj map[string]interface{}, key string) string {
	if val, ok := obj[key].(string); ok {
		return val
	}
	return ""
}

func int64Field(obj map[string]interface{}, key string) int64 {
	if val, ok := obj[key].(float64); ok {
		return int64(val)
	}
	return 0
}

func metadataField(obj map[string]interface{}, key string) map[string]string {
	raw, ok := obj[key].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if str, ok := v.(string); ok {
			out[k] = str
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func latestRefundIDField(obj map[string]interface{}) string {
	if direct := stringField(obj, "refund_id"); direct != "" {
		return direct
	}
	refundsObj, ok := obj["refunds"].(map[string]interface{})
	if !ok {
		return ""
	}
	data, ok := refundsObj["data"].([]interface{})
	if !ok || len(data) == 0 {
		return ""
	}
	first, ok := data[0].(map[string]interface{})
	if !ok {
		return ""
	}
	return stringField(first, "id")
}

func mapStripePaymentStatus(status stripe.PaymentIntentStatus) PaymentStatus {
	switch status {
	case stripe.PaymentIntentStatusSucceeded:
		return PaymentStatusSucceeded
	case stripe.PaymentIntentStatusCanceled:
		return PaymentStatusFailed
	default:
		return PaymentStatusPending
	}
}

func mapStripeRefundStatus(status stripe.RefundStatus) RefundStatus {
	switch status {
	case stripe.RefundStatusSucceeded:
		return RefundStatusSucceeded
	case stripe.RefundStatusFailed:
		return RefundStatusFailed
	case stripe.RefundStatusCanceled:
		return RefundStatusCanceled
	default:
		return RefundStatusPending
	}
}
