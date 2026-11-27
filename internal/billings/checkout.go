package billings

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	stripekit "github.com/dreamoutbox/go-stripe-kit"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

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
func (s *Service) CreateServerCheckout(ctx context.Context, userID uint, planID uint, serverName string, serverType domain.ServerType, serverVersion string) (*stripekit.CheckoutSessionResult, *domain.Order, error) {
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
		serverVersion = domain.DefaultServerVersion
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

	orderIDStr := strconv.FormatUint(uint64(order.ID), 10)
	cs, err := s.stripeSvc.CreateCheckoutSession(ctx, stripekit.CheckoutSessionRequest{
		ClientReferenceID: orderIDStr,
		Currency:          stripekit.DefaultCurrency,
		CustomerEmail:     user.Email,
		ProductName:       fmt.Sprintf("Minecraft Server (%s)", plan.Name),
		Description:       fmt.Sprintf("%s (%s %s) - 30 days", serverName, serverType, serverVersion),
		AmountCents:       plan.PriceCents,
		SuccessURL:        fmt.Sprintf("%s/dashboard?success=Payment+received.+Your+server+is+queued+for+creation!", s.appBaseURL),
		CancelURL:         fmt.Sprintf("%s/dashboard/servers/buy?error=Checkout+cancelled", s.appBaseURL),
		Metadata: map[string]string{
			"order_id": orderIDStr,
		},
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
func (s *Service) CreateExtendCheckout(ctx context.Context, userID uint, serverID uint) (*stripekit.CheckoutSessionResult, *domain.Order, error) {
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

	orderIDStr := strconv.FormatUint(uint64(order.ID), 10)
	cs, err := s.stripeSvc.CreateCheckoutSession(ctx, stripekit.CheckoutSessionRequest{
		ClientReferenceID: orderIDStr,
		Currency:          stripekit.DefaultCurrency,
		CustomerEmail:     user.Email,
		ProductName:       fmt.Sprintf("Extend %s (%s)", server.Name, server.Plan.Name),
		Description:       "Extend server duration by 30 days",
		AmountCents:       server.Plan.PriceCents,
		SuccessURL:        fmt.Sprintf("%s/dashboard/servers/%d?success=Extension+payment+received!", s.appBaseURL, server.ID),
		CancelURL:         fmt.Sprintf("%s/dashboard/servers/%d/settings?error=Checkout+cancelled#extend-server", s.appBaseURL, server.ID),
		Metadata: map[string]string{
			"order_id": orderIDStr,
		},
	})
	if err != nil {
		_ = s.db.WithContext(ctx).Model(&order).Update("status", domain.OrderStatusFailed).Error
		return nil, nil, err
	}

	_ = s.db.WithContext(ctx).Model(&order).Update("stripe_session_id", cs.SessionID).Error
	order.StripeSessionID = cs.SessionID
	return cs, &order, nil
}
