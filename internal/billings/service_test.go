package billings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dreamoutbox/go-authkit"
	"github.com/dreamoutbox/go-authkit/authkitjwt"
	stripekit "github.com/dreamoutbox/go-stripe-kit"
	"github.com/dreamoutbox/go-stripe-kit/ginapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

func setupBillingTestEnv(t *testing.T) (*Service, *stripekit.MockPaymentProvider, *domain.User, *domain.Server, *domain.Plan, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil, nil, nil, nil, nil, ""
	}
	_ = domain.AutoMigrate(db)
	_ = domain.SeedPlans(db)

	mockProvider := stripekit.NewMockPaymentProvider()
	webhookSecret := "whsec_test_secret_123"
	stripeSvc := stripekit.NewService(mockProvider, webhookSecret)
	service := NewService(db, stripeSvc)

	user := domain.User{
		User: authkit.User{
			Email:        fmt.Sprintf("billing-user-%d@example.com", time.Now().UnixNano()),
			PasswordHash: "hashed",
			Role:         domain.UserRoleUser,
		},
	}
	_ = db.Create(&user)

	var plan domain.Plan
	_ = db.Where("name = ?", "Small").First(&plan)

	host := domain.Host{
		Hostname:      fmt.Sprintf("billing-host-%d", time.Now().UnixNano()),
		WorkerBaseURL: "http://127.0.0.1:9090",
		APIKey:        "k",
		APIKeyHash:    "h",
		MaxServers:    10,
		Status:        domain.HostStatusActive,
	}
	_ = db.Create(&host)

	server := domain.Server{
		UserID:    user.ID,
		PlanID:    plan.ID,
		HostID:    host.ID,
		Name:      "BilledServer",
		Port:      25540 + int(time.Now().UnixNano()%10),
		Status:    domain.ServerStatusRunning,
		MountPath: "/var/lib/mounts/billed",
	}
	db.Where("port = ?", server.Port).Delete(&domain.Server{})
	_ = db.Create(&server)

	jwtSecret := "billing-jwt-secret"
	tokens, _ := authkitjwt.GenerateTokenPair(user.ID, user.Email, user.Role, jwtSecret)

	return service, mockProvider, &user, &server, &plan, tokens.AccessToken
}

func TestWebhookSignatureVerification(t *testing.T) {
	service, mockProvider, _, _, _, _ := setupBillingTestEnv(t)
	if service == nil {
		return
	}

	secret := service.StripeService().WebhookSecret()
	apiHandler := NewAPIHandler(service, ginapi.NewHandler(service.StripeService()))
	r := gin.New()
	apiHandler.RegisterRoutes(r, "jwt-secret")

	evt := stripekit.WebhookEvent{
		Type:        stripekit.EventPaymentIntentSucceeded,
		PaymentID:   "pi_test_abc123",
		AmountCents: 1000,
		CustomerID:  "cust_test_1",
		Status:      stripekit.PaymentStatusSucceeded,
	}

	validPayload, validSig := mockProvider.GenerateSignedWebhook(evt, secret)

	// 1. Missing signature header returns 400
	wNoSig := httptest.NewRecorder()
	reqNoSig, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(validPayload))
	r.ServeHTTP(wNoSig, reqNoSig)
	if wNoSig.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing signature, got %d", wNoSig.Code)
	}

	// 2. Tampered signature returns 400
	wTampered := httptest.NewRecorder()
	reqTampered, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(validPayload))
	reqTampered.Header.Set("Stripe-Signature", "t=12345,v1=bad_signature_hash")
	r.ServeHTTP(wTampered, reqTampered)
	if wTampered.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for tampered signature, got %d", wTampered.Code)
	}

	// 3. Valid signature returns 200
	wGood := httptest.NewRecorder()
	reqGood, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/webhook", bytes.NewReader(validPayload))
	reqGood.Header.Set("Stripe-Signature", validSig)
	r.ServeHTTP(wGood, reqGood)
	if wGood.Code != http.StatusOK {
		t.Errorf("expected 200 for valid signed webhook, got %d. Body: %s", wGood.Code, wGood.Body.String())
	}
}

