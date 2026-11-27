package gin_middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

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
