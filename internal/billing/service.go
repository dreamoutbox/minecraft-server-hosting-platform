package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// JobEnqueuer enqueues async server provisioning jobs after checkout payment completes.
type JobEnqueuer interface {
	EnqueueCreateServerWithExpiry(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string, expiresAt *time.Time) (*domain.Server, *domain.ServerJob, error)
}

// Service manages checkout orders, payment history, and payment webhooks.
type Service struct {
	db            *gorm.DB
	provider      PaymentProvider
	jobQueue      JobEnqueuer
	webhookSecret string
	appBaseURL    string
	retentionDays int
}

// NewService instantiates a billing service for checkout orders and webhook fulfillment.
//
// Arguments:
//   - db: Active database handle.
//   - provider: Configured payment provider (Stripe or Mock).
//   - webhookSecret: Secret key used to verify incoming webhook signatures.
//
// Example usage:
//
//	svc := billing.NewService(db, paymentProvider, "whsec_123")
func NewService(db *gorm.DB, provider PaymentProvider, webhookSecret string) *Service {
	return &Service{
		db:            db,
		provider:      provider,
		webhookSecret: webhookSecret,
		appBaseURL:    "http://localhost:8080",
		retentionDays: 7,
	}
}

// SetJobQueue wires the async job queue used to provision servers after Stripe Checkout completion.
func (s *Service) SetJobQueue(jq JobEnqueuer) {
	s.jobQueue = jq
}

// SetAppBaseURL sets the external base URL used for Stripe Checkout success/cancel redirects.
func (s *Service) SetAppBaseURL(baseURL string) {
	if baseURL != "" {
		s.appBaseURL = baseURL
	}
}

// SetRetentionDays configures the post-expiration grace window in days during which extensions are allowed.
func (s *Service) SetRetentionDays(days int) {
	if days > 0 {
		s.retentionDays = days
	}
}

// CreateServerCheckout creates a pending Order and Stripe Checkout session for purchasing a new server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Purchasing user ID.
//   - planID: Hardware plan tier ID.
//   - serverName: Desired Minecraft server name.
//   - serverType: Minecraft server jar type.
//   - serverVersion: Minecraft version tag.
//
// Example usage:
//
//	res, order, err := svc.CreateServerCheckout(ctx, user.ID, plan.ID, "Survival", domain.ServerTypePaper, "1.21.4")
func (s *Service) CreateServerCheckout(ctx context.Context, userID uint, planID uint, serverName string, serverType domain.ServerType, serverVersion string) (*CheckoutSessionResult, *domain.Order, error) {
	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, nil, fmt.Errorf("user not found: %w", err)
	}

	var plan domain.Plan
	if err := s.db.WithContext(ctx).First(&plan, planID).Error; err != nil {
		return nil, nil, fmt.Errorf("invalid plan ID %d: %w", planID, err)
	}

	if serverType == "" {
		serverType = domain.ServerTypeVanilla
	}
	if serverVersion == "" {
		serverVersion = "LATEST"
	}

	order := domain.Order{
		UserID:        userID,
		PlanID:        plan.ID,
		OrderType:     domain.OrderTypeCreateServer,
		ServerName:    serverName,
		ServerType:    serverType,
		ServerVersion: serverVersion,
		DurationDays:  30,
		AmountCents:   plan.PriceCents,
		Status:        domain.OrderStatusPending,
	}
	if err := s.db.WithContext(ctx).Create(&order).Error; err != nil {
		return nil, nil, fmt.Errorf("failed creating checkout order: %w", err)
	}

	cs, err := s.provider.CreateCheckoutSession(ctx, CheckoutSessionRequest{
		OrderID:     order.ID,
		UserEmail:   user.Email,
		ProductName: fmt.Sprintf("Minecraft Server (%s)", plan.Name),
		Description: fmt.Sprintf("%s (%s %s) - 30 days", serverName, serverType, serverVersion),
		AmountCents: plan.PriceCents,
		SuccessURL:  fmt.Sprintf("%s/dashboard?success=Payment+received.+Your+server+is+queued+for+creation!", s.appBaseURL),
		CancelURL:   fmt.Sprintf("%s/dashboard/servers/buy?error=Checkout+cancelled", s.appBaseURL),
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Model(&order).Update("status", domain.OrderStatusFailed).Error
		return nil, nil, err
	}

	_ = s.db.WithContext(ctx).Model(&order).Update("stripe_session_id", cs.SessionID).Error
	order.StripeSessionID = cs.SessionID
	return cs, &order, nil
}

