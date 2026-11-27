package billings

import (
	"net/http"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// ExtendServerDuration creates a Stripe Checkout session to extend a server by 30 days.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/extend
func (h *Handler) ExtendServerDuration(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	serverIDVal, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	cs, _, err := h.billingSvc.CreateExtendCheckout(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, view.ServerRedirectURL(serverIDVal, "/settings", "error", err.Error())+"#extend-server")
		return
	}

	c.Redirect(http.StatusSeeOther, cs.CheckoutURL)
}
