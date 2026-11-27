package users

import (
	"net/http"

	"github.com/dreamoutbox/go-authkit"
	"github.com/gin-gonic/gin"
)

// ShowLogin renders the login page.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /login
func (h *Handler) ShowLogin(c *gin.Context) {
	h.view.RenderView(c, "login.html", nil)
}

// ProcessLogin authenticates credentials and establishes browser session cookie.
//
// Arguments:
//   - c: Active Gin request context with email and password form fields.
//
// Example usage:
//
//	POST /login
func (h *Handler) ProcessLogin(c *gin.Context) {
	email := c.PostForm("email")
	password := c.PostForm("password")

	user, err := authkit.Authenticate(h.db, email, password)
	if err != nil {
		h.view.RenderView(c, "login.html", map[string]interface{}{
			"FlashError": "Invalid email or password",
		})
		return
	}

	if err := h.authsessions.SetSession(c, user.ID); err != nil {
		h.view.RenderView(c, "login.html", map[string]interface{}{"FlashError": "Failed generating session"})
		return
	}

	c.Redirect(http.StatusFound, "/dashboard")
}
