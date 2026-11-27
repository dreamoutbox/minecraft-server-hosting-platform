package session

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// CookieConfig defines attributes applied consistently when setting and clearing session cookies.
type CookieConfig struct {
	Name          string
	MaxAgeSeconds int
	Secure        bool
	SameSite      http.SameSite
	Path          string
	Domain        string
}

// DefaultCookieConfig constructs a CookieConfig following web security best practices:
// HttpOnly is always enforced, SameSite defaults to Lax, and Path defaults to "/".
//
// Arguments:
//   - name: Cookie name (defaults to DefaultCookieName if empty).
//   - ttl: Cookie Max-Age duration (defaults to DefaultTTL if <= 0).
//   - secure: Whether to restrict cookie transmission to HTTPS.
//
// Example usage:
//
//	cfg := session.DefaultCookieConfig("session", 24*time.Hour, true)
func DefaultCookieConfig(name string, ttl time.Duration, secure bool) CookieConfig {
	if name == "" {
		name = DefaultCookieName
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return CookieConfig{
		Name:          name,
		MaxAgeSeconds: int(ttl.Seconds()),
		Secure:        secure,
		SameSite:      http.SameSiteLaxMode,
		Path:          "/",
	}
}

func normalizeCookieConfig(cfg CookieConfig) CookieConfig {
	if cfg.Name == "" {
		cfg.Name = DefaultCookieName
	}
	if cfg.Path == "" {
		cfg.Path = "/"
	}
	if cfg.SameSite == 0 {
		cfg.SameSite = http.SameSiteLaxMode
	}
	if cfg.MaxAgeSeconds <= 0 {
		cfg.MaxAgeSeconds = int(DefaultTTL.Seconds())
	}
	return cfg
}

// SetCookie writes an HttpOnly session cookie with the configured security attributes.
//
// Arguments:
//   - c: Active Gin request context.
//   - cfg: Cookie configuration specifying name, Max-Age, Secure, SameSite, Path, and Domain.
//   - rawToken: Opaque session token string returned by Store.Create.
//
// Example usage:
//
//	session.SetCookie(c, cookieCfg, rawToken)
func SetCookie(c *gin.Context, cfg CookieConfig, rawToken string) {
	norm := normalizeCookieConfig(cfg)
	c.SetSameSite(norm.SameSite)
	c.SetCookie(norm.Name, rawToken, norm.MaxAgeSeconds, norm.Path, norm.Domain, norm.Secure, true)
}

// ClearCookie expires the session cookie on the client browser using the exact same
// Path, Domain, SameSite, and Secure attributes used when setting it.
//
// Arguments:
//   - c: Active Gin request context.
//   - cfg: Cookie configuration matching the cookie to clear.
//
// Example usage:
//
//	session.ClearCookie(c, cookieCfg)
func ClearCookie(c *gin.Context, cfg CookieConfig) {
	norm := normalizeCookieConfig(cfg)
	c.SetSameSite(norm.SameSite)
	c.SetCookie(norm.Name, "", -1, norm.Path, norm.Domain, norm.Secure, true)
}

// ReadCookie extracts the raw session token from the incoming request cookie.
//
// Arguments:
//   - c: Active Gin request context.
//   - cfg: Cookie configuration specifying the cookie name.
//
// Example usage:
//
//	rawToken, err := session.ReadCookie(c, cookieCfg)
func ReadCookie(c *gin.Context, cfg CookieConfig) (string, error) {
	norm := normalizeCookieConfig(cfg)
	return c.Cookie(norm.Name)
}
