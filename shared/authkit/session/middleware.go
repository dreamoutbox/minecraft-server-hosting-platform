package session

import (
	"net/http"

	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// Middleware validates incoming browser session cookies against the database-backed Store.
// On success, it loads the live shared.User record (reflecting current role/existence) and
// populates shared context keys. On failure, it clears invalid cookies and either redirects
// to loginPath (when non-empty) or aborts with HTTP 401 JSON.
//
// Arguments:
//   - store: Session store backed by the database containing users and sessions tables.
//   - cfg: Cookie configuration matching the cookie set at login.
//   - loginPath: Redirect destination when unauthenticated; if empty, responds with 401 JSON.
//
// Example usage:
//
//	dash.Use(session.Middleware(sessionStore, cookieCfg, "/login"))
func Middleware(store *Store, cfg CookieConfig, loginPath string) gin.HandlerFunc {
	norm := normalizeCookieConfig(cfg)
	return func(c *gin.Context) {
		rawToken, err := ReadCookie(c, norm)
		if err != nil || rawToken == "" {
			rejectSession(c, loginPath)
			return
		}

		sess, err := store.GetValid(c.Request.Context(), rawToken)
		if err != nil {
			ClearCookie(c, norm)
			rejectSession(c, loginPath)
			return
		}

		var user shared.User
		if err := store.DB().WithContext(c.Request.Context()).First(&user, sess.UserID).Error; err != nil {
			_ = store.Delete(c.Request.Context(), rawToken)
			ClearCookie(c, norm)
			rejectSession(c, loginPath)
			return
		}

		c.Set(shared.ContextUserIDKey, user.ID)
		c.Set(shared.ContextEmailKey, user.Email)
		c.Set(shared.ContextRoleKey, user.Role)
		c.Next()
	}
}

func rejectSession(c *gin.Context, loginPath string) {
	if loginPath != "" {
		c.Redirect(http.StatusFound, loginPath)
		c.Abort()
		return
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid session"})
}
