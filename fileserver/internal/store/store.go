package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/dreamoutbox/authkit"
)

var (
	// ErrUserNotFound indicates the requested user ID does not exist in the local store.
	ErrUserNotFound = errors.New("user not found")
	// ErrPermissionDenied indicates the user lacks a grant covering the requested directory path.
	ErrPermissionDenied = errors.New("permission denied for directory")
	// ErrTokenAlreadyUsed indicates a single-use login token was already redeemed.
	ErrTokenAlreadyUsed = errors.New("login token has already been used")
)

const randomPasswordEntropyBytes = 32

// OpenSQLite opens (or creates) the fileserver SQLite database and applies schema migrations.
//
// Arguments:
//   - dbPath: Filesystem path to the SQLite file, or an in-memory DSN such as ":memory:".
//
// Example usage:
//
//	db, err := store.OpenSQLite("/var/lib/mc-platform/fileserver/fileserver.db")
func OpenSQLite(dbPath string) (*gorm.DB, error) {
	isMemory := dbPath == ":memory:" || strings.HasPrefix(dbPath, "file:")
	if !isMemory {
		parent := filepath.Dir(dbPath)
		if err := os.MkdirAll(parent, 0750); err != nil {
			return nil, fmt.Errorf("failed creating sqlite parent dir %s: %w", parent, err)
		}
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed opening sqlite database: %w", err)
	}

	if !isMemory {
		// WAL mode improves concurrent HTTP reads/writes; busy_timeout avoids SQLITE_BUSY under load.
		_ = db.Exec("PRAGMA journal_mode=WAL;").Error
		_ = db.Exec("PRAGMA busy_timeout=5000;").Error
	}

	if err := db.AutoMigrate(&authkit.User{}, &DirPermission{}, &UsedLoginToken{}); err != nil {
		return nil, fmt.Errorf("failed running sqlite automigrate: %w", err)
	}

	return db, nil
}

// Store encapsulates SQLite persistence for fileserver users, directory permissions, and token replay state.
type Store struct {
	db        *gorm.DB
	mountsDir string
}

// New constructs a Store bound to db and the node's base mounts directory.
//
// Arguments:
//   - db: Initialized GORM database handle.
//   - mountsDir: Base directory containing per-server mount folders (e.g. /var/lib/mc-platform/mounts).
//
// Example usage:
//
//	st := store.New(db, "/var/lib/mc-platform/mounts")
func New(db *gorm.DB, mountsDir string) *Store {
	return &Store{
		db:        db,
		mountsDir: filepath.Clean(mountsDir),
	}
}

// ServerDirPath computes the canonical mount directory path for a server ID.
//
// Arguments:
//   - serverID: Target game server ID.
//
// Example usage:
//
//	dir := st.ServerDirPath(42)
func (s *Store) ServerDirPath(serverID uint) string {
	return filepath.Join(s.mountsDir, strconv.FormatUint(uint64(serverID), 10))
}

// EnsureUser idempotently provisions a local user record matching the platform user ID.
// When creating a new user, a random password is generated and hashed with bcrypt.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Platform user primary key.
//   - email: Account email address.
//
// Example usage:
//
//	user, created, err := st.EnsureUser(ctx, 1, "player@example.com")
func (s *Store) EnsureUser(ctx context.Context, userID uint, email string) (*authkit.User, bool, error) {
	if userID == 0 {
		return nil, false, errors.New("user_id must be positive")
	}
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return nil, false, errors.New("email is required")
	}

	var existing authkit.User
	err := s.db.WithContext(ctx).First(&existing, userID).Error
	if err == nil {
		if existing.Email != normalized {
			existing.Email = normalized
			if saveErr := s.db.WithContext(ctx).Save(&existing).Error; saveErr != nil {
				return nil, false, fmt.Errorf("failed updating user email: %w", saveErr)
			}
		}
		return &existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("failed querying user: %w", err)
	}

	rawPwd, err := authkit.GenerateRandomPassword(randomPasswordEntropyBytes)
	if err != nil {
		return nil, false, fmt.Errorf("failed generating random password: %w", err)
	}
	hashed, err := authkit.HashPassword(rawPwd)
	if err != nil {
		return nil, false, fmt.Errorf("failed hashing random password: %w", err)
	}

	user := authkit.User{
		ID:           userID,
		Email:        normalized,
		PasswordHash: hashed,
		Role:         authkit.UserRoleUser,
	}

	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		// Handle concurrent creation race for the same user ID.
		var raced authkit.User
		if lookupErr := s.db.WithContext(ctx).First(&raced, userID).Error; lookupErr == nil {
			return &raced, false, nil
		}
		return nil, false, fmt.Errorf("failed creating user: %w", err)
	}

	return &user, true, nil
}

