package stripekit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type mockPaymentRecord struct {
	result        PaymentResult
	refundedCents int64
}

// MockPaymentProvider provides deterministic in-memory payment, checkout, refund, and webhook responses for local development and tests.
type MockPaymentProvider struct {
	mu       sync.RWMutex
	payments map[string]*mockPaymentRecord
	refunds  map[string]*RefundResult
}

// NewMockPaymentProvider constructs an in-memory mock payment provider.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	provider := stripekit.NewMockPaymentProvider()
func NewMockPaymentProvider() *MockPaymentProvider {
	return &MockPaymentProvider{
		payments: make(map[string]*mockPaymentRecord),
		refunds:  make(map[string]*RefundResult),
	}
}

// CreatePayment records a succeeded mock payment in memory.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - amountCents: Charge amount in cents.
//   - currency: Currency code (defaults to usd when empty).
//   - customerID: Optional customer identifier.
//
// Example usage:
//
//	res, err := mock.CreatePayment(ctx, 1000, "usd", "cus_test")
func (m *MockPaymentProvider) CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error) {
	if amountCents <= 0 {
		return nil, errors.New("amount_cents must be greater than zero")
	}
	if currency == "" {
		currency = DefaultCurrency
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	txID := fmt.Sprintf("mock_pi_%s", uuid.New().String())
	res := PaymentResult{
		TransactionID: txID,
		AmountCents:   amountCents,
		Currency:      currency,
		Status:        PaymentStatusSucceeded,
	}
	m.payments[txID] = &mockPaymentRecord{result: res}
	copyRes := res
	return &copyRes, nil
}

// GetPayment looks up a previously recorded mock payment by transaction ID.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - paymentID: Mock payment transaction ID.
//
// Example usage:
//
//	res, err := mock.GetPayment(ctx, txID)
func (m *MockPaymentProvider) GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rec, exists := m.payments[paymentID]
	if !exists {
		return nil, errors.New("mock payment not found")
	}
	copyRes := rec.result
	return &copyRes, nil
}

// Refund issues a full or partial refund against an in-memory mock payment.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Refund parameters; AmountCents == 0 refunds the full remaining balance.
//
// Example usage:
//
//	res, err := mock.Refund(ctx, stripekit.RefundRequest{PaymentID: txID, AmountCents: 500})
func (m *MockPaymentProvider) Refund(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rec, exists := m.payments[req.PaymentID]
	if !exists {
		return nil, errors.New("mock payment not found")
	}

	remaining := rec.result.AmountCents - rec.refundedCents
	if remaining <= 0 {
		return nil, errors.New("payment already fully refunded")
	}

	refundAmount := req.AmountCents
	if refundAmount == 0 {
		refundAmount = remaining
	}
	if refundAmount > remaining {
		return nil, fmt.Errorf("refund amount %d exceeds remaining refundable balance %d", refundAmount, remaining)
	}

	rec.refundedCents += refundAmount
	if rec.refundedCents == rec.result.AmountCents {
		rec.result.Status = PaymentStatusRefunded
	}

	res := &RefundResult{
		RefundID:    fmt.Sprintf("mock_re_%s", uuid.New().String()),
		PaymentID:   req.PaymentID,
		AmountCents: refundAmount,
		Currency:    rec.result.Currency,
		Status:      RefundStatusSucceeded,
		Reason:      req.Reason,
	}
	m.refunds[res.RefundID] = res
	copyRes := *res
	return &copyRes, nil
}

// CreateCheckoutSession returns a simulated hosted checkout session pointing directly to SuccessURL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Checkout session parameters.
//
// Example usage:
//
//	cs, err := mock.CreateCheckoutSession(ctx, req)
func (m *MockPaymentProvider) CreateCheckoutSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSessionResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	sessionID := fmt.Sprintf("mock_cs_%s", uuid.New().String())
	return &CheckoutSessionResult{
		SessionID:   sessionID,
		CheckoutURL: req.SuccessURL,
	}, nil
}

// GenerateSignedWebhook constructs a Stripe-shaped JSON payload and valid HMAC signature for tests.
//
// Arguments:
//   - event: Event fields to encode in the mock webhook payload.
//   - secret: Webhook signing secret.
//
// Example usage:
//
//	payload, sig := mock.GenerateSignedWebhook(evt, "whsec_test")
func (m *MockPaymentProvider) GenerateSignedWebhook(event WebhookEvent, secret string) ([]byte, string) {
	obj := map[string]interface{}{
		"id":       event.PaymentID,
		"amount":   event.AmountCents,
		"currency": event.Currency,
		"customer": event.CustomerID,
		"status":   string(event.Status),
	}
	if len(event.Metadata) > 0 {
		obj["metadata"] = event.Metadata
	}

	switch event.Type {
	case EventCheckoutSessionCompleted:
		if event.SessionID != "" {
			obj["id"] = event.SessionID
		}
		if event.PaymentID != "" {
			obj["payment_intent"] = event.PaymentID
		}
		if event.ClientReferenceID != "" {
			obj["client_reference_id"] = event.ClientReferenceID
		}
		obj["amount_total"] = event.AmountCents
	case EventChargeRefunded:
		if event.PaymentID != "" {
			obj["payment_intent"] = event.PaymentID
		}
		obj["amount_refunded"] = event.AmountCents
		obj["refunded"] = event.Status == "" || event.Status == PaymentStatusRefunded
		if event.RefundID != "" {
			obj["refund_id"] = event.RefundID
		}
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"type": string(event.Type),
		"data": map[string]interface{}{
			"object": obj,
		},
	})

	timestamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.%s", timestamp, string(payload))
	sigHex := hex.EncodeToString(mac.Sum(nil))

	return payload, fmt.Sprintf("t=%d,v1=%s", timestamp, sigHex)
}

// VerifyWebhookSignature validates a mock HMAC Stripe-Signature header and parses the webhook payload.
//
// Arguments:
//   - payload: Raw HTTP request body.
//   - signatureHeader: Stripe-Signature header value (t=...,v1=...).
//   - secret: Expected webhook signing secret.
//
// Example usage:
//
//	evt, err := mock.VerifyWebhookSignature(payload, sigHeader, "whsec_test")
func (m *MockPaymentProvider) VerifyWebhookSignature(payload []byte, signatureHeader string, secret string) (*WebhookEvent, error) {
	if signatureHeader == "" || secret == "" {
		return nil, errors.New("missing webhook signature or secret")
	}

	timestamp, signature := parseMockSignatureHeader(signatureHeader)
	if timestamp == "" || signature == "" {
		return nil, errors.New("malformed webhook signature header")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s.%s", timestamp, string(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return nil, errors.New("invalid webhook signature")
	}

	var root struct {
		Type string `json:"type"`
		Data struct {
			Object map[string]interface{} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("failed parsing webhook json: %w", err)
	}

	return parseStripeEventObject(EventType(root.Type), root.Data.Object), nil
}

func parseMockSignatureHeader(signatureHeader string) (string, string) {
	var timestamp string
	var signature string
	for _, part := range strings.Split(signatureHeader, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signature = kv[1]
		}
	}
	return timestamp, signature
}
