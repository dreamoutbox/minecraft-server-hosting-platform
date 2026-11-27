package stripekit

// WebhookEvent represents a verified, normalized payment lifecycle event received from Stripe.
type WebhookEvent struct {
	Type              EventType         `json:"type"`
	PaymentID         string            `json:"payment_id,omitempty"`
	SessionID         string            `json:"session_id,omitempty"`
	RefundID          string            `json:"refund_id,omitempty"`
	ClientReferenceID string            `json:"client_reference_id,omitempty"`
	AmountCents       int64             `json:"amount_cents"`
	Currency          string            `json:"currency,omitempty"`
	CustomerID        string            `json:"customer_id,omitempty"`
	Status            PaymentStatus     `json:"status"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}
