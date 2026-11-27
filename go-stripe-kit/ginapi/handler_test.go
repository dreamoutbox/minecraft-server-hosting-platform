package ginapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	stripekit "github.com/dreamoutbox/go-stripe-kit"
	"github.com/gin-gonic/gin"
)

func setupGinTestRouter(secret string) (*gin.Engine, *stripekit.Service, *stripekit.MockPaymentProvider) {
	gin.SetMode(gin.TestMode)
	mock := stripekit.NewMockPaymentProvider()
	svc := stripekit.NewService(mock, secret)
	handler := NewHandler(svc)

	r := gin.New()
	group := r.Group("/api/v1/billing")
	handler.RegisterWebhookRoute(group)
	handler.RegisterRefundRoute(group)
	return r, svc, mock
}

func TestGinWebhookRoute(t *testing.T) {
	secret := "whsec_gin_test"
	r, svc, mock := setupGinTestRouter(secret)

	var handledID string
	svc.OnEvent(stripekit.EventPaymentIntentSucceeded, func(ctx context.Context, evt *stripekit.WebhookEvent) error {
		handledID = evt.PaymentID
		return nil
	})

	payload, sig := mock.GenerateSignedWebhook(stripekit.WebhookEvent{
		Type:        stripekit.EventPaymentIntentSucceeded,
		PaymentID:   "pi_gin_123",
		AmountCents: 1500,
		Status:      stripekit.PaymentStatusSucceeded,
	}, secret)

	// 1. Missing signature header returns 400
	wNoSig := httptest.NewRecorder()
	reqNoSig, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(payload))
	r.ServeHTTP(wNoSig, reqNoSig)
	if wNoSig.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing signature, got %d", wNoSig.Code)
	}

	// 2. Tampered signature returns 400
	wBadSig := httptest.NewRecorder()
	reqBadSig, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(payload))
	reqBadSig.Header.Set("Stripe-Signature", "t=123,v1=tampered")
	r.ServeHTTP(wBadSig, reqBadSig)
	if wBadSig.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for tampered signature, got %d", wBadSig.Code)
	}

	// 3. Valid signature returns 200 and invokes registered callback
	wGood := httptest.NewRecorder()
	reqGood, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(payload))
	reqGood.Header.Set("Stripe-Signature", sig)
	r.ServeHTTP(wGood, reqGood)
	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid webhook, got %d: %s", wGood.Code, wGood.Body.String())
	}
	if handledID != "pi_gin_123" {
		t.Fatalf("expected callback to handle pi_gin_123, got %q", handledID)
	}

	// 4. Handler error returns 400
	svc.OnEvent(stripekit.EventPaymentIntentPaymentFailed, func(ctx context.Context, evt *stripekit.WebhookEvent) error {
		return errors.New("fulfillment failed")
	})
	failPayload, failSig := mock.GenerateSignedWebhook(stripekit.WebhookEvent{
		Type:      stripekit.EventPaymentIntentPaymentFailed,
		PaymentID: "pi_gin_fail",
		Status:    stripekit.PaymentStatusFailed,
	}, secret)
	wErr := httptest.NewRecorder()
	reqErr, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(failPayload))
	reqErr.Header.Set("Stripe-Signature", failSig)
	r.ServeHTTP(wErr, reqErr)
	if wErr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on webhook handler error, got %d", wErr.Code)
	}
}

func TestGinRefundRoute(t *testing.T) {
	r, svc, _ := setupGinTestRouter("whsec_gin_refund")
	ctx := context.Background()

	pay, err := svc.CreatePayment(ctx, 2000, "usd", "cus_refund")
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}

	// 1. Invalid JSON returns 400
	wBadJSON := httptest.NewRecorder()
	reqBadJSON, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/refund", bytes.NewBufferString("{bad"))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wBadJSON, reqBadJSON)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", wBadJSON.Code)
	}

	// 2. Invalid reason / missing payment_id returns 400
	invalidBody, _ := json.Marshal(stripekit.RefundRequest{
		PaymentID: pay.TransactionID,
		Reason:    stripekit.RefundReason("not_valid"),
	})
	wInvalid := httptest.NewRecorder()
	reqInvalid, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/refund", bytes.NewReader(invalidBody))
	reqInvalid.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wInvalid, reqInvalid)
	if wInvalid.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid refund reason, got %d", wInvalid.Code)
	}

	// 3. Partial refund returns 200 with RefundResult
	partBody, _ := json.Marshal(stripekit.RefundRequest{
		PaymentID:   pay.TransactionID,
		AmountCents: 600,
		Reason:      stripekit.RefundReasonRequestedByCustomer,
	})
	wPart := httptest.NewRecorder()
	reqPart, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/refund", bytes.NewReader(partBody))
	reqPart.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wPart, reqPart)
	if wPart.Code != http.StatusOK {
		t.Fatalf("expected 200 for partial refund, got %d: %s", wPart.Code, wPart.Body.String())
	}
	var partRes stripekit.RefundResult
	if err := json.Unmarshal(wPart.Body.Bytes(), &partRes); err != nil {
		t.Fatalf("unmarshal refund result failed: %v", err)
	}
	if partRes.AmountCents != 600 || partRes.Status != stripekit.RefundStatusSucceeded {
		t.Fatalf("unexpected partial refund response: %+v", partRes)
	}

	// 4. Full remaining refund returns 200
	fullBody, _ := json.Marshal(stripekit.RefundRequest{
		PaymentID: pay.TransactionID,
	})
	wFull := httptest.NewRecorder()
	reqFull, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/refund", bytes.NewReader(fullBody))
	reqFull.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wFull, reqFull)
	if wFull.Code != http.StatusOK {
		t.Fatalf("expected 200 for remaining full refund, got %d: %s", wFull.Code, wFull.Body.String())
	}
	var fullRes stripekit.RefundResult
	if err := json.Unmarshal(wFull.Body.Bytes(), &fullRes); err != nil {
		t.Fatalf("unmarshal full refund result failed: %v", err)
	}
	if fullRes.AmountCents != 1400 {
		t.Fatalf("expected remaining 1400 refunded, got %d", fullRes.AmountCents)
	}

	// 5. Unknown payment ID returns 502
	unknownBody, _ := json.Marshal(stripekit.RefundRequest{PaymentID: "pi_unknown"})
	wUnknown := httptest.NewRecorder()
	reqUnknown, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/refund", bytes.NewReader(unknownBody))
	reqUnknown.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wUnknown, reqUnknown)
	if wUnknown.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for provider refund failure, got %d", wUnknown.Code)
	}
}
