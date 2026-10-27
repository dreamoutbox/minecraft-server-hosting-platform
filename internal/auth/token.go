package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

// TokenType designates whether a token is scoped for access or session refresh.
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
)

// CustomClaims holds identity and authorization attributes embedded within JWT payloads.
type CustomClaims struct {
	UserID    uint            `json:"user_id"`
	Email     string          `json:"email"`
	Role      domain.UserRole `json:"role"`
	TokenType TokenType       `json:"token_type"`
	jwt.RegisteredClaims
}

// TokenPair bundles newly minted access and refresh authentication tokens.
type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// HashPassword hashes a plaintext password using bcrypt with standard cost.
//
// Arguments:
//   - password: Raw plaintext password to hash.
//
// Example usage:
//   hash, err := auth.HashPassword("secret-pass")
//   if err != nil {
//       return err
//   }
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
//   if !auth.CheckPassword(user.PasswordHash, "attempted-pass") {
//       return errors.New("invalid credentials")
//   }
func CheckPassword(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateTokenPair mints both a short-lived access token and a longer-lived refresh token.
//
// Arguments:
//   - userID: Database primary key of the authenticated user.
//   - email: Account email address.
//   - role: Authorization role tier of the user.
//   - secret: HMAC signing secret key.
//
// Example usage:
//   pair, err := auth.GenerateTokenPair(user.ID, user.Email, user.Role, "jwt-secret")
//   if err != nil {
//       return nil, err
//   }
func GenerateTokenPair(userID uint, email string, role domain.UserRole, secret string) (*TokenPair, error) {
	now := time.Now()
	accessExpiry := now.Add(15 * time.Minute)
	refreshExpiry := now.Add(7 * 24 * time.Hour)

	accessClaims := CustomClaims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		TokenType: TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExpiry),
		},
	}

	accessTokenObj := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenStr, err := accessTokenObj.SignedString([]byte(secret))
	if err != nil {
		return nil, fmt.Errorf("failed signing access token: %w", err)
	}

	refreshClaims := CustomClaims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		TokenType: TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
		},
	}

	refreshTokenObj := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenStr, err := refreshTokenObj.SignedString([]byte(secret))
	if err != nil {
		return nil, fmt.Errorf("failed signing refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessTokenStr,
		RefreshToken: refreshTokenStr,
		ExpiresAt:    accessExpiry,
	}, nil
}

// ParseAndValidateToken decodes a token string, verifies cryptographic signature and expiration,
// and ensures the token matches the expected token type (access vs refresh).
//
// Arguments:
//   - tokenStr: Raw JWT string received in header or payload.
//   - secret: HMAC signing secret key.
//   - expectedType: Expected token purpose (TokenTypeAccess or TokenTypeRefresh).
//
// Example usage:
//   claims, err := auth.ParseAndValidateToken(rawToken, "jwt-secret", auth.TokenTypeAccess)
//   if err != nil {
//       return nil, err
//   }
func ParseAndValidateToken(tokenStr string, secret string, expectedType TokenType) (*CustomClaims, error) {
	parsedToken, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("token parsing failed: %w", err)
	}

	claims, ok := parsedToken.Claims.(*CustomClaims)
	if !ok || !parsedToken.Valid {
		return nil, errors.New("invalid or unverified token claims")
	}

	if claims.TokenType != expectedType {
		return nil, fmt.Errorf("invalid token type: expected %s, got %s", expectedType, claims.TokenType)
	}

	return claims, nil
}
