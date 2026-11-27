package users

import (
	"errors"
	"net/http"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"
)

// ShowRegister renders the account creation page.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /register
func (h *Handler) ShowRegister(c *gin.Context) {
	h.view.RenderView(c, "register.html", nil)
}

// ProcessRegister validates input, creates a user account, and redirects to the login page.
//
// Arguments:
//   - c: Active Gin request context with email and password form fields.
//
// Example usage:
//
//	POST /register
func (h *Handler) ProcessRegister(c *gin.Context) {
	_, err := authkit.RegisterUser(h.db, c.PostForm("email"), c.PostForm("password"))
	if err != nil {
		msg := "Failed saving account"
		if errors.Is(err, authkit.ErrInvalidEmail) ||
			errors.Is(err, authkit.ErrPasswordTooShort) ||
			errors.Is(err, authkit.ErrEmailAlreadyExists) {
			msg = err.Error()
		}
		h.view.RenderView(c, "register.html", map[string]interface{}{"FlashError": msg})
		return
	}

	c.Redirect(http.StatusFound, "/login?success=Account+created.+Please+sign+in.")
}
