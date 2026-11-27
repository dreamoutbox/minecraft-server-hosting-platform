package stripekit

import (
	"context"
	"strings"
	"testing"
)

func TestMockProviderCreateGetAndCheckout(t *testing.T) {
	ctx := context.Background()
	mock := NewMockPaymentProvider()

	res, err := mock.CreatePayment(ctx, 2500, "", "cus_123")
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}
	if !strings.HasPrefix(res.TransactionID, "mock_pi_") || res.Currency != DefaultCurrency || res.Status != PaymentStatusSucceeded {
		t.Fatalf("unexpected CreatePayment result: %+v", res)
	}

	fetched, err := mock.GetPayment(ctx, res.TransactionID)
	if err != nil {
		t.Fatalf("GetPayment failed: %v", err)
	}
	if fetched.AmountCents != 2500 || fetched.TransactionID != res.TransactionID {
		t.Fatalf("unexpected GetPayment result: %+v", fetched)
	}

	if _, err := mock.GetPayment(ctx, "pi_missing"); err == nil {
		t.Fatal("expected error for unknown payment ID")
	}

	cs, err := mock.CreateCheckoutSession(ctx, CheckoutSessionRequest{
		ClientReferenceID: "order-42",
		CustomerEmail:     "buyer@example.com",
		ProductName:       "Pro Plan",
		AmountCents:       1500,
		SuccessURL:        "https://example.com/success",
		CancelURL:         "https://example.com/cancel",
		Metadata:          map[string]string{"order_id": "42"},
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession failed: %v", err)
	}
	if !strings.HasPrefix(cs.SessionID, "mock_cs_") || cs.CheckoutURL != "https://example.com/success" {
		t.Fatalf("unexpected checkout session result: %+v", cs)
	}

	if _, err := mock.CreateCheckoutSession(ctx, CheckoutSessionRequest{}); err == nil {
		t.Fatal("expected validation error for empty checkout session request")
	}
}

func TestMockProviderFullAndPartialRefunds(t *testing.T) {
	ctx := context.Background()
	mock := NewMockPaymentProvider()

	// 1. Full refund (AmountCents == 0)
	fullPay, err := mock.CreatePayment(ctx, 2000, "usd", "cus_full")
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}

	fullRes, err := mock.Refund(ctx, RefundRequest{
		PaymentID: fullPay.TransactionID,
		Reason:    RefundReasonRequestedByCustomer,
	})
	if err != nil {
		t.Fatalf("full Refund failed: %v", err)
	}
	if !strings.HasPrefix(fullRes.RefundID, "mock_re_") || fullRes.AmountCents != 2000 || fullRes.Status != RefundStatusSucceeded {
		t.Fatalf("unexpected full refund result: %+v", fullRes)
	}

	afterFull, err := mock.GetPayment(ctx, fullPay.TransactionID)
	if err != nil {
		t.Fatalf("GetPayment after full refund failed: %v", err)
	}
	if afterFull.Status != PaymentStatusRefunded {
		t.Fatalf("expected payment status %s after full refund, got %s", PaymentStatusRefunded, afterFull.Status)
	}

	// Second refund on already fully refunded payment must fail
	if _, err := mock.Refund(ctx, RefundRequest{PaymentID: fullPay.TransactionID}); err == nil {
		t.Fatal("expected error refunding already fully refunded payment")
	}

	// 2. Partial refunds summing to full amount
	partPay, err := mock.CreatePayment(ctx, 3000, "eur", "cus_part")
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}

	r1, err := mock.Refund(ctx, RefundRequest{
		PaymentID:   partPay.TransactionID,
		AmountCents: 1000,
		Reason:      RefundReasonDuplicate,
	})
	if err != nil {
		t.Fatalf("first partial refund failed: %v", err)
	}
	if r1.AmountCents != 1000 || r1.Currency != "eur" {
		t.Fatalf("unexpected first partial refund: %+v", r1)
	}

	midState, _ := mock.GetPayment(ctx, partPay.TransactionID)
	if midState.Status != PaymentStatusSucceeded {
		t.Fatalf("expected payment still %s after partial refund, got %s", PaymentStatusSucceeded, midState.Status)
	}

	// Over-refund must fail
	if _, err := mock.Refund(ctx, RefundRequest{PaymentID: partPay.TransactionID, AmountCents: 2500}); err == nil {
		t.Fatal("expected error for over-refund exceeding remaining balance")
	}

	// Refund remaining balance via AmountCents == 0
	r2, err := mock.Refund(ctx, RefundRequest{PaymentID: partPay.TransactionID})
	if err != nil {
		t.Fatalf("remaining refund failed: %v", err)
	}
	if r2.AmountCents != 2000 {
		t.Fatalf("expected remaining refund 2000, got %d", r2.AmountCents)
	}

	finalState, _ := mock.GetPayment(ctx, partPay.TransactionID)
	if finalState.Status != PaymentStatusRefunded {
		t.Fatalf("expected payment status %s once fully refunded, got %s", PaymentStatusRefunded, finalState.Status)
	}
}

