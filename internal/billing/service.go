package billing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

// ServerSuspender abstracts server suspension to decouple billing from hosting infrastructure.
type ServerSuspender interface {
	StopServer(ctx context.Context, userID uint, serverID uint) (*domain.Server, error)
}

// JobEnqueuer enqueues async server provisioning jobs after checkout payment completes.
type JobEnqueuer interface {
	EnqueueCreateServerWithExpiry(ctx context.Context, userID uint, name string, planID uint, serverType domain.ServerType, serverVersion string, expiresAt *time.Time) (*domain.Server, *domain.ServerJob, error)
}

// Service manages subscriptions, cron billing renewal cycles, checkout orders, and payment webhooks.
type Service struct {
	db            *gorm.DB
	provider      PaymentProvider
	suspender     ServerSuspender
	jobQueue      JobEnqueuer
	webhookSecret string
	appBaseURL    string
	retentionDays int
	cronRunner    *cron.Cron
}

// NewService instantiates a subscription billing service.
//
// Arguments:
//   - db: Active database handle.
//   - provider: Configured payment provider (Stripe or Mock).
//   - suspender: Server lifecycle coordinator for payment failure suspensions.
//   - webhookSecret: Secret key used to verify incoming webhook signatures.
//
// Example usage:
//
//	svc := billing.NewService(db, paymentProvider, hostingSvc, "whsec_123")
func NewService(db *gorm.DB, provider PaymentProvider, suspender ServerSuspender, webhookSecret string) *Service {
	return &Service{
		db:            db,
		provider:      provider,
		suspender:     suspender,
		webhookSecret: webhookSecret,
		appBaseURL:    "http://localhost:8080",
		retentionDays: 7,
		cronRunner:    cron.New(),
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

// SavePaymentMethod attaches a customer payment method and records it in the user profile.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: User primary key.
//   - paymentMethodID: Token or ID of the payment method (e.g. pm_card_...).
//
// Example usage:
//
//	err := svc.SavePaymentMethod(ctx, user.ID, "pm_card_visa")
func (s *Service) SavePaymentMethod(ctx context.Context, userID uint, paymentMethodID string) error {
	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return fmt.Errorf("user not found: %w", err)
	}

	if user.CustomerID == "" {
		user.CustomerID = fmt.Sprintf("cust_%d_%s", user.ID, user.Email)
	}

	if err := s.provider.SavePaymentMethod(ctx, user.CustomerID, paymentMethodID); err != nil {
		return fmt.Errorf("failed attaching payment method: %w", err)
	}

	user.PaymentMethodID = paymentMethodID
	if err := s.db.WithContext(ctx).Save(&user).Error; err != nil {
		return fmt.Errorf("failed saving payment method to user: %w", err)
	}

	return nil
}

// CreateSubscription initiates a recurring subscription, executing the initial cycle charge.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Account ID of subscriber.
//   - planID: Plan tier ID.
//   - serverID: Associated game server ID (optional).
//
// Example usage:
//
//	sub, err := svc.CreateSubscription(ctx, user.ID, plan.ID, &server.ID)
func (s *Service) CreateSubscription(ctx context.Context, userID uint, planID uint, serverID *uint) (*domain.Subscription, error) {
	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	if user.PaymentMethodID == "" {
		return nil, errors.New("no saved payment method found; please attach a payment method first")
	}

	var plan domain.Plan
	if err := s.db.WithContext(ctx).First(&plan, planID).Error; err != nil {
		return nil, fmt.Errorf("plan not found: %w", err)
	}

	// Immediate charge for the upcoming month
	description := fmt.Sprintf("Initial cycle for %s plan", plan.Name)
	res, err := s.provider.ChargeSavedPaymentMethod(ctx, user.CustomerID, user.PaymentMethodID, plan.PriceCents, description)

	paymentStatus := domain.PaymentStatusSucceeded
	txID := ""
	if res != nil {
		paymentStatus = res.Status
		txID = res.TransactionID
	}

	paymentRecord := domain.PaymentRecord{
		UserID:       user.ID,
		AmountCents:  plan.PriceCents,
		Status:       paymentStatus,
		ProviderTxID: txID,
	}
	_ = s.db.WithContext(ctx).Create(&paymentRecord)

	if err != nil || paymentStatus != domain.PaymentStatusSucceeded {
		return nil, fmt.Errorf("initial payment charge failed: %v", err)
	}

	nextBilling := time.Now().AddDate(0, 1, 0)
	sub := domain.Subscription{
		UserID:          user.ID,
		PlanID:          plan.ID,
		ServerID:        serverID,
		Status:          domain.SubscriptionStatusActive,
		FailureCount:    0,
		NextBillingDate: nextBilling,
	}

	if err := s.db.WithContext(ctx).Create(&sub).Error; err != nil {
		return nil, fmt.Errorf("failed creating subscription record: %w", err)
	}

	paymentRecord.SubscriptionID = &sub.ID
	_ = s.db.WithContext(ctx).Save(&paymentRecord)

	return &sub, nil
}

// ProcessBillingCycle sweeps active and past due subscriptions whose billing date has arrived.
// Upon successful charge, next_billing_date is advanced by 1 month.
// Upon repeated charge failure (threshold 3), the server is suspended (container stopped, volume kept).
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	err := svc.ProcessBillingCycle(ctx)
func (s *Service) ProcessBillingCycle(ctx context.Context) error {
	now := time.Now()
	var subs []domain.Subscription
	err := s.db.WithContext(ctx).
		Preload("Plan").
		Where("status IN (?) AND next_billing_date <= ?", []domain.SubscriptionStatus{
			domain.SubscriptionStatusActive,
			domain.SubscriptionStatusPastDue,
		}, now).
		Find(&subs).Error
	if err != nil {
		return fmt.Errorf("failed querying subscriptions due for billing: %w", err)
	}

	for i := range subs {
		sub := &subs[i]
		var user domain.User
		if err := s.db.WithContext(ctx).First(&user, sub.UserID).Error; err != nil {
			log.Printf("[ERROR] user %d not found for subscription %d: %v", sub.UserID, sub.ID, err)
			continue
		}

		desc := fmt.Sprintf("Monthly renewal for subscription #%d (%s plan)", sub.ID, sub.Plan.Name)
		res, chargeErr := s.provider.ChargeSavedPaymentMethod(ctx, user.CustomerID, user.PaymentMethodID, sub.Plan.PriceCents, desc)

		txID := ""
		paymentStatus := domain.PaymentStatusFailed
		if res != nil {
			txID = res.TransactionID
			paymentStatus = res.Status
		}

		payRec := domain.PaymentRecord{
			UserID:         user.ID,
			SubscriptionID: &sub.ID,
			AmountCents:    sub.Plan.PriceCents,
			Status:         paymentStatus,
			ProviderTxID:   txID,
		}
		_ = s.db.WithContext(ctx).Create(&payRec)

		if chargeErr == nil && paymentStatus == domain.PaymentStatusSucceeded {
			// Success: advance next billing date by 1 month from successful charge
			sub.Status = domain.SubscriptionStatusActive
			sub.FailureCount = 0
			sub.NextBillingDate = time.Now().AddDate(0, 1, 0)
			_ = s.db.WithContext(ctx).Save(sub)
		} else {
			// Failure: increment failure count
			sub.FailureCount++
			if sub.FailureCount >= 3 {
				sub.Status = domain.SubscriptionStatusSuspended
				_ = s.db.WithContext(ctx).Save(sub)

				// Repeated failure triggers server suspension (keeps volume intact)
				if sub.ServerID != nil && s.suspender != nil {
					_, _ = s.suspender.StopServer(ctx, sub.UserID, *sub.ServerID)
					_ = s.db.WithContext(ctx).Model(&domain.Server{}).
						Where("id = ?", *sub.ServerID).
						Update("status", domain.ServerStatusSuspended).Error
				}
			} else {
				sub.Status = domain.SubscriptionStatusPastDue
				_ = s.db.WithContext(ctx).Save(sub)
			}
		}
	}

	return nil
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
		CancelURL:   fmt.Sprintf("%s/dashboard/servers/%d?error=Checkout+cancelled", s.appBaseURL, server.ID),
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
		_ = s.db.WithContext(ctx).Create(&domain.PaymentRecord{
			UserID:       order.UserID,
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

// ListUserSubscriptions returns all subscriptions owned by the user.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: User primary key.
//
// Example usage:
//
//	subs, err := svc.ListUserSubscriptions(ctx, user.ID)
func (s *Service) ListUserSubscriptions(ctx context.Context, userID uint) ([]domain.Subscription, error) {
	var subs []domain.Subscription
	err := s.db.WithContext(ctx).
		Preload("Plan").
		Where("user_id = ?", userID).
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return subs, nil
}

// ScheduleBillingCron registers the background billing sweep on the cron runner.
//
// Arguments:
//   - cronSpec: Standard cron schedule string (e.g. "@daily" or "@hourly").
//
// Example usage:
//
//	err := svc.ScheduleBillingCron("@daily")
func (s *Service) ScheduleBillingCron(cronSpec string) error {
	_, err := s.cronRunner.AddFunc(cronSpec, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := s.ProcessBillingCycle(ctx); err != nil {
			log.Printf("[ERROR] billing sweep failed: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %s: %w", cronSpec, err)
	}

	s.cronRunner.Start()
	return nil
}

// StopCron halts the billing background scheduler.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	svc.StopCron()
func (s *Service) StopCron() {
	if s.cronRunner != nil {
		s.cronRunner.Stop()
	}
}
