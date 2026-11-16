package billing

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
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/paymentintent"
	"github.com/stripe/stripe-go/v81/paymentmethod"
	"github.com/stripe/stripe-go/v81/refund"
	"github.com/stripe/stripe-go/v81/webhook"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

// PaymentResult captures the outcome of a financial transaction.
type PaymentResult struct {
	TransactionID string               `json:"transaction_id"`
	AmountCents   int64                `json:"amount_cents"`
	Status        domain.PaymentStatus `json:"status"`
	ErrorMessage  string               `json:"error_message,omitempty"`
}

// WebhookEvent represents an incoming payment lifecycle notification.
type WebhookEvent struct {
	Type         string               `json:"type"`
	PaymentID    string               `json:"payment_id"`
	AmountCents  int64                `json:"amount_cents"`
	CustomerID   string               `json:"customer_id"`
	Status       domain.PaymentStatus `json:"status"`
	SignatureKey string               `json:"-"`
}

// PaymentProvider defines payment operations for moving money and verifying webhook notifications.
// Subscriptions are intentionally managed by platform DB and cron, not through provider subscription APIs.
type PaymentProvider interface {
	CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error)
	GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error)
	Refund(ctx context.Context, paymentID string) error
	SavePaymentMethod(ctx context.Context, customerID string, paymentMethodID string) error
	ChargeSavedPaymentMethod(ctx context.Context, customerID string, paymentMethodID string, amountCents int64, description string) (*PaymentResult, error)
	VerifyWebhookSignature(payload []byte, signatureHeader string, secret string) (*WebhookEvent, error)
}

// MockPaymentProvider provides deterministic in-memory payment responses for local testing and development.
type MockPaymentProvider struct {
	mu          sync.RWMutex
	FailCharges bool
	payments    map[string]*PaymentResult
	methods     map[string]string // customerID -> paymentMethodID
}

// NewMockPaymentProvider constructs a mock payment provider with in-memory ledger.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	p := billing.NewMockPaymentProvider()
func NewMockPaymentProvider() *MockPaymentProvider {
	return &MockPaymentProvider{
		payments: make(map[string]*PaymentResult),
		methods:  make(map[string]string),
	}
}

func (m *MockPaymentProvider) CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	txID := fmt.Sprintf("mock_pi_%s", uuid.New().String())
	res := &PaymentResult{
		TransactionID: txID,
		AmountCents:   amountCents,
		Status:        domain.PaymentStatusSucceeded,
	}
	m.payments[txID] = res
	return res, nil
}

func (m *MockPaymentProvider) GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res, exists := m.payments[paymentID]
	if !exists {
		return nil, errors.New("mock payment not found")
	}
	return res, nil
}

func (m *MockPaymentProvider) Refund(ctx context.Context, paymentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	res, exists := m.payments[paymentID]
	if !exists {
		return errors.New("mock payment not found")
	}
	res.Status = domain.PaymentStatusRefunded
	return nil
}

func (m *MockPaymentProvider) SavePaymentMethod(ctx context.Context, customerID string, paymentMethodID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.methods[customerID] = paymentMethodID
	return nil
}

func (m *MockPaymentProvider) ChargeSavedPaymentMethod(ctx context.Context, customerID string, paymentMethodID string, amountCents int64, description string) (*PaymentResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	txID := fmt.Sprintf("mock_charge_%s", uuid.New().String())
	if m.FailCharges {
		res := &PaymentResult{
			TransactionID: txID,
			AmountCents:   amountCents,
			Status:        domain.PaymentStatusFailed,
			ErrorMessage:  "card_declined: insufficient funds",
		}
		m.payments[txID] = res
		return res, errors.New("payment failed: card_declined")
	}

	res := &PaymentResult{
		TransactionID: txID,
		AmountCents:   amountCents,
		Status:        domain.PaymentStatusSucceeded,
	}
	m.payments[txID] = res
	return res, nil
}

// GenerateSignedWebhook crafts a valid mock payload and HMAC signature for testing the webhook pipeline.
//
// Arguments:
//   - event: Desired event attributes to emit.
//   - secret: Webhook signing secret.
//
// Example usage:
//
//	payload, sig := mock.GenerateSignedWebhook(event, "whsec_test")
func (m *MockPaymentProvider) GenerateSignedWebhook(event WebhookEvent, secret string) ([]byte, string) {
	payload, _ := json.Marshal(map[string]interface{}{
		"type": event.Type,
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":       event.PaymentID,
				"amount":   event.AmountCents,
				"customer": event.CustomerID,
				"status":   string(event.Status),
			},
		},
	})

	timestamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.%s", timestamp, string(payload))
	sigHex := hex.EncodeToString(mac.Sum(nil))

	sigHeader := fmt.Sprintf("t=%d,v1=%s", timestamp, sigHex)
	return payload, sigHeader
}

