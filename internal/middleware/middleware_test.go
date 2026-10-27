package middleware

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

func TestRateLimitMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Rate limit: 2 tokens per second, burst 2
	r.Use(RateLimitMiddleware(2, 2))
	r.GET("/limited", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// First two requests succeed (burst = 2)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/limited", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d expected 200, got %d", i+1, w.Code)
		}
	}

	// Third request immediately after exceeds burst -> 429
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodGet, "/limited", nil)
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests, got %d", w3.Code)
	}
}
