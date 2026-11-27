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
