package billing

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

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"

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
