package stripekit

import (
	"errors"
	"strings"
)

// RefundRequest holds parameters for issuing a full or partial refund against a payment intent.
// When AmountCents is 0, the full remaining amount of the payment is refunded.
type RefundRequest struct {
	PaymentID      string            `json:"payment_id"`
	AmountCents    int64             `json:"amount_cents,omitempty"`
	Reason         RefundReason      `json:"reason,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// Validate checks that a refund request contains a valid payment ID, non-negative amount, and allowed reason.
//
// Arguments:
//   - None (validates the receiver fields).
//
// Example usage:
//
//	if err := req.Validate(); err != nil { return nil, err }
func (r RefundRequest) Validate() error {
	if strings.TrimSpace(r.PaymentID) == "" {
		return errors.New("payment_id is required")
	}
	if r.AmountCents < 0 {
		return errors.New("amount_cents must not be negative")
	}
	if !r.Reason.IsValid() {
		return errors.New("invalid refund reason; allowed: duplicate, fraudulent, requested_by_customer")
	}
	return nil
}

// RefundResult captures the outcome of a refund operation.
type RefundResult struct {
	RefundID    string       `json:"refund_id"`
	PaymentID   string       `json:"payment_id"`
	AmountCents int64        `json:"amount_cents"`
	Currency    string       `json:"currency,omitempty"`
	Status      RefundStatus `json:"status"`
	Reason      RefundReason `json:"reason,omitempty"`
}
