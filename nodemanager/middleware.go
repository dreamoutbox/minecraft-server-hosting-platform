package nodemanager

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
)

// APIKeyHeader is the HTTP header used to supply the worker daemon API key.
const APIKeyHeader = nodemanagerclient.APIKeyHeader

// APIKeyMiddleware ensures every request to the worker daemon possesses a valid API key.
// It verifies the secret using constant-time comparison to prevent timing side-channel attacks.
//
// Arguments:
//   - expectedKey: Host secret configured at worker deployment.
//
// Example usage:
//
//	r.Use(worker.APIKeyMiddleware("node-secret-key"))
func APIKeyMiddleware(expectedKey string) gin.HandlerFunc {
	expectedBytes := []byte(expectedKey)

	return func(c *gin.Context) {
		key := c.GetHeader(APIKeyHeader)
		if key == "" {
			authHeader := c.GetHeader("Authorization")
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				key = strings.TrimSpace(authHeader[7:])
			}
		}

		if key == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing worker API key in header",
			})
			return
		}

		keyBytes := []byte(key)
		if subtle.ConstantTimeCompare(keyBytes, expectedBytes) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid worker API key",
			})
			return
		}

		c.Next()
	}
}
