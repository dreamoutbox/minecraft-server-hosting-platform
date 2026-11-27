package gin_middlewares

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDHeader defines the standard HTTP header used for distributed tracing.
const RequestIDHeader = "X-Request-ID"

// ContextRequestIDKey is the Gin context key storing the correlation ID.
const ContextRequestIDKey = "request_id"

// RequestIDMiddleware injects a unique tracing correlation ID into incoming requests and response headers.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	router.Use(gin_middlewares.RequestIDMiddleware())
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader(RequestIDHeader)
		if reqID == "" {
			reqID = uuid.New().String()
		}

		c.Set(ContextRequestIDKey, reqID)
		c.Header(RequestIDHeader, reqID)
		c.Next()
	}
}
