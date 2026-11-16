package authkit

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ContextUserIDKey = "auth_user_id"
	ContextEmailKey  = "auth_user_email"
	ContextRoleKey   = "auth_user_role"
)

// setContextClaims stores verified JWT identity claims onto the Gin request context.
func setContextClaims(c *gin.Context, claims *CustomClaims) {
	c.Set(ContextUserIDKey, claims.UserID)
	c.Set(ContextEmailKey, claims.Email)
	c.Set(ContextRoleKey, claims.Role)
}

// AuthMiddleware inspects incoming requests for a valid Bearer JWT access token.
// If valid, sets identity values in the Gin context; otherwise halts with HTTP 401.
//
// Arguments:
//   - secret: HMAC signing secret key used to verify tokens.
//
// Example usage:
//
//	api := router.Group("/api")
//	api.Use(authkit.AuthMiddleware(cfg.JWTSecret))
func AuthMiddleware(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "malformed authorization header"})
			return
		}

		claims, err := ParseAndValidateToken(parts[1], secret, TokenTypeAccess)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		setContextClaims(c, claims)
		c.Next()
	}
}

// CookieAuthMiddleware inspects browser cookies for a valid JWT access token.
// On missing or invalid cookies, clears the cookie and either redirects to loginPath
// (when non-empty) or aborts with HTTP 401.
//
// Arguments:
//   - secret: HMAC signing secret key used to verify tokens.
//   - cookieName: Name of the HTTP cookie carrying the access JWT.
//   - loginPath: Redirect destination when unauthenticated; if empty, responds 401 JSON.
//
// Example usage:
//
//	dash.Use(authkit.CookieAuthMiddleware(jwtSecret, "token", "/login"))
func CookieAuthMiddleware(secret string, cookieName string, loginPath string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr, err := c.Cookie(cookieName)
		if err != nil || tokenStr == "" {
			rejectCookieAuth(c, loginPath)
			return
		}

		claims, err := ParseAndValidateToken(tokenStr, secret, TokenTypeAccess)
		if err != nil {
			ClearSessionCookie(c, cookieName)
			rejectCookieAuth(c, loginPath)
			return
		}

		setContextClaims(c, claims)
		c.Next()
	}
}

func rejectCookieAuth(c *gin.Context, loginPath string) {
	if loginPath != "" {
		c.Redirect(http.StatusFound, loginPath)
		c.Abort()
		return
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid session cookie"})
}

// SetSessionCookie writes an HttpOnly SameSite=Lax session cookie containing a JWT.
//
// Arguments:
//   - c: Active Gin request context.
//   - cookieName: Name of the cookie to set.
//   - token: Signed JWT string.
//   - maxAgeSeconds: Cookie expiration duration in seconds.
//   - secure: Whether to restrict cookie transmission to HTTPS.
//
// Example usage:
//
//	authkit.SetSessionCookie(c, "token", tokens.AccessToken, 3600*24, false)
func SetSessionCookie(c *gin.Context, cookieName string, token string, maxAgeSeconds int, secure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieName, token, maxAgeSeconds, "/", "", secure, true)
}

// ClearSessionCookie expires the named session cookie on the client browser.
//
// Arguments:
//   - c: Active Gin request context.
//   - cookieName: Name of the cookie to clear.
//
// Example usage:
//
//	authkit.ClearSessionCookie(c, "token")
func ClearSessionCookie(c *gin.Context, cookieName string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieName, "", -1, "/", "", false, true)
}

// GetUserIDFromContext retrieves the authenticated user ID set by auth middleware.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	userID, err := authkit.GetUserIDFromContext(c)
//	if err != nil {
//	    c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
//	    return
//	}
func GetUserIDFromContext(c *gin.Context) (uint, error) {
	val, exists := c.Get(ContextUserIDKey)
	if !exists {
		return 0, errors.New("user ID missing from context")
	}
	id, ok := val.(uint)
	if !ok {
		return 0, errors.New("user ID in context is not a valid uint")
	}
	return id, nil
}

// GetUserRoleFromContext retrieves the authenticated user role from Gin context.
// Defaults to UserRoleUser when missing or unrecognized.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	role := authkit.GetUserRoleFromContext(c)
func GetUserRoleFromContext(c *gin.Context) UserRole {
	val, exists := c.Get(ContextRoleKey)
	if !exists {
		return UserRoleUser
	}
	switch r := val.(type) {
	case UserRole:
		if r != "" {
			return r
		}
	case string:
		if r != "" {
			return UserRole(r)
		}
	}
	return UserRoleUser
}