func TestBillingHTTPRoutesAndRefundWebhookSync(t *testing.T) {
	service, mockProvider, user, srv, plan, token := setupBillingTestEnv(t)
	if service == nil {
		return
	}

	jq := &mockCheckoutJobQueue{db: service.db, hostID: srv.HostID}
	service.SetJobQueue(jq)
	cs, order, err := service.CreateServerCheckout(context.Background(), user.ID, plan.ID, "OrderHistoryTest", domain.ServerTypeVanilla, domain.DefaultServerVersion)
	if err != nil {
		t.Fatalf("CreateServerCheckout failed: %v", err)
	}

	paymentIntentID := fmt.Sprintf("pi_routes_%d", time.Now().UnixNano())
	secret := service.StripeService().WebhookSecret()
	payload, sig := mockProvider.GenerateSignedWebhook(stripekit.WebhookEvent{
		Type:              stripekit.EventCheckoutSessionCompleted,
		SessionID:         cs.SessionID,
		PaymentID:         paymentIntentID,
		ClientReferenceID: fmt.Sprintf("%d", order.ID),
		AmountCents:       plan.PriceCents,
		Status:            stripekit.PaymentStatusSucceeded,
		Metadata:          map[string]string{"order_id": fmt.Sprintf("%d", order.ID)},
	}, secret)
	if err := service.HandleWebhook(context.Background(), payload, sig); err != nil {
		t.Fatalf("HandleWebhook failed: %v", err)
	}

	apiHandler := NewAPIHandler(service, ginapi.NewHandler(service.StripeService()))
	r := gin.New()
	apiHandler.RegisterRoutes(r, "billing-jwt-secret")

	// 1. List orders
	wOrders := httptest.NewRecorder()
	reqOrders, _ := http.NewRequest(http.MethodGet, "/api/v1/billing/orders", nil)
	reqOrders.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wOrders, reqOrders)
	if wOrders.Code != http.StatusOK {
		t.Fatalf("list orders failed: %d: %s", wOrders.Code, wOrders.Body.String())
	}
	var ordersResp struct {
		Orders []domain.Order `json:"orders"`
	}
	if err := json.Unmarshal(wOrders.Body.Bytes(), &ordersResp); err != nil {
		t.Fatalf("unmarshal orders failed: %v", err)
	}
	if len(ordersResp.Orders) == 0 || ordersResp.Orders[0].ID != order.ID {
		t.Fatalf("expected order #%d in orders response, got %+v", order.ID, ordersResp.Orders)
	}

	// 2. List payments
	wPayments := httptest.NewRecorder()
	reqPayments, _ := http.NewRequest(http.MethodGet, "/api/v1/billing/payments", nil)
	reqPayments.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wPayments, reqPayments)
	if wPayments.Code != http.StatusOK {
		t.Fatalf("list payments failed: %d: %s", wPayments.Code, wPayments.Body.String())
	}
	var paymentsResp struct {
		Payments []domain.PaymentRecord `json:"payments"`
	}
	if err := json.Unmarshal(wPayments.Body.Bytes(), &paymentsResp); err != nil {
		t.Fatalf("unmarshal payments failed: %v", err)
	}
	if len(paymentsResp.Payments) == 0 || paymentsResp.Payments[0].OrderID == nil || *paymentsResp.Payments[0].OrderID != order.ID {
		t.Fatalf("expected payment linked to order #%d, got %+v", order.ID, paymentsResp.Payments)
	}

	// 3. charge.refunded webhook syncs PaymentRecord status to refunded
	refundPayload, refundSig := mockProvider.GenerateSignedWebhook(stripekit.WebhookEvent{
		Type:        stripekit.EventChargeRefunded,
		PaymentID:   paymentIntentID,
		RefundID:    "re_test_sync",
		AmountCents: plan.PriceCents,
		Status:      stripekit.PaymentStatusRefunded,
	}, secret)
	if err := service.HandleWebhook(context.Background(), refundPayload, refundSig); err != nil {
		t.Fatalf("refund HandleWebhook failed: %v", err)
	}

	var updatedPayment domain.PaymentRecord
	if err := service.db.Where("provider_tx_id = ?", paymentIntentID).First(&updatedPayment).Error; err != nil {
		t.Fatalf("failed loading updated payment record: %v", err)
	}
	if updatedPayment.Status != domain.PaymentStatusRefunded {
		t.Fatalf("expected payment record status %s after charge.refunded webhook, got %s", domain.PaymentStatusRefunded, updatedPayment.Status)
	}
}

type mockCheckoutJobQueue struct {
	db     *gorm.DB
	hostID uint
	calls  int
}

func (m *mockCheckoutJobQueue) EnqueueCreateServerWithExpiry(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string, expiresAt *time.Time) (*domain.Server, *domain.ServerJob, error) {
	m.calls++
	srv := domain.Server{
		UserID:        userID,
		PlanID:        planID,
		HostID:        m.hostID,
		Name:          name,
		Port:          int(25800 + time.Now().UnixNano()%1000),
		Status:        domain.ServerStatusPending,
		ServerType:    serverType,
		ServerVersion: serverVersion,
		ExpiresAt:     expiresAt,
	}
	if err := m.db.WithContext(ctx).Create(&srv).Error; err != nil {
		return nil, nil, err
	}
	return &srv, &domain.ServerJob{ID: uint(m.calls)}, nil
}

