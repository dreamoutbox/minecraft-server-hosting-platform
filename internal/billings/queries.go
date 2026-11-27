package billings

import (
	"context"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

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
