package stripekit

import "context"

// WebhookHandler processes a verified Stripe webhook event.
type WebhookHandler func(ctx context.Context, event *WebhookEvent) error

// RefundHook runs after a refund succeeds at the payment provider.
type RefundHook func(ctx context.Context, result *RefundResult) error
