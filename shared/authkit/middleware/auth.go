package middleware

import (
	"net/http"
	"strings"

	"github.com/dreamoutbox/authkit/authkitjwt"
	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// setContextClaims stores verified JWT identity claims onto the Gin request context.
func setContextClaims(c *gin.Context, claims *authkitjwt.CustomClaims) {
	c.Set(shared.ContextUserIDKey, claims.UserID)
	c.Set(shared.ContextEmailKey, claims.Email)
	c.Set(shared.ContextRoleKey, claims.Role)
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
//	api.Use(middleware.AuthMiddleware(cfg.JWTSecret))
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

		claims, err := authkitjwt.ParseAndValidateToken(parts[1], secret, authkitjwt.TokenTypeAccess)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		setContextClaims(c, claims)
		c.Next()
	}
}
