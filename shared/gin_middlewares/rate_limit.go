package gin_middlewares

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// clientLimiter tracks rate limits for an individual IP address.
type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter provides per-IP token bucket rate limiting with automatic idle eviction.
type IPRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*clientLimiter
	r       rate.Limit
	b       int
}

// NewIPRateLimiter constructs an IP rate limiter.
//
// Arguments:
//   - r: Tokens per second replenished.
//   - b: Burst capacity ceiling.
//
// Example usage:
//
//	limiter := gin_middlewares.NewIPRateLimiter(5, 10)
func NewIPRateLimiter(r rate.Limit, b int) *IPRateLimiter {
	limiter := &IPRateLimiter{
		clients: make(map[string]*clientLimiter),
		r:       r,
		b:       b,
	}

	// Periodically clean up stale client entries
	go func() {
		for {
			time.Sleep(3 * time.Minute)
			limiter.mu.Lock()
			for ip, client := range limiter.clients {
				if time.Since(client.lastSeen) > 5*time.Minute {
					delete(limiter.clients, ip)
				}
			}
			limiter.mu.Unlock()
		}
	}()

	return limiter
}

// getLimiter retrieves or initializes a rate limiter for the given IP address.
func (i *IPRateLimiter) getLimiter(ip string) *rate.Limiter {
	i.mu.Lock()
	defer i.mu.Unlock()

	client, exists := i.clients[ip]
	if !exists {
		l := rate.NewLimiter(i.r, i.b)
		i.clients[ip] = &clientLimiter{limiter: l, lastSeen: time.Now()}
		return l
	}

	client.lastSeen = time.Now()
	return client.limiter
}

// RateLimitMiddleware returns a Gin handler enforcing rate limits per client IP.
//
// Arguments:
//   - r: Replenishment rate in requests per second.
//   - b: Burst ceiling.
//
// Example usage:
//
//	sensitiveRoutes.Use(gin_middlewares.RateLimitMiddleware(5, 10))
func RateLimitMiddleware(r rate.Limit, b int) gin.HandlerFunc {
	limiter := NewIPRateLimiter(r, b)

	return func(c *gin.Context) {
		ip := c.ClientIP()
		l := limiter.getLimiter(ip)
		if !l.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded; please slow down requests",
			})
			return
		}
		c.Next()
	}
}
