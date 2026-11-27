# go-stripe-kit

Reusable Go toolkit for Stripe Checkout, PaymentIntents, full/partial refunds, and signature-verified webhook dispatch, with HTTP framework adapters kept separate from core payment logic.

## Architecture

- Root package (`github.com/dreamoutbox/go-stripe-kit`, package `stripekit`):
  - Framework-agnostic payment logic with zero web-framework or database dependencies.
  - Typed enums: `PaymentStatus`, `EventType`, `RefundReason`, `RefundStatus`.
  - `PaymentProvider` interface with `StripePaymentProvider` (instance-scoped `stripe-go/v81` client) and `MockPaymentProvider` (deterministic in-memory ledger + signed webhook helper for tests).
  - `Service` orchestrating checkout sessions, direct payments, full/partial refunds (with optional `RefundHook`), and callback-based webhook dispatch (`OnEvent` / `OnWebhook`).
- Gin adapter (`github.com/dreamoutbox/go-stripe-kit/ginapi`):
  - Thin HTTP transport layer exposing `Webhook` (`POST /webhook`) and `Refund` (`POST /refund`) handlers without embedding business logic or auth middleware.
  - Designed so additional adapters (`echoapi`, `fiberapi`, `stdhttp`) can wrap the same `*stripekit.Service` without duplicating logic.

## Core Usage

```go
provider := stripekit.NewStripePaymentProvider(os.Getenv("STRIPE_SECRET_KEY"))
// For local development or tests:
// provider := stripekit.NewMockPaymentProvider()

svc := stripekit.NewService(provider, os.Getenv("STRIPE_WEBHOOK_SECRET"))

svc.OnEvent(stripekit.EventCheckoutSessionCompleted, func(ctx context.Context, evt *stripekit.WebhookEvent) error {
	orderID := evt.Metadata["order_id"]
	// Fulfill order idempotently using orderID / evt.SessionID / evt.PaymentID.
	return nil
})

svc.OnEvent(stripekit.EventChargeRefunded, func(ctx context.Context, evt *stripekit.WebhookEvent) error {
	// Sync local payment record status using evt.PaymentID and evt.Status.
	return nil
})

// Create a hosted Stripe Checkout session
cs, err := svc.CreateCheckoutSession(ctx, stripekit.CheckoutSessionRequest{
	ClientReferenceID: "42",
	Currency:          stripekit.DefaultCurrency,
	CustomerEmail:     "buyer@example.com",
	ProductName:       "Pro Plan",
	Description:       "Monthly subscription",
	AmountCents:       1500,
	SuccessURL:        "https://app.example.com/success",
	CancelURL:         "https://app.example.com/cancel",
	Metadata:          map[string]string{"order_id": "42"},
})

// Issue a full refund (AmountCents == 0) or partial refund (AmountCents > 0)
refundRes, err := svc.Refund(ctx, stripekit.RefundRequest{
	PaymentID:      "pi_123",
	AmountCents:    500,
	Reason:         stripekit.RefundReasonRequestedByCustomer,
	IdempotencyKey: "refund-order-42-part-1",
})
```

## Gin Adapter Usage

```go
stripeGin := ginapi.NewHandler(svc)

// Mount unauthenticated, signature-verified webhook route (add your own rate limiter)
billingGroup := router.Group("/api/v1/billing")
stripeGin.RegisterWebhookRoute(billingGroup)

// Mount refund route behind your own admin authentication/authorization middleware
adminBilling := router.Group("/api/v1/admin/billing")
adminBilling.Use(yourAdminAuthMiddleware)
stripeGin.RegisterRefundRoute(adminBilling)
```

## Adding Another Web Framework Adapter

Create a sibling package (for example `echoapi/` or `fiberapi/`) that accepts `ginapi.BillingService`-style minimal methods (`HandleWebhook`, `Refund`) on `*stripekit.Service`:

1. Read `Stripe-Signature` header and bounded raw request body, then call `svc.HandleWebhook(ctx, payload, sigHeader)`.
2. Bind JSON into `stripekit.RefundRequest`, call `req.Validate()`, then call `svc.Refund(ctx, req)`.

## Publishing to a Standalone GitHub Repository

1. Initialize a new repository at `github.com/dreamoutbox/go-stripe-kit` with the contents of this directory and push a tagged release (e.g. `v0.1.0`).
2. In consuming repositories:
   - Remove `./go-stripe-kit` and its `replace` entry from `go.work`.
   - Remove the local `replace github.com/dreamoutbox/go-stripe-kit => ./go-stripe-kit` line from `go.mod`.
   - Run `go get github.com/dreamoutbox/go-stripe-kit@v0.1.0 && go mod tidy`.
   - Remove `COPY go-stripe-kit/go.* ./go-stripe-kit/` from Dockerfiles.
