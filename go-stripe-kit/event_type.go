package stripekit

// EventType enumerates Stripe webhook event types handled by stripekit.
type EventType string

const (
	EventCheckoutSessionCompleted   EventType = "checkout.session.completed"
	EventPaymentIntentSucceeded     EventType = "payment_intent.succeeded"
	EventPaymentIntentPaymentFailed EventType = "payment_intent.payment_failed"
	EventChargeRefunded             EventType = "charge.refunded"
)
