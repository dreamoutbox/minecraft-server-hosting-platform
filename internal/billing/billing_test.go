package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

// mockSuspender records server stop calls during payment suspension tests.
type mockSuspender struct {
	stoppedServers map[uint]bool
}

func (m *mockSuspender) StopServer(ctx context.Context, userID uint, serverID uint) (*domain.Server, error) {
	if m.stoppedServers == nil {
		m.stoppedServers = make(map[uint]bool)
	}
	m.stoppedServers[serverID] = true
	return &domain.Server{ID: serverID, Status: domain.ServerStatusSuspended}, nil
}

func setupBillingTestEnv(t *testing.T) (*Service, *MockPaymentProvider, *mockSuspender, *domain.User, *domain.Server, *domain.Plan, string) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgrespassword dbname=minecraft_hosting port=5432 sslmode=disable"
	}
	db, err := domain.InitDB(dsn)
	if err != nil {
		t.Skipf("skipping test: database unreachable: %v", err)
		return nil, nil, nil, nil, nil, nil, ""
	}
	_ = domain.AutoMigrate(db)
	_ = domain.SeedPlans(db)

	mockProvider := NewMockPaymentProvider()
	suspender := &mockSuspender{stoppedServers: make(map[uint]bool)}
	webhookSecret := "whsec_test_secret_123"
	service := NewService(db, mockProvider, suspender, webhookSecret)

	user := domain.User{
		Email:           fmt.Sprintf("billing-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash:    "hashed",
		Role:            domain.UserRoleUser,
		CustomerID:      fmt.Sprintf("cust_%d", time.Now().UnixNano()),
		PaymentMethodID: "pm_card_test_visa",
	}
	_ = db.Create(&user)

	var plan domain.Plan
	_ = db.Where("name = ?", "Small").First(&plan)

	// Create dummy host
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
	tokens, _ := authkit.GenerateTokenPair(user.ID, user.Email, user.Role, jwtSecret)

	return service, mockProvider, suspender, &user, &server, &plan, tokens.AccessToken
}

func TestBillingFullCycle(t *testing.T) {
	service, mockProvider, suspender, user, server, plan, _ := setupBillingTestEnv(t)
	if service == nil {
		return
	}

	ctx := context.Background()

	// 1. Create subscription -> initial charge succeeds
	sub, err := service.CreateSubscription(ctx, user.ID, plan.ID, &server.ID)
	if err != nil {
		t.Fatalf("CreateSubscription failed: %v", err)
	}

	if sub.Status != domain.SubscriptionStatusActive {
		t.Errorf("expected subscription active, got: %s", sub.Status)
	}

	// 2. Advance next_billing_date to the past to simulate due renewal
	pastBillingDate := time.Now().Add(-1 * time.Hour)
	sub.NextBillingDate = pastBillingDate
	service.db.Save(sub)

	// Process cycle -> successful renewal
	if err := service.ProcessBillingCycle(ctx); err != nil {
		t.Fatalf("ProcessBillingCycle failed: %v", err)
	}

	service.db.First(sub, sub.ID)
	if sub.Status != domain.SubscriptionStatusActive {
		t.Errorf("expected subscription active after renewal, got %s", sub.Status)
	}
	if !sub.NextBillingDate.After(pastBillingDate) {
		t.Errorf("expected next_billing_date to advance forward, got %v", sub.NextBillingDate)
	}

	// 3. Configure mock payment provider to fail charges
	mockProvider.FailCharges = true

	// Advance billing date to past again
	sub.NextBillingDate = time.Now().Add(-1 * time.Hour)
	service.db.Save(sub)

	// Failure 1
	_ = service.ProcessBillingCycle(ctx)
	service.db.First(sub, sub.ID)
	if sub.FailureCount != 1 || sub.Status != domain.SubscriptionStatusPastDue {
		t.Errorf("failure 1: expected past_due with count 1, got status=%s count=%d", sub.Status, sub.FailureCount)
	}

	// Failure 2
	sub.NextBillingDate = time.Now().Add(-1 * time.Hour)
	service.db.Save(sub)
	_ = service.ProcessBillingCycle(ctx)
	service.db.First(sub, sub.ID)
	if sub.FailureCount != 2 || sub.Status != domain.SubscriptionStatusPastDue {
		t.Errorf("failure 2: expected past_due with count 2, got status=%s count=%d", sub.Status, sub.FailureCount)
	}

	// Failure 3 -> should trigger suspension
	sub.NextBillingDate = time.Now().Add(-1 * time.Hour)
	service.db.Save(sub)
	_ = service.ProcessBillingCycle(ctx)
	service.db.First(sub, sub.ID)
	if sub.FailureCount != 3 || sub.Status != domain.SubscriptionStatusSuspended {
		t.Errorf("failure 3: expected suspended with count 3, got status=%s count=%d", sub.Status, sub.FailureCount)
	}

	// Verify server suspension triggered on suspender
	if !suspender.stoppedServers[server.ID] {
		t.Errorf("expected server %d to be suspended by billing coordinator", server.ID)
	}

	// Verify server status in DB is suspended
	var reloadedServer domain.Server
	service.db.First(&reloadedServer, server.ID)
	if reloadedServer.Status != domain.ServerStatusSuspended {
		t.Errorf("expected server DB status to be suspended, got: %s", reloadedServer.Status)
	}
}