// CreateExtendCheckout creates a pending Order and Stripe Checkout session for extending a server by 30 days.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Owner user ID.
//   - serverID: Target server primary key.
//
// Example usage:
//
//	res, order, err := svc.CreateExtendCheckout(ctx, user.ID, server.ID)
func (s *Service) CreateExtendCheckout(ctx context.Context, userID uint, serverID uint) (*CheckoutSessionResult, *domain.Order, error) {
	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, nil, fmt.Errorf("user not found: %w", err)
	}

	var server domain.Server
	if err := s.db.WithContext(ctx).Preload("Plan").First(&server, serverID).Error; err != nil {
		return nil, nil, fmt.Errorf("server not found: %w", err)
	}
	if server.UserID != userID {
		return nil, nil, errors.New("unauthorized access to server")
	}
	if !server.IsWithinRetention(time.Now(), s.retentionDays) {
		return nil, nil, errors.New("server has passed the retention period and cannot be extended")
	}

	order := domain.Order{
		UserID:       userID,
		PlanID:       server.PlanID,
		ServerID:     &server.ID,
		OrderType:    domain.OrderTypeExtendServer,
		ServerName:   server.Name,
		DurationDays: 30,
		AmountCents:  server.Plan.PriceCents,
		Status:       domain.OrderStatusPending,
	}
	if err := s.db.WithContext(ctx).Create(&order).Error; err != nil {
		return nil, nil, fmt.Errorf("failed creating extension order: %w", err)
	}

	cs, err := s.provider.CreateCheckoutSession(ctx, CheckoutSessionRequest{
		OrderID:     order.ID,
		UserEmail:   user.Email,
		ProductName: fmt.Sprintf("Extend %s (%s)", server.Name, server.Plan.Name),
		Description: "Extend server duration by 30 days",
		AmountCents: server.Plan.PriceCents,
		SuccessURL:  fmt.Sprintf("%s/dashboard/servers/%d?success=Extension+payment+received!", s.appBaseURL, server.ID),
		CancelURL:   fmt.Sprintf("%s/dashboard/servers/%d/settings?error=Checkout+cancelled#extend-server", s.appBaseURL, server.ID),
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Model(&order).Update("status", domain.OrderStatusFailed).Error
		return nil, nil, err
	}

	_ = s.db.WithContext(ctx).Model(&order).Update("stripe_session_id", cs.SessionID).Error
	order.StripeSessionID = cs.SessionID
	return cs, &order, nil
}

// HandleWebhook validates signature and processes webhook status changes.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - payload: Raw HTTP request body.
//   - signatureHeader: Signature value from Stripe-Signature header.
//
// Example usage:
//
//	err := svc.HandleWebhook(ctx, rawBody, signatureHeader)
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) error {
	event, err := s.provider.VerifyWebhookSignature(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		return fmt.Errorf("webhook signature verification failed: %w", err)
	}

	if event.Type == "checkout.session.completed" {
		return s.handleCheckoutCompleted(ctx, event)
	}

	if event.PaymentID != "" {
		var rec domain.PaymentRecord
		if err := s.db.WithContext(ctx).Where("provider_tx_id = ?", event.PaymentID).First(&rec).Error; err == nil {
			rec.Status = event.Status
			_ = s.db.WithContext(ctx).Save(&rec)
		}
	}

	return nil
}

