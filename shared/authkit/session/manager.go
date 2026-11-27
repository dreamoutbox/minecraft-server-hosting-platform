package session

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Manager combines a database-backed Store with a CookieConfig so application handlers
// can start sessions, log out, and protect routes without managing tokens or cookies directly.
type Manager struct {
	store     *Store
	cookieCfg CookieConfig
}

// NewManager initializes a session Manager with a GORM-backed Store and CookieConfig.
//
// Arguments:
//   - db: Active GORM database handle containing users and sessions tables.
//   - cfg: Cookie configuration (name, MaxAgeSeconds, Secure, SameSite, Path, Domain).
//
// Example usage:
//
//	cfg := session.DefaultCookieConfig(session.DefaultCookieName, 24*time.Hour, true)
//	authsessions := session.NewManager(db, cfg)
func NewManager(db *gorm.DB, cfg CookieConfig) *Manager {
	norm := normalizeCookieConfig(cfg)
	ttl := time.Duration(norm.MaxAgeSeconds) * time.Second
	return &Manager{
		store:     NewStore(db, ttl),
		cookieCfg: norm,
	}
}

// Store returns the underlying session Store for maintenance operations such as PruneExpired.
func (m *Manager) Store() *Store {
	return m.store
}

// CookieConfig returns the normalized CookieConfig used by this Manager.
func (m *Manager) CookieConfig() CookieConfig {
	return m.cookieCfg
}

// SetSession creates a new server-side session for userID and writes the session cookie to c.
//
// Arguments:
//   - c: Active Gin request context.
//   - userID: Database primary key of the authenticated user.
//
// Example usage:
//
//	if err := authsessions.SetSession(c, user.ID); err != nil {
//	    ...
//	}
func (m *Manager) SetSession(c *gin.Context, userID uint) error {
	rawToken, _, err := m.store.Create(c.Request.Context(), userID, 0)
	if err != nil {
		return err
	}
	SetCookie(c, m.cookieCfg, rawToken)
	return nil
}

// Logout revokes the active session in the database (if present) and clears the session cookie.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	_ = authsessions.Logout(c)
func (m *Manager) Logout(c *gin.Context) error {
	var deleteErr error
	if rawToken, err := ReadCookie(c, m.cookieCfg); err == nil && rawToken != "" {
		deleteErr = m.store.Delete(c.Request.Context(), rawToken)
	}
	ClearCookie(c, m.cookieCfg)
	return deleteErr
}

// Middleware returns a Gin middleware that validates the session cookie against the Store.
//
// Arguments:
//   - loginPath: Redirect destination when unauthenticated; if empty, responds with 401 JSON.
//
// Example usage:
//
//	dash.Use(authsessions.Middleware("/login"))
func (m *Manager) Middleware(loginPath string) gin.HandlerFunc {
	return Middleware(m.store, m.cookieCfg, loginPath)
}
