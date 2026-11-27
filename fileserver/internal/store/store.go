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

	"github.com/dreamoutbox/go-authkit"
	"github.com/dreamoutbox/go-authkit/session"
)

var (
	// ErrUserNotFound indicates the requested user ID does not exist in the local store.
	ErrUserNotFound = errors.New("user not found")
	// ErrPermissionDenied indicates the user lacks a grant covering the requested directory path.
	ErrPermissionDenied = errors.New("permission denied for directory")
	// ErrTokenAlreadyUsed indicates a single-use login token was already redeemed.
	ErrTokenAlreadyUsed = errors.New("login token has already been used")
	// ErrInvalidUIDGID indicates a negative Unix UID or GID was supplied.
	ErrInvalidUIDGID = errors.New("uid and gid must be non-negative")
	// ErrPasswordRequired indicates an empty password was supplied during user registration.
	ErrPasswordRequired = errors.New("password is required")
)

const (
	usageRetentionDays = 30
	// UsageDayFormat is the UTC date layout used to bucket daily transfer usage.
	UsageDayFormat = "2006-01-02"
)

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

	if err := session.AutoMigrate(db); err != nil {
		return nil, fmt.Errorf("failed running sqlite session automigrate: %w", err)
	}
	if err := db.AutoMigrate(&User{}, &DirPermission{}, &UsedLoginToken{}, &TransferUsage{}); err != nil {
		return nil, fmt.Errorf("failed running sqlite automigrate: %w", err)
	}

	return db, nil
}

// Store encapsulates SQLite persistence for fileserver users, directory permissions, and token replay state.
type Store struct {
	db        *gorm.DB
	mountsDir string
}

// DB exposes the underlying GORM database handle for shared stores such as authkit/session.
func (s *Store) DB() *gorm.DB {
	return s.db
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

// cloneIntPtr returns a fresh copy of an *int to avoid aliasing caller memory.
func cloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

// intPtrDiffers reports whether incoming is non-nil and differs from current.
func intPtrDiffers(current, incoming *int) bool {
	if incoming == nil {
		return false
	}
	return current == nil || *current != *incoming
}

// RegisterUser idempotently provisions a local user record matching the platform user ID.
// When creating a new user, the provided password is hashed with bcrypt.
// On existing users, the existing password hash is preserved; email and non-nil uid/gid values
// are updated when changed, while nil uid/gid leave existing values untouched.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Platform user primary key.
//   - email: Account email address.
//   - password: Raw password required for registration (hashed on user creation).
//   - uid: Optional Unix user ID (>= 0) for file ownership.
//   - gid: Optional Unix group ID (>= 0) for file ownership.
//
// Example usage:
//
//	uid, gid := 1000, 1000
//	user, created, err := st.RegisterUser(ctx, 1, "player@example.com", "secret-pass", &uid, &gid)
func (s *Store) RegisterUser(ctx context.Context, userID uint, email string, password string, uid *int, gid *int) (*User, bool, error) {
	if userID == 0 {
		return nil, false, errors.New("user_id must be positive")
	}
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return nil, false, errors.New("email is required")
	}
	if strings.TrimSpace(password) == "" {
		return nil, false, ErrPasswordRequired
	}
	if (uid != nil && *uid < 0) || (gid != nil && *gid < 0) {
		return nil, false, ErrInvalidUIDGID
	}

	var existing User
	err := s.db.WithContext(ctx).First(&existing, userID).Error
	if err == nil {
		changed := false
		if existing.Email != normalized {
			existing.Email = normalized
			changed = true
		}
		if intPtrDiffers(existing.UID, uid) {
			existing.UID = cloneIntPtr(uid)
			changed = true
		}
		if intPtrDiffers(existing.GID, gid) {
			existing.GID = cloneIntPtr(gid)
			changed = true
		}
		if changed {
			if saveErr := s.db.WithContext(ctx).Save(&existing).Error; saveErr != nil {
				return nil, false, fmt.Errorf("failed updating user: %w", saveErr)
			}
		}
		return &existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("failed querying user: %w", err)
	}

	hashed, err := authkit.HashPassword(password)
	if err != nil {
		return nil, false, fmt.Errorf("failed hashing password: %w", err)
	}

	user := User{
		User: authkit.User{
			ID:           userID,
			Email:        normalized,
			PasswordHash: hashed,
			Role:         authkit.UserRoleUser,
		},
		UID: cloneIntPtr(uid),
		GID: cloneIntPtr(gid),
	}

	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		// Handle concurrent creation race for the same user ID.
		var raced User
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
func (s *Store) GetUser(ctx context.Context, userID uint) (*User, error) {
	var user User
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

// GetUsage returns the recorded upload and download bytes for userID on day (YYYY-MM-DD UTC).
// Returns zeroed usage when no transfers have been recorded yet for that day.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//   - day: UTC date string formatted as UsageDayFormat ("2006-01-02").
//
// Example usage:
//
//	usage, err := st.GetUsage(ctx, 1, time.Now().UTC().Format(store.UsageDayFormat))
func (s *Store) GetUsage(ctx context.Context, userID uint, day string) (TransferUsage, error) {
	var usage TransferUsage
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND day = ?", userID, day).
		First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return TransferUsage{UserID: userID, Day: day}, nil
	}
	if err != nil {
		return TransferUsage{}, fmt.Errorf("failed querying transfer usage: %w", err)
	}
	return usage, nil
}

// AddUsage atomically increments a user's daily upload or download byte counter and prunes old rows.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//   - day: UTC date string formatted as UsageDayFormat ("2006-01-02").
//   - dir: TransferDirectionUpload or TransferDirectionDownload.
//   - deltaBytes: Non-negative number of bytes transferred.
//
// Example usage:
//
//	err := st.AddUsage(ctx, 1, "2026-11-22", store.TransferDirectionUpload, 1024)
func (s *Store) AddUsage(ctx context.Context, userID uint, day string, dir TransferDirection, deltaBytes int64) error {
	if userID == 0 {
		return errors.New("user_id must be positive")
	}
	if strings.TrimSpace(day) == "" {
		return errors.New("day is required")
	}
	if deltaBytes < 0 {
		return errors.New("delta bytes cannot be negative")
	}
	if deltaBytes == 0 {
		return nil
	}

	var uploadDelta, downloadDelta int64
	switch dir {
	case TransferDirectionUpload:
		uploadDelta = deltaBytes
	case TransferDirectionDownload:
		downloadDelta = deltaBytes
	default:
		return fmt.Errorf("invalid transfer direction: %q", dir)
	}

	record := TransferUsage{
		UserID:        userID,
		Day:           day,
		UploadBytes:   uploadDelta,
		DownloadBytes: downloadDelta,
	}

	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "day"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"upload_bytes":   gorm.Expr("upload_bytes + ?", uploadDelta),
			"download_bytes": gorm.Expr("download_bytes + ?", downloadDelta),
		}),
	}).Create(&record).Error
	if err != nil {
		return fmt.Errorf("failed recording transfer usage: %w", err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -usageRetentionDays).Format(UsageDayFormat)
	_ = s.db.WithContext(ctx).Where("day < ?", cutoff).Delete(&TransferUsage{}).Error

	return nil
}
