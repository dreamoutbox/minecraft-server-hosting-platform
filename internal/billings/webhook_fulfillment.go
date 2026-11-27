package billings

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	stripekit "github.com/dreamoutbox/go-stripe-kit"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// HandleWebhookEvent fulfills platform orders and syncs PaymentRecord status for verified Stripe events.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - event: Verified stripekit webhook event.
//
// Example usage:
//
//	stripeSvc.OnWebhook(billingSvc.HandleWebhookEvent)
func (s *Service) HandleWebhookEvent(ctx context.Context, event *stripekit.WebhookEvent) error {
	if event == nil {
		return nil
	}

	if event.Type == stripekit.EventCheckoutSessionCompleted {
		return s.handleCheckoutCompleted(ctx, event)
	}

	if event.PaymentID == "" {
		return nil
	}

	var rec domain.PaymentRecord
	if err := s.db.WithContext(ctx).Where("provider_tx_id = ?", event.PaymentID).First(&rec).Error; err == nil {
		rec.Status = mapStripePaymentStatus(event.Status)
		_ = s.db.WithContext(ctx).Save(&rec)
	}

	return nil
}

func (s *Service) handleCheckoutCompleted(ctx context.Context, event *stripekit.WebhookEvent) error {
	orderID := parseOrderID(event)

	var order domain.Order
	var findErr error
	switch {
	case orderID != 0:
		findErr = s.db.WithContext(ctx).First(&order, orderID).Error
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
		oid := order.ID
		_ = s.db.WithContext(ctx).Create(&domain.PaymentRecord{
			UserID:       order.UserID,
			OrderID:      &oid,
			AmountCents:  order.AmountCents,
			Status:       domain.PaymentStatusSucceeded,
			ProviderTxID: txID,
		}).Error
	}

	now := time.Now()
	duration := time.Duration(order.DurationDays) * 24 * time.Hour

	switch order.OrderType {
	case domain.OrderTypeCreateServer:
		return s.fulfillCreateServerOrder(ctx, &order, now, duration)
	case domain.OrderTypeExtendServer:
		return s.fulfillExtendServerOrder(ctx, &order, now, duration)
	}

	return nil
}

func (s *Service) fulfillCreateServerOrder(ctx context.Context, order *domain.Order, now time.Time, duration time.Duration) error {
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
		_ = s.db.WithContext(ctx).Model(order).Update("status", domain.OrderStatusFailed).Error
		return fmt.Errorf("failed enqueueing purchased server creation: %w", err)
	}

	_ = s.db.WithContext(ctx).Model(order).Updates(map[string]interface{}{
		"server_id": srv.ID,
		"status":    domain.OrderStatusFulfilled,
	}).Error
	return nil
}

func (s *Service) fulfillExtendServerOrder(ctx context.Context, order *domain.Order, now time.Time, duration time.Duration) error {
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

	_ = s.db.WithContext(ctx).Model(order).Update("status", domain.OrderStatusFulfilled).Error
	return nil
}

func parseOrderID(event *stripekit.WebhookEvent) uint {
	raw := event.Metadata["order_id"]
	if raw == "" {
		raw = event.ClientReferenceID
	}
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(parsed)
}
