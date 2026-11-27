package stripekit

// PaymentResult captures the outcome of a payment intent transaction.
type PaymentResult struct {
	TransactionID string        `json:"transaction_id"`
	AmountCents   int64         `json:"amount_cents"`
	Currency      string        `json:"currency,omitempty"`
	Status        PaymentStatus `json:"status"`
	ErrorMessage  string        `json:"error_message,omitempty"`
}
