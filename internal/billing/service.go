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

// Service manages subscriptions, cron billing renewal cycles, and payment webhooks.
type Service struct {
	db            *gorm.DB
	provider      PaymentProvider
	suspender     ServerSuspender
	webhookSecret string
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
//   svc := billing.NewService(db, paymentProvider, hostingSvc, "whsec_123")
func NewService(db *gorm.DB, provider PaymentProvider, suspender ServerSuspender, webhookSecret string) *Service {
	return &Service{
		db:            db,
		provider:      provider,
		suspender:     suspender,
		webhookSecret: webhookSecret,
		cronRunner:    cron.New(),
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
//   err := svc.SavePaymentMethod(ctx, user.ID, "pm_card_visa")
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
//   sub, err := svc.CreateSubscription(ctx, user.ID, plan.ID, &server.ID)
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
//   err := svc.ProcessBillingCycle(ctx)
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

// HandleWebhook validates signature and processes webhook status changes.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - payload: Raw HTTP request body.
//   - signatureHeader: Signature value from Stripe-Signature header.
//
// Example usage:
//   err := svc.HandleWebhook(ctx, rawBody, signatureHeader)
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) error {
	event, err := s.provider.VerifyWebhookSignature(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		return fmt.Errorf("webhook signature verification failed: %w", err)
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

// ListUserSubscriptions returns all subscriptions owned by the user.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: User primary key.
//
// Example usage:
//   subs, err := svc.ListUserSubscriptions(ctx, user.ID)
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
//   err := svc.ScheduleBillingCron("@daily")
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
//   svc.StopCron()
func (s *Service) StopCron() {
	if s.cronRunner != nil {
		s.cronRunner.Stop()
	}
}
