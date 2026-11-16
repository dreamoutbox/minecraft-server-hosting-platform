package authkit

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// TokenType designates the intended purpose of an issued JWT.
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
	TokenTypeLogin   TokenType = "login"
)

// CustomClaims holds identity and authorization attributes embedded within JWT payloads.
type CustomClaims struct {
	UserID    uint      `json:"user_id"`
	Email     string    `json:"email"`
	Role      UserRole  `json:"role"`
	TokenType TokenType `json:"token_type"`
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
//
//	hash, err := authkit.HashPassword("secret-pass")
//	if err != nil {
//	    return err
//	}
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
//	if !authkit.CheckPassword(user.PasswordHash, "attempted-pass") {
//	    return errors.New("invalid credentials")
//	}
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
//	pwd, err := authkit.GenerateRandomPassword(32)
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

// signClaims signs CustomClaims using HS256 and the provided HMAC secret.
func signClaims(claims CustomClaims, secret string) (string, error) {
	tokenObj := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tokenObj.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("failed signing %s token: %w", claims.TokenType, err)
	}
	return signed, nil
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
//
//	pair, err := authkit.GenerateTokenPair(user.ID, user.Email, user.Role, "jwt-secret")
//	if err != nil {
//	    return nil, err
//	}
func GenerateTokenPair(userID uint, email string, role UserRole, secret string) (*TokenPair, error) {
	now := time.Now()
	accessExpiry := now.Add(15 * time.Minute)
	refreshExpiry := now.Add(7 * 24 * time.Hour)

	accessTokenStr, err := signClaims(CustomClaims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		TokenType: TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExpiry),
		},
	}, secret)
	if err != nil {
		return nil, err
	}

	refreshTokenStr, err := signClaims(CustomClaims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		TokenType: TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
		},
	}, secret)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessTokenStr,
		RefreshToken: refreshTokenStr,
		ExpiresAt:    accessExpiry,
	}, nil
}

// GenerateLoginToken mints a short-lived, single-use login JWT with a unique JTI claim.
//
// Arguments:
//   - userID: Database primary key of the target user.
//   - email: Account email address.
//   - role: Authorization role tier of the user.
//   - secret: HMAC signing secret key.
//   - ttl: Token lifetime duration before expiration.
//
// Example usage:
//
//	tok, exp, jti, err := authkit.GenerateLoginToken(user.ID, user.Email, user.Role, secret, 60*time.Second)
func GenerateLoginToken(userID uint, email string, role UserRole, secret string, ttl time.Duration) (string, time.Time, string, error) {
	if ttl <= 0 {
		return "", time.Time{}, "", errors.New("ttl must be positive")
	}

	now := time.Now()
	expiresAt := now.Add(ttl)
	jti := uuid.NewString()

	tokenStr, err := signClaims(CustomClaims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		TokenType: TokenTypeLogin,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}, secret)
	if err != nil {
		return "", time.Time{}, "", err
	}

	return tokenStr, expiresAt, jti, nil
}

// ParseAndValidateToken decodes a token string, verifies cryptographic signature and expiration,
// and ensures the token matches the expected token type.
//
// Arguments:
//   - tokenStr: Raw JWT string received in header, cookie, or query payload.
//   - secret: HMAC signing secret key.
//   - expectedType: Expected token purpose (TokenTypeAccess, TokenTypeRefresh, or TokenTypeLogin).
//
// Example usage:
//
//	claims, err := authkit.ParseAndValidateToken(rawToken, "jwt-secret", authkit.TokenTypeAccess)
//	if err != nil {
//	    return nil, err
//	}
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