func TestRefundRequestValidation(t *testing.T) {
	ctx := context.Background()
	mock := NewMockPaymentProvider()

	cases := []struct {
		name string
		req  RefundRequest
	}{
		{name: "missing payment id", req: RefundRequest{AmountCents: 100}},
		{name: "negative amount", req: RefundRequest{PaymentID: "pi_1", AmountCents: -50}},
		{name: "invalid reason", req: RefundRequest{PaymentID: "pi_1", Reason: RefundReason("unknown_reason")}},
		{name: "unknown payment id", req: RefundRequest{PaymentID: "pi_does_not_exist"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mock.Refund(ctx, tc.req); err == nil {
				t.Fatalf("expected error for case %q", tc.name)
			}
		})
	}
}

func TestMockProviderWebhookVerification(t *testing.T) {
	mock := NewMockPaymentProvider()
	secret := "whsec_unit_test"

	// 1. Checkout completed with generic metadata
	checkoutEvt := WebhookEvent{
		Type:              EventCheckoutSessionCompleted,
		SessionID:         "cs_test_123",
		PaymentID:         "pi_test_123",
		ClientReferenceID: "ref_99",
		AmountCents:       4900,
		Currency:          "usd",
		CustomerID:        "cus_1",
		Status:            PaymentStatusSucceeded,
		Metadata:          map[string]string{"order_id": "99", "plan": "large"},
	}
	payload, sig := mock.GenerateSignedWebhook(checkoutEvt, secret)

	if _, err := mock.VerifyWebhookSignature(payload, "", secret); err == nil {
		t.Fatal("expected error for missing signature")
	}
	if _, err := mock.VerifyWebhookSignature(payload, "t=123,v1=tampered", secret); err == nil {
		t.Fatal("expected error for tampered signature")
	}

	parsed, err := mock.VerifyWebhookSignature(payload, sig, secret)
	if err != nil {
		t.Fatalf("VerifyWebhookSignature failed: %v", err)
	}
	if parsed.Type != EventCheckoutSessionCompleted || parsed.SessionID != "cs_test_123" || parsed.PaymentID != "pi_test_123" {
		t.Fatalf("unexpected parsed checkout event: %+v", parsed)
	}
	if parsed.ClientReferenceID != "ref_99" || parsed.AmountCents != 4900 || parsed.Metadata["order_id"] != "99" {
		t.Fatalf("unexpected checkout metadata/reference fields: %+v", parsed)
	}

	// 2. Charge refunded webhook
	refundEvt := WebhookEvent{
		Type:        EventChargeRefunded,
		PaymentID:   "pi_test_123",
		RefundID:    "re_test_456",
		AmountCents: 4900,
		Currency:    "usd",
		Status:      PaymentStatusRefunded,
	}
	refPayload, refSig := mock.GenerateSignedWebhook(refundEvt, secret)
	parsedRefund, err := mock.VerifyWebhookSignature(refPayload, refSig, secret)
	if err != nil {
		t.Fatalf("VerifyWebhookSignature for refund failed: %v", err)
	}
	if parsedRefund.Type != EventChargeRefunded || parsedRefund.PaymentID != "pi_test_123" || parsedRefund.RefundID != "re_test_456" || parsedRefund.Status != PaymentStatusRefunded {
		t.Fatalf("unexpected parsed refund event: %+v", parsedRefund)
	}

	// 3. Payment intent failed webhook
	failEvt := WebhookEvent{
		Type:        EventPaymentIntentPaymentFailed,
		PaymentID:   "pi_fail_1",
		AmountCents: 1200,
		Status:      PaymentStatusFailed,
	}
	failPayload, failSig := mock.GenerateSignedWebhook(failEvt, secret)
	parsedFail, err := mock.VerifyWebhookSignature(failPayload, failSig, secret)
	if err != nil {
		t.Fatalf("VerifyWebhookSignature for payment failure failed: %v", err)
	}
	if parsedFail.Status != PaymentStatusFailed || parsedFail.PaymentID != "pi_fail_1" {
		t.Fatalf("unexpected parsed failure event: %+v", parsedFail)
	}
}
