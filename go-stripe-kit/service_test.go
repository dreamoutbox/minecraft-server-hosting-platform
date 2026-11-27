package stripekit

import (
	"context"
	"errors"
	"testing"
)

func TestServiceCheckoutAndRefundWithHook(t *testing.T) {
	ctx := context.Background()
	mock := NewMockPaymentProvider()
	svc := NewService(mock, "whsec_service_test")

	cs, err := svc.CreateCheckoutSession(ctx, CheckoutSessionRequest{
		ProductName: "Starter",
		AmountCents: 999,
		SuccessURL:  "https://app.example.com/ok",
		CancelURL:   "https://app.example.com/cancel",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession failed: %v", err)
	}
	if cs.SessionID == "" {
		t.Fatal("expected non-empty checkout session ID")
	}

	pay, err := svc.CreatePayment(ctx, 1500, "usd", "cus_svc")
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}

	var hookResult *RefundResult
	svc.SetRefundHook(func(ctx context.Context, res *RefundResult) error {
		hookResult = res
		return nil
	})

	refRes, err := svc.Refund(ctx, RefundRequest{
		PaymentID:   pay.TransactionID,
		AmountCents: 500,
		Reason:      RefundReasonRequestedByCustomer,
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if hookResult == nil || hookResult.RefundID != refRes.RefundID || hookResult.AmountCents != 500 {
		t.Fatalf("expected refund hook to receive %+v, got %+v", refRes, hookResult)
	}

	// Refund hook failure should propagate error alongside result
	svc.SetRefundHook(func(ctx context.Context, res *RefundResult) error {
		return errors.New("ledger write failed")
	})
	if res, err := svc.Refund(ctx, RefundRequest{PaymentID: pay.TransactionID, AmountCents: 1000}); err == nil || res == nil {
		t.Fatalf("expected hook error with non-nil refund result, got res=%+v err=%v", res, err)
	}
}

func TestServiceWebhookDispatch(t *testing.T) {
	ctx := context.Background()
	mock := NewMockPaymentProvider()
	secret := "whsec_dispatch"
	svc := NewService(mock, secret)

	var order []string
	svc.OnEvent(EventCheckoutSessionCompleted, func(ctx context.Context, evt *WebhookEvent) error {
		order = append(order, "specific:"+evt.SessionID)
		return nil
	})
	svc.OnEvent(EventChargeRefunded, func(ctx context.Context, evt *WebhookEvent) error {
		order = append(order, "refund:"+evt.PaymentID)
		return nil
	})
	svc.OnWebhook(func(ctx context.Context, evt *WebhookEvent) error {
		order = append(order, "catchall:"+string(evt.Type))
		return nil
	})

	payload, sig := mock.GenerateSignedWebhook(WebhookEvent{
		Type:        EventCheckoutSessionCompleted,
		SessionID:   "cs_dispatch_1",
		PaymentID:   "pi_dispatch_1",
		AmountCents: 2500,
		Status:      PaymentStatusSucceeded,
	}, secret)

	// Tampered signature must fail before invoking any handlers
	if _, err := svc.HandleWebhook(ctx, payload, "t=1,v1=bad"); err == nil {
		t.Fatal("expected webhook signature verification error")
	}
	if len(order) != 0 {
		t.Fatalf("expected no handlers invoked on invalid signature, got %v", order)
	}

	evt, err := svc.HandleWebhook(ctx, payload, sig)
	if err != nil {
		t.Fatalf("HandleWebhook failed: %v", err)
	}
	if evt.SessionID != "cs_dispatch_1" {
		t.Fatalf("unexpected returned event: %+v", evt)
	}
	if len(order) != 2 || order[0] != "specific:cs_dispatch_1" || order[1] != "catchall:checkout.session.completed" {
		t.Fatalf("unexpected handler execution order: %v", order)
	}

	// Handler error stops dispatch and returns error
	svc.OnEvent(EventPaymentIntentPaymentFailed, func(ctx context.Context, evt *WebhookEvent) error {
		return errors.New("downstream failure")
	})
	failPayload, failSig := mock.GenerateSignedWebhook(WebhookEvent{
		Type:      EventPaymentIntentPaymentFailed,
		PaymentID: "pi_fail",
		Status:    PaymentStatusFailed,
	}, secret)
	if _, err := svc.HandleWebhook(ctx, failPayload, failSig); err == nil {
		t.Fatal("expected downstream handler error to propagate")
	}
}

func TestServiceValidationGuards(t *testing.T) {
	ctx := context.Background()
	nilSvc := NewService(nil, "whsec_nil")

	if _, err := nilSvc.CreatePayment(ctx, 100, "usd", ""); err == nil {
		t.Fatal("expected error with nil provider on CreatePayment")
	}
	if _, err := nilSvc.GetPayment(ctx, "pi_1"); err == nil {
		t.Fatal("expected error with nil provider on GetPayment")
	}
	if _, err := nilSvc.CreateCheckoutSession(ctx, CheckoutSessionRequest{}); err == nil {
		t.Fatal("expected error with nil provider on CreateCheckoutSession")
	}
	if _, err := nilSvc.Refund(ctx, RefundRequest{PaymentID: "pi_1"}); err == nil {
		t.Fatal("expected error with nil provider on Refund")
	}
	if _, err := nilSvc.HandleWebhook(ctx, nil, ""); err == nil {
		t.Fatal("expected error with nil provider on HandleWebhook")
	}

	svc := NewService(NewMockPaymentProvider(), "whsec_ok")
	if _, err := svc.CreatePayment(ctx, 0, "usd", ""); err == nil {
		t.Fatal("expected error for zero amount")
	}
	if _, err := svc.GetPayment(ctx, " "); err == nil {
		t.Fatal("expected error for empty payment ID")
	}
}
