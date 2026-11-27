package stripekit

// RefundReason enumerates allowed Stripe refund reason values.
type RefundReason string

const (
	RefundReasonDuplicate           RefundReason = "duplicate"
	RefundReasonFraudulent          RefundReason = "fraudulent"
	RefundReasonRequestedByCustomer RefundReason = "requested_by_customer"
)

// IsValid reports whether the refund reason is empty or one of Stripe's supported reasons.
//
// Arguments:
//   - None (inspects the receiver value).
//
// Example usage:
//
//	if !req.Reason.IsValid() { return errors.New("invalid refund reason") }
func (r RefundReason) IsValid() bool {
	switch r {
	case "", RefundReasonDuplicate, RefundReasonFraudulent, RefundReasonRequestedByCustomer:
		return true
	default:
		return false
	}
}
