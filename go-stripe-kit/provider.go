package stripekit

import "context"

// PaymentProvider defines payment operations for hosted checkout, payment intents, refunds, and webhook verification.
type PaymentProvider interface {
	CreatePayment(ctx context.Context, amountCents int64, currency string, customerID string) (*PaymentResult, error)
	GetPayment(ctx context.Context, paymentID string) (*PaymentResult, error)
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
	CreateCheckoutSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSessionResult, error)
	VerifyWebhookSignature(payload []byte, signatureHeader string, secret string) (*WebhookEvent, error)
}