func (m *MockPaymentProvider) VerifyWebhookSignature(payload []byte, signatureHeader string, secret string) (*WebhookEvent, error) {
	if signatureHeader == "" || secret == "" {
		return nil, errors.New("missing webhook signature or secret")
	}

	// Parse t=... and v1=...
	var timestamp string
	var signature string
	parts := strings.Split(signatureHeader, ",")
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 {
			switch kv[0] {
			case "t":
				timestamp = kv[1]
			case "v1":
				signature = kv[1]
			}
		}
	}

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
			Object struct {
				ID       string `json:"id"`
				Amount   int64  `json:"amount"`
				Customer string `json:"customer"`
				Status   string `json:"status"`
			} `json:"object"`
		} `json:"data"`
	}

	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("failed parsing webhook json: %w", err)
	}

	status := domain.PaymentStatusSucceeded
	if root.Type == "payment_intent.payment_failed" {
		status = domain.PaymentStatusFailed
	}

	return &WebhookEvent{
		Type:        root.Type,
		PaymentID:   root.Data.Object.ID,
		AmountCents: root.Data.Object.Amount,
		CustomerID:  root.Data.Object.Customer,
		Status:      status,
	}, nil
}

// StripePaymentProvider executes live payment requests against Stripe using PaymentIntents.
type StripePaymentProvider struct {
	apiKey string
}

// NewStripePaymentProvider constructs a live Stripe payment provider.
//
// Arguments:
//   - apiKey: Stripe secret API key (sk_live_... or sk_test_...).
//
// Example usage:
//
//	provider := billing.NewStripePaymentProvider(os.Getenv("STRIPE_SECRET_KEY"))
func NewStripePaymentProvider(apiKey string) *StripePaymentProvider {
	stripe.Key = apiKey
	return &StripePaymentProvider{apiKey: apiKey}
}

func (s *StripePaymentProvider) CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error) {
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(currency),
		Customer: stripe.String(customerID),
	}
	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe payment intent creation failed: %w", err)
	}

	return &PaymentResult{
		TransactionID: pi.ID,
		AmountCents:   pi.Amount,
		Status:        mapStripeStatus(pi.Status),
	}, nil
}

func (s *StripePaymentProvider) GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error) {
	pi, err := paymentintent.Get(paymentID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe payment intent lookup failed: %w", err)
	}
	return &PaymentResult{
		TransactionID: pi.ID,
		AmountCents:   pi.Amount,
		Status:        mapStripeStatus(pi.Status),
	}, nil
}

func (s *StripePaymentProvider) Refund(ctx context.Context, paymentID string) error {
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(paymentID),
	}
	_, err := refund.New(params)
	if err != nil {
		return fmt.Errorf("stripe refund failed: %w", err)
	}
	return nil
}

func (s *StripePaymentProvider) SavePaymentMethod(ctx context.Context, customerID string, paymentMethodID string) error {
	params := &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(customerID),
	}
	_, err := paymentmethod.Attach(paymentMethodID, params)
	if err != nil {
		return fmt.Errorf("failed attaching payment method: %w", err)
	}
	return nil
}

func (s *StripePaymentProvider) ChargeSavedPaymentMethod(ctx context.Context, customerID string, paymentMethodID string, amountCents int64, description string) (*PaymentResult, error) {
	params := &stripe.PaymentIntentParams{
		Amount:        stripe.Int64(amountCents),
		Currency:      stripe.String(string(stripe.CurrencyUSD)),
		Customer:      stripe.String(customerID),
		PaymentMethod: stripe.String(paymentMethodID),
		Confirm:       stripe.Bool(true),
		OffSession:    stripe.Bool(true),
		Description:   stripe.String(description),
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe recurring charge failed: %w", err)
	}

	return &PaymentResult{
		TransactionID: pi.ID,
		AmountCents:   pi.Amount,
		Status:        mapStripeStatus(pi.Status),
	}, nil
}

func (s *StripePaymentProvider) VerifyWebhookSignature(payload []byte, signatureHeader string, secret string) (*WebhookEvent, error) {
	event, err := webhook.ConstructEvent(payload, signatureHeader, secret)
	if err != nil {
		return nil, fmt.Errorf("stripe signature verification failed: %w", err)
	}

	var status domain.PaymentStatus
	var paymentID string
	var amount int64
	var customerID string

	switch event.Type {
	case "payment_intent.succeeded":
		status = domain.PaymentStatusSucceeded
		if pi, ok := event.Data.Object["id"].(string); ok {
			paymentID = pi
		}
		if amt, ok := event.Data.Object["amount"].(float64); ok {
			amount = int64(amt)
		}
		if cust, ok := event.Data.Object["customer"].(string); ok {
			customerID = cust
		}
	case "payment_intent.payment_failed":
		status = domain.PaymentStatusFailed
		if pi, ok := event.Data.Object["id"].(string); ok {
			paymentID = pi
		}
		if cust, ok := event.Data.Object["customer"].(string); ok {
			customerID = cust
		}
	default:
		status = domain.PaymentStatusPending
	}

	return &WebhookEvent{
		Type:        string(event.Type),
		PaymentID:   paymentID,
		AmountCents: amount,
		CustomerID:  customerID,
		Status:      status,
	}, nil
}

func mapStripeStatus(status stripe.PaymentIntentStatus) domain.PaymentStatus {
	switch status {
	case stripe.PaymentIntentStatusSucceeded:
		return domain.PaymentStatusSucceeded
	case stripe.PaymentIntentStatusCanceled:
		return domain.PaymentStatusFailed
	default:
		return domain.PaymentStatusPending
	}
}
