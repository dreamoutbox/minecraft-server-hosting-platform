package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// APIKeyMiddleware authenticates management requests using a Bearer API key
// compared in constant time.
//
// Arguments:
//   - expectedKey: Node secret configured via FILESERVER_API_KEY.
//
// Example usage:
//
//	mgmt := r.Group("/api/v1")
//	mgmt.Use(api.APIKeyMiddleware(cfg.APIKey))
func APIKeyMiddleware(expectedKey string) gin.HandlerFunc {
	expectedBytes := []byte(expectedKey)

	return func(c *gin.Context) {
		if expectedKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "fileserver API key is not configured"})
			return
		}

		authHeader := c.GetHeader("Authorization")
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or malformed bearer token"})
			return
		}

		token := strings.TrimSpace(parts[1])
		if subtle.ConstantTimeCompare([]byte(token), expectedBytes) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
			return
		}

		c.Next()
	}
}
