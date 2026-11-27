package authkit

import (
	"github.com/dreamoutbox/authkit/shared"
	"gorm.io/gorm"
)

// Core type aliases re-exported from authkit/shared for convenience.
type (
	User     = shared.User
	UserRole = shared.UserRole
	Session  = shared.Session
)

const (
	UserRoleUser  = shared.UserRoleUser
	UserRoleStaff = shared.UserRoleStaff
	UserRoleAdmin = shared.UserRoleAdmin

	MinPasswordLength = shared.MinPasswordLength

	ContextUserIDKey = shared.ContextUserIDKey
	ContextEmailKey  = shared.ContextEmailKey
	ContextRoleKey   = shared.ContextRoleKey
)

var (
	ErrInvalidCredentials = shared.ErrInvalidCredentials
	ErrInvalidEmail       = shared.ErrInvalidEmail
	ErrPasswordTooShort   = shared.ErrPasswordTooShort
	ErrEmailAlreadyExists = shared.ErrEmailAlreadyExists
)

// HashPassword hashes a plaintext password using bcrypt.
func HashPassword(password string) (string, error) {
	return shared.HashPassword(password)
}

// CheckPassword verifies a plaintext password against a stored bcrypt hash.
func CheckPassword(hash, password string) bool {
	return shared.CheckPassword(hash, password)
}

// GenerateRandomPassword creates a cryptographically random URL-safe password string.
func GenerateRandomPassword(numBytes int) (string, error) {
	return shared.GenerateRandomPassword(numBytes)
}

// Authenticate verifies an email and plaintext password against the users table.
func Authenticate(db *gorm.DB, email, password string) (*User, error) {
	return shared.Authenticate(db, email, password)
}

// RegisterUser validates credentials, hashes the password, and creates a new User record.
func RegisterUser(db *gorm.DB, email, password string) (*User, error) {
	return shared.RegisterUser(db, email, password)
}