// GetUser retrieves a user record by primary key.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//
// Example usage:
//
//	user, err := st.GetUser(ctx, 1)
func (s *Store) GetUser(ctx context.Context, userID uint) (*authkit.User, error) {
	var user authkit.User
	err := s.db.WithContext(ctx).First(&user, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed querying user: %w", err)
	}
	return &user, nil
}

// Allow idempotently grants a user access to a server's mount directory.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//   - serverID: Target server ID.
//
// Example usage:
//
//	perm, err := st.Allow(ctx, 1, 42)
func (s *Store) Allow(ctx context.Context, userID uint, serverID uint) (*DirPermission, error) {
	if userID == 0 || serverID == 0 {
		return nil, errors.New("user_id and server_id must be positive")
	}

	perm := DirPermission{
		UserID:   userID,
		ServerID: serverID,
		DirPath:  s.ServerDirPath(serverID),
	}

	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "server_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"dir_path"}),
	}).Create(&perm).Error
	if err != nil {
		return nil, fmt.Errorf("failed saving directory permission: %w", err)
	}

	return &perm, nil
}

// Disallow removes one user's permission for a server directory.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//   - serverID: Target server ID.
//
// Example usage:
//
//	err := st.Disallow(ctx, 1, 42)
func (s *Store) Disallow(ctx context.Context, userID uint, serverID uint) error {
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND server_id = ?", userID, serverID).
		Delete(&DirPermission{}).Error; err != nil {
		return fmt.Errorf("failed revoking directory permission: %w", err)
	}
	return nil
}

// RevokeServer removes all directory permissions for a server (e.g. when the server is deleted).
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target server ID.
//
// Example usage:
//
//	err := st.RevokeServer(ctx, 42)
func (s *Store) RevokeServer(ctx context.Context, serverID uint) error {
	if err := s.db.WithContext(ctx).
		Where("server_id = ?", serverID).
		Delete(&DirPermission{}).Error; err != nil {
		return fmt.Errorf("failed revoking server permissions: %w", err)
	}
	return nil
}

// ListUserPermissions returns all directory grants held by a user.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//
// Example usage:
//
//	perms, err := st.ListUserPermissions(ctx, 1)
func (s *Store) ListUserPermissions(ctx context.Context, userID uint) ([]DirPermission, error) {
	var perms []DirPermission
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("server_id ASC").
		Find(&perms).Error; err != nil {
		return nil, fmt.Errorf("failed listing directory permissions: %w", err)
	}
	return perms, nil
}

// FindPermissionForPath checks whether userID holds a grant covering absPath and returns
// the matching DirPermission along with the relative path inside that grant root.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Authenticated user ID.
//   - absPath: Requested filesystem path.
//
// Example usage:
//
//	perm, rel, err := st.FindPermissionForPath(ctx, 1, "/var/lib/mc-platform/mounts/42/world")
func (s *Store) FindPermissionForPath(ctx context.Context, userID uint, absPath string) (*DirPermission, string, error) {
	cleaned := filepath.Clean(absPath)
	if !filepath.IsAbs(cleaned) {
		return nil, "", ErrPermissionDenied
	}

	perms, err := s.ListUserPermissions(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	for i := range perms {
		root := filepath.Clean(perms[i].DirPath)
		rel, err := filepath.Rel(root, cleaned)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
			return &perms[i], rel, nil
		}
	}

	return nil, "", ErrPermissionDenied
}

// ConsumeLoginToken records a login token's JTI to enforce single-use semantics.
// Expired token records are pruned during the same transaction.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - jti: Unique token identifier from JWT claims.
//   - expiresAt: Token expiration timestamp.
//
// Example usage:
//
//	err := st.ConsumeLoginToken(ctx, claims.ID, claims.ExpiresAt.Time)
func (s *Store) ConsumeLoginToken(ctx context.Context, jti string, expiresAt time.Time) error {
	if jti == "" {
		return errors.New("token jti is required")
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Where("expires_at < ?", now).Delete(&UsedLoginToken{}).Error; err != nil {
			return fmt.Errorf("failed pruning expired login tokens: %w", err)
		}

		var count int64
		if err := tx.Model(&UsedLoginToken{}).Where("jti = ?", jti).Count(&count).Error; err != nil {
			return fmt.Errorf("failed checking used token: %w", err)
		}
		if count > 0 {
			return ErrTokenAlreadyUsed
		}

		record := UsedLoginToken{
			JTI:       jti,
			ExpiresAt: expiresAt,
		}
		if err := tx.Create(&record).Error; err != nil {
			return ErrTokenAlreadyUsed
		}
		return nil
	})
}
