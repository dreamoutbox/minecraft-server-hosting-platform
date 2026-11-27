package shared

import (
	"errors"
	"net/mail"
	"strings"

	"gorm.io/gorm"
)

// MinPasswordLength is the minimum accepted plaintext password length for account registration.
const MinPasswordLength = 8

var (
	// ErrInvalidCredentials indicates the supplied email or password failed verification.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidEmail indicates the supplied email address is empty or malformed.
	ErrInvalidEmail = errors.New("invalid email address")
	// ErrPasswordTooShort indicates the supplied password does not meet MinPasswordLength.
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	// ErrEmailAlreadyExists indicates an account with the normalized email is already registered.
	ErrEmailAlreadyExists = errors.New("user with this email already exists")
)

// Authenticate verifies an email and plaintext password against the users table.
//
// Arguments:
//   - db: Active GORM database handle containing the users table.
//   - email: Account email address (trimmed and lowercased before lookup).
//   - password: Plaintext password candidate to verify.
//
// Example usage:
//
//	user, err := shared.Authenticate(db, "player@example.com", "secret-pass")
func Authenticate(db *gorm.DB, email, password string) (*User, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	var user User
	if err := db.Where("email = ?", normalized).First(&user).Error; err != nil {
		return nil, ErrInvalidCredentials
	}

	if !CheckPassword(user.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}

	return &user, nil
}

// RegisterUser validates email and password, checks uniqueness, hashes the password,
// and creates a new User record with the default UserRoleUser role.
//
// Arguments:
//   - db: Active GORM database handle containing the users table.
//   - email: Account email address (trimmed and lowercased before validation and storage).
//   - password: Plaintext password (must be at least MinPasswordLength characters).
//
// Example usage:
//
//	user, err := shared.RegisterUser(db, "player@example.com", "secret-pass")
func RegisterUser(db *gorm.DB, email, password string) (*User, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return nil, ErrInvalidEmail
	}
	if _, err := mail.ParseAddress(normalized); err != nil {
		return nil, ErrInvalidEmail
	}
	if len(password) < MinPasswordLength {
		return nil, ErrPasswordTooShort
	}

	var existing User
	if err := db.Where("email = ?", normalized).First(&existing).Error; err == nil {
		return nil, ErrEmailAlreadyExists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	hashedPassword, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	newUser := User{
		Email:        normalized,
		PasswordHash: hashedPassword,
		Role:         UserRoleUser,
	}
	if err := db.Create(&newUser).Error; err != nil {
		return nil, err
	}

	return &newUser, nil
}
