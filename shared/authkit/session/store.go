package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AutoMigrate creates or updates the sessions table in db.
//
// Arguments:
//   - db: Active GORM database handle.
//
// Example usage:
//
//	if err := session.AutoMigrate(db); err != nil {
//	    return err
//	}
func AutoMigrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Session{}); err != nil {
		return fmt.Errorf("failed migrating sessions table: %w", err)
	}
	return nil
}

// Store manages persistence and validation of server-side sessions in a GORM database.
type Store struct {
	db         *gorm.DB
	defaultTTL time.Duration
}

// NewStore initializes a session Store backed by db with a default session lifetime.
// If defaultTTL is <= 0, DefaultTTL (24h) is used.
//
// Arguments:
//   - db: Active GORM database handle containing users and sessions tables.
//   - defaultTTL: Default lifetime for newly created sessions.
//
// Example usage:
//
//	store := session.NewStore(db, 24*time.Hour)
func NewStore(db *gorm.DB, defaultTTL time.Duration) *Store {
	if defaultTTL <= 0 {
		defaultTTL = DefaultTTL
	}
	return &Store{
		db:         db,
		defaultTTL: defaultTTL,
	}
}

// DB exposes the underlying GORM database handle for middleware user lookups.
func (s *Store) DB() *gorm.DB {
	return s.db
}

// Create generates a new cryptographically random session token, stores its hash in the database,
// and returns the raw token (for the HTTP cookie) along with the persisted Session record.
// When ttl is 0, the store's defaultTTL is applied.
//
// Arguments:
//   - ctx: Request context for cancellation and deadlines.
//   - userID: Database primary key of the authenticated user.
//   - ttl: Session lifetime duration; 0 uses Store's defaultTTL.
//
// Example usage:
//
//	rawToken, sess, err := store.Create(ctx, user.ID, 0)
func (s *Store) Create(ctx context.Context, userID uint, ttl time.Duration) (string, *Session, error) {
	if userID == 0 {
		return "", nil, ErrInvalidUserID
	}
	if ttl == 0 {
		ttl = s.defaultTTL
	}
	if ttl < 0 {
		return "", nil, ErrInvalidTTL
	}

	rawToken, err := generateRawToken()
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	sess := &Session{
		TokenHash:  HashToken(rawToken),
		UserID:     userID,
		ExpiresAt:  now.Add(ttl),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		return "", nil, fmt.Errorf("failed creating session: %w", err)
	}

	return rawToken, sess, nil
}

// GetValid looks up a session by raw token and verifies it has not expired.
// Expired sessions are lazily deleted and reported as ErrSessionExpired.
//
// Arguments:
//   - ctx: Request context for cancellation and deadlines.
//   - rawToken: Raw opaque session token extracted from the client cookie.
//
// Example usage:
//
//	sess, err := store.GetValid(ctx, rawToken)
func (s *Store) GetValid(ctx context.Context, rawToken string) (*Session, error) {
	trimmed := strings.TrimSpace(rawToken)
	if trimmed == "" {
		return nil, ErrInvalidSession
	}

	var sess Session
	err := s.db.WithContext(ctx).Where("token_hash = ?", HashToken(trimmed)).First(&sess).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("failed querying session: %w", err)
	}

	now := time.Now().UTC()
	if !sess.ExpiresAt.After(now) {
		_ = s.db.WithContext(ctx).Where("token_hash = ?", sess.TokenHash).Delete(&Session{}).Error
		return nil, ErrSessionExpired
	}

	return &sess, nil
}

// Delete revokes a single session by its raw cookie token.
//
// Arguments:
//   - ctx: Request context for cancellation and deadlines.
//   - rawToken: Raw opaque session token to revoke.
//
// Example usage:
//
//	_ = store.Delete(ctx, rawToken)
func (s *Store) Delete(ctx context.Context, rawToken string) error {
	trimmed := strings.TrimSpace(rawToken)
	if trimmed == "" {
		return nil
	}
	if err := s.db.WithContext(ctx).Where("token_hash = ?", HashToken(trimmed)).Delete(&Session{}).Error; err != nil {
		return fmt.Errorf("failed deleting session: %w", err)
	}
	return nil
}

// DeleteByUserID revokes all active sessions belonging to the specified user.
//
// Arguments:
//   - ctx: Request context for cancellation and deadlines.
//   - userID: Database primary key of the user whose sessions should be revoked.
//
// Example usage:
//
//	err := store.DeleteByUserID(ctx, user.ID)
func (s *Store) DeleteByUserID(ctx context.Context, userID uint) error {
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&Session{}).Error; err != nil {
		return fmt.Errorf("failed deleting user sessions: %w", err)
	}
	return nil
}

// PruneExpired removes all expired sessions from the database and returns the number of deleted rows.
//
// Arguments:
//   - ctx: Request context for cancellation and deadlines.
//
// Example usage:
//
//	removed, err := store.PruneExpired(ctx)
func (s *Store) PruneExpired(ctx context.Context) (int64, error) {
	res := s.db.WithContext(ctx).Where("expires_at <= ?", time.Now().UTC()).Delete(&Session{})
	if res.Error != nil {
		return 0, fmt.Errorf("failed pruning expired sessions: %w", res.Error)
	}
	return res.RowsAffected, nil
}