func (s *Service) handleCheckoutCompleted(ctx context.Context, event *WebhookEvent) error {
	var order domain.Order
	var findErr error
	switch {
	case event.OrderID != 0:
		findErr = s.db.WithContext(ctx).First(&order, event.OrderID).Error
	case event.SessionID != "":
		findErr = s.db.WithContext(ctx).Where("stripe_session_id = ?", event.SessionID).First(&order).Error
	default:
		return nil
	}
	if findErr != nil {
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("failed finding checkout order: %w", findErr)
	}

	claim := s.db.WithContext(ctx).Model(&domain.Order{}).
		Where("id = ? AND status = ?", order.ID, domain.OrderStatusPending).
		Update("status", domain.OrderStatusPaid)
	if claim.Error != nil {
		return fmt.Errorf("failed claiming checkout order: %w", claim.Error)
	}
	if claim.RowsAffected == 0 {
		// Already processed idempotently.
		return nil
	}

	txID := event.PaymentID
	if txID == "" {
		txID = event.SessionID
	}
	if txID != "" {
		orderID := order.ID
		_ = s.db.WithContext(ctx).Create(&domain.PaymentRecord{
			UserID:       order.UserID,
			OrderID:      &orderID,
			AmountCents:  order.AmountCents,
			Status:       domain.PaymentStatusSucceeded,
			ProviderTxID: txID,
		}).Error
	}

	now := time.Now()
	duration := time.Duration(order.DurationDays) * 24 * time.Hour

	switch order.OrderType {
	case domain.OrderTypeCreateServer:
		if s.jobQueue == nil {
			return errors.New("job queue not configured for server creation fulfillment")
		}
		expiresAt := now.Add(duration)
		srv, _, err := s.jobQueue.EnqueueCreateServerWithExpiry(
			ctx,
			order.UserID,
			order.ServerName,
			order.PlanID,
			order.ServerType,
			order.ServerVersion,
			&expiresAt,
		)
		if err != nil {
			_ = s.db.WithContext(ctx).Model(&order).Update("status", domain.OrderStatusFailed).Error
			return fmt.Errorf("failed enqueueing purchased server creation: %w", err)
		}
		_ = s.db.WithContext(ctx).Model(&order).Updates(map[string]interface{}{
			"server_id": srv.ID,
			"status":    domain.OrderStatusFulfilled,
		}).Error

	case domain.OrderTypeExtendServer:
		if order.ServerID == nil {
			return errors.New("extend order missing server_id")
		}
		var srv domain.Server
		if err := s.db.WithContext(ctx).First(&srv, *order.ServerID).Error; err != nil {
			return fmt.Errorf("failed finding server for extension: %w", err)
		}
		base := now
		if srv.ExpiresAt != nil && srv.ExpiresAt.After(base) {
			base = *srv.ExpiresAt
		}
		newExp := base.Add(duration)
		if err := s.db.WithContext(ctx).Model(&srv).Update("expires_at", newExp).Error; err != nil {
			return fmt.Errorf("failed extending server expiration: %w", err)
		}
		_ = s.db.WithContext(ctx).Model(&order).Update("status", domain.OrderStatusFulfilled).Error
	}

	return nil
}

// ListUserOrders returns checkout orders placed by the user, newest first, with their Plan preloaded.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: User primary key.
//
// Example usage:
//
//	orders, err := svc.ListUserOrders(ctx, user.ID)
func (s *Service) ListUserOrders(ctx context.Context, userID uint) ([]domain.Order, error) {
	var orders []domain.Order
	err := s.db.WithContext(ctx).
		Preload("Plan").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&orders).Error
	if err != nil {
		return nil, err
	}
	return orders, nil
}

// ListUserPayments returns recorded payment transactions for the user, newest first.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: User primary key.
//
// Example usage:
//
//	payments, err := svc.ListUserPayments(ctx, user.ID)
func (s *Service) ListUserPayments(ctx context.Context, userID uint) ([]domain.PaymentRecord, error) {
	var payments []domain.PaymentRecord
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&payments).Error
	if err != nil {
		return nil, err
	}
	return payments, nil
}
