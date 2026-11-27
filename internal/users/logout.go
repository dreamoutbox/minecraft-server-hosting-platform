package users

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Logout terminates user browser session.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /logout
func (h *Handler) Logout(c *gin.Context) {
	_ = h.authsessions.Logout(c)
	c.Redirect(http.StatusFound, "/login?success=Logged+out+successfully")
}
