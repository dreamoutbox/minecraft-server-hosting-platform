package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/dreamoutbox/authkit"
)

const (
	// DefaultTTL is the fallback session lifetime when none is specified.
	DefaultTTL = 24 * time.Hour
	// DefaultCookieName is the default HTTP cookie name carrying the opaque session token.
	DefaultCookieName = "session"
	tokenEntropyBytes = 32
)

var (
	// ErrInvalidSession indicates the supplied session token is empty or not found in storage.
	ErrInvalidSession = errors.New("invalid session")
	// ErrSessionExpired indicates the session exists but has passed its expiration timestamp.
	ErrSessionExpired = errors.New("session expired")
	// ErrInvalidUserID indicates a zero user ID was supplied when creating a session.
	ErrInvalidUserID = errors.New("user ID must be non-zero")
	// ErrInvalidTTL indicates a non-positive session lifetime was supplied.
	ErrInvalidTTL = errors.New("session ttl must be positive")
)

// Session aliases authkit.Session so callers importing authkit/session share the exact same schema.
type Session = authkit.Session

// HashToken computes the SHA-256 hex digest of a raw opaque session token for safe DB storage.
//
// Arguments:
//   - rawToken: Opaque session token string received from the client cookie.
//
// Example usage:
//
//	hash := session.HashToken(rawToken)
func HashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

func generateRawToken() (string, error) {
	buf := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed generating session token entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