func TestCheckoutWebhookCreateAndExtendServer(t *testing.T) {
	svc, mockProvider, user, srv, plan, _ := setupBillingTestEnv(t)
	if svc == nil {
		return
	}
	ctx := context.Background()
	secret := svc.StripeService().WebhookSecret()

	jq := &mockCheckoutJobQueue{db: svc.db, hostID: srv.HostID}
	svc.SetJobQueue(jq)
	svc.SetRetentionDays(7)

	// 1. Create server checkout order
	cs, order, err := svc.CreateServerCheckout(ctx, user.ID, plan.ID, "PaidWorld", domain.ServerTypePaper, "1.21.4")
	if err != nil {
		t.Fatalf("CreateServerCheckout failed: %v", err)
	}
	if cs.SessionID == "" || order.Status != domain.OrderStatusPending {
		t.Fatalf("unexpected checkout result: session=%s status=%s", cs.SessionID, order.Status)
	}

	// 2. Simulate checkout.session.completed webhook
	payload, sig := mockProvider.GenerateSignedWebhook(stripekit.WebhookEvent{
		Type:              stripekit.EventCheckoutSessionCompleted,
		SessionID:         cs.SessionID,
		PaymentID:         fmt.Sprintf("pi_test_%d", time.Now().UnixNano()),
		ClientReferenceID: fmt.Sprintf("%d", order.ID),
		AmountCents:       plan.PriceCents,
		Status:            stripekit.PaymentStatusSucceeded,
		Metadata:          map[string]string{"order_id": fmt.Sprintf("%d", order.ID)},
	}, secret)
	if err := svc.HandleWebhook(ctx, payload, sig); err != nil {
		t.Fatalf("HandleWebhook failed: %v", err)
	}
	// Idempotent duplicate delivery should not enqueue a second server
	if err := svc.HandleWebhook(ctx, payload, sig); err != nil {
		t.Fatalf("duplicate HandleWebhook failed: %v", err)
	}
	if jq.calls != 1 {
		t.Fatalf("expected 1 enqueued server creation, got %d", jq.calls)
	}

	var fulfilledOrder domain.Order
	_ = svc.db.First(&fulfilledOrder, order.ID)
	if fulfilledOrder.Status != domain.OrderStatusFulfilled || fulfilledOrder.ServerID == nil {
		t.Fatalf("expected order fulfilled with server_id, got status=%s server_id=%v", fulfilledOrder.Status, fulfilledOrder.ServerID)
	}

	var createdSrv domain.Server
	_ = svc.db.First(&createdSrv, *fulfilledOrder.ServerID)
	if createdSrv.ExpiresAt == nil || createdSrv.ExpiresAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("expected purchased server ExpiresAt ~30 days in future, got %v", createdSrv.ExpiresAt)
	}

	// 3. Extend server duration via checkout webhook
	prevExp := *createdSrv.ExpiresAt
	extCS, extOrder, err := svc.CreateExtendCheckout(ctx, user.ID, createdSrv.ID)
	if err != nil {
		t.Fatalf("CreateExtendCheckout failed: %v", err)
	}
	extPayload, extSig := mockProvider.GenerateSignedWebhook(stripekit.WebhookEvent{
		Type:              stripekit.EventCheckoutSessionCompleted,
		SessionID:         extCS.SessionID,
		PaymentID:         fmt.Sprintf("pi_ext_%d", time.Now().UnixNano()),
		ClientReferenceID: fmt.Sprintf("%d", extOrder.ID),
		AmountCents:       plan.PriceCents,
		Status:            stripekit.PaymentStatusSucceeded,
		Metadata:          map[string]string{"order_id": fmt.Sprintf("%d", extOrder.ID)},
	}, secret)
	if err := svc.HandleWebhook(ctx, extPayload, extSig); err != nil {
		t.Fatalf("extension HandleWebhook failed: %v", err)
	}
	_ = svc.db.First(&createdSrv, createdSrv.ID)
	if !createdSrv.ExpiresAt.After(prevExp.Add(29 * 24 * time.Hour)) {
		t.Fatalf("expected extended ExpiresAt (+30d from %v), got %v", prevExp, createdSrv.ExpiresAt)
	}

	// 4. Reject extension if server is past 7-day retention
	pastRetention := time.Now().Add(-8 * 24 * time.Hour)
	_ = svc.db.Model(&createdSrv).Update("expires_at", pastRetention).Error
	if _, _, err := svc.CreateExtendCheckout(ctx, user.ID, createdSrv.ID); err == nil {
		t.Fatal("expected error extending server past 7-day retention")
	}

	_ = svc.db.Delete(&createdSrv).Error
}