func TestWebhookSignatureVerification(t *testing.T) {
	service, mockProvider, _, _, _, _, _ := setupBillingTestEnv(t)
	if service == nil {
		return
	}

	secret := "whsec_test_secret_123"
	handler := NewHandler(service)
	r := gin.New()
	handler.RegisterRoutes(r, "jwt-secret")

	evt := WebhookEvent{
		Type:        "payment_intent.succeeded",
		PaymentID:   "pi_test_abc123",
		AmountCents: 1000,
		CustomerID:  "cust_test_1",
		Status:      domain.PaymentStatusSucceeded,
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

func TestBillingHTTPRoutes(t *testing.T) {
	service, _, _, _, _, plan, token := setupBillingTestEnv(t)
	if service == nil {
		return
	}

	handler := NewHandler(service)
	r := gin.New()
	handler.RegisterRoutes(r, "billing-jwt-secret")

	// 1. Save payment method
	pmBody, _ := json.Marshal(SavePaymentMethodRequest{
		PaymentMethodID: "pm_card_brand_new",
	})
	wPM := httptest.NewRecorder()
	reqPM, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/payment-methods", bytes.NewReader(pmBody))
	reqPM.Header.Set("Authorization", "Bearer "+token)
	reqPM.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wPM, reqPM)
	if wPM.Code != http.StatusOK {
		t.Fatalf("save payment method failed: %d: %s", wPM.Code, wPM.Body.String())
	}

	// 2. Create subscription
	subBody, _ := json.Marshal(CreateSubscriptionRequest{
		PlanID: plan.ID,
	})
	wSub := httptest.NewRecorder()
	reqSub, _ := http.NewRequest(http.MethodPost, "/api/v1/billing/subscriptions", bytes.NewReader(subBody))
	reqSub.Header.Set("Authorization", "Bearer "+token)
	reqSub.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wSub, reqSub)
	if wSub.Code != http.StatusCreated {
		t.Fatalf("create subscription failed: %d: %s", wSub.Code, wSub.Body.String())
	}

	// 3. List subscriptions
	wList := httptest.NewRecorder()
	reqList, _ := http.NewRequest(http.MethodGet, "/api/v1/billing/subscriptions", nil)
	reqList.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("list subscriptions failed: %d", wList.Code)
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

func signMockStripePayload(payload []byte, secret string) string {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.%s", ts, string(payload))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func TestCheckoutWebhookCreateAndExtendServer(t *testing.T) {
	svc, _, _, user, srv, plan, _ := setupBillingTestEnv(t)
	if svc == nil {
		return
	}
	ctx := context.Background()

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
	payload, _ := json.Marshal(map[string]interface{}{
		"type": "checkout.session.completed",
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":             cs.SessionID,
				"payment_intent": fmt.Sprintf("pi_test_%d", time.Now().UnixNano()),
				"amount_total":   plan.PriceCents,
				"metadata": map[string]string{
					"order_id": fmt.Sprintf("%d", order.ID),
				},
			},
		},
	})
	sig := signMockStripePayload(payload, svc.webhookSecret)
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
	extPayload, _ := json.Marshal(map[string]interface{}{
		"type": "checkout.session.completed",
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":             extCS.SessionID,
				"payment_intent": fmt.Sprintf("pi_ext_%d", time.Now().UnixNano()),
				"amount_total":   plan.PriceCents,
				"metadata": map[string]string{
					"order_id": fmt.Sprintf("%d", extOrder.ID),
				},
			},
		},
	})
	if err := svc.HandleWebhook(ctx, extPayload, signMockStripePayload(extPayload, svc.webhookSecret)); err != nil {
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
