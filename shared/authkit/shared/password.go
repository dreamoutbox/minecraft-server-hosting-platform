package shared

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a plaintext password using bcrypt with standard cost.
//
// Arguments:
//   - password: Raw plaintext password to hash.
//
// Example usage:
//
//	hash, err := shared.HashPassword("secret-pass")
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed hashing password: %w", err)
	}
	return string(bytes), nil
}

// CheckPassword verifies a plaintext password against a stored bcrypt hash.
//
// Arguments:
//   - hash: Stored bcrypt hashed string.
//   - password: Plaintext password provided during authentication.
//
// Example usage:
//
//	if !shared.CheckPassword(user.PasswordHash, "attempted-pass") { ... }
func CheckPassword(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateRandomPassword creates a cryptographically random URL-safe password string.
//
// Arguments:
//   - numBytes: Number of random entropy bytes to encode before base64 encoding.
//
// Example usage:
//
//	pwd, err := shared.GenerateRandomPassword(32)
func GenerateRandomPassword(numBytes int) (string, error) {
	if numBytes <= 0 {
		return "", errors.New("numBytes must be positive")
	}
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
