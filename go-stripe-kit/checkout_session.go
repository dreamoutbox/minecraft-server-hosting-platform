package stripekit

import "errors"

// DefaultCurrency is used when CheckoutSessionRequest.Currency is omitted.
const DefaultCurrency = "usd"

// CheckoutSessionRequest holds parameters for creating a hosted Stripe Checkout session.
type CheckoutSessionRequest struct {
	ClientReferenceID string            `json:"client_reference_id,omitempty"`
	Currency          string            `json:"currency,omitempty"`
	CustomerEmail     string            `json:"customer_email,omitempty"`
	ProductName       string            `json:"product_name"`
	Description       string            `json:"description,omitempty"`
	AmountCents       int64             `json:"amount_cents"`
	SuccessURL        string            `json:"success_url"`
	CancelURL         string            `json:"cancel_url"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// Validate checks required fields on a checkout session request.
//
// Arguments:
//   - None (validates the receiver fields).
//
// Example usage:
//
//	if err := req.Validate(); err != nil { return nil, err }
func (r CheckoutSessionRequest) Validate() error {
	if r.ProductName == "" {
		return errors.New("product_name is required")
	}
	if r.AmountCents <= 0 {
		return errors.New("amount_cents must be greater than zero")
	}
	if r.SuccessURL == "" {
		return errors.New("success_url is required")
	}
	if r.CancelURL == "" {
		return errors.New("cancel_url is required")
	}
	return nil
}

// CheckoutSessionResult holds the created hosted checkout session ID and redirect URL.
type CheckoutSessionResult struct {
	SessionID   string `json:"session_id"`
	CheckoutURL string `json:"checkout_url"`
}
