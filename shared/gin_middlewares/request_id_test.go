package gin_middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		reqID, _ := c.Get(ContextRequestIDKey)
		c.String(http.StatusOK, reqID.(string))
	})

	// 1. Without header -> generated
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	r.ServeHTTP(w1, req1)

	respID1 := w1.Header().Get(RequestIDHeader)
	if respID1 == "" {
		t.Fatal("expected X-Request-ID response header to be populated")
	}

	// 2. With header -> preserved
	customID := "custom-trace-id-12345"
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/ping", nil)
	req2.Header.Set(RequestIDHeader, customID)
	r.ServeHTTP(w2, req2)

	respID2 := w2.Header().Get(RequestIDHeader)
	if respID2 != customID {
		t.Errorf("expected preserved request id %s, got %s", customID, respID2)
	}
}
