package billings

import (
	"net/http"
	"strconv"
	"strings"

	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// ShowBuyServer displays the Stripe Checkout server purchase page for users.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard/servers/buy
func (h *Handler) ShowBuyServer(c *gin.Context) {
	var plans []domain.Plan
	_ = h.db.Find(&plans)

	h.view.RenderView(c, "server_buy.html", map[string]interface{}{
		"Plans":       plans,
		"ServerTypes": domain.AllServerTypes(),
	})
}

// ProcessBuyServer creates a Stripe Checkout session for a new server and redirects the user to Stripe.
//
// Arguments:
//   - c: Active Gin request context with form fields name, plan_id, server_type, server_version.
//
// Example usage:
//
//	POST /dashboard/servers/buy
func (h *Handler) ProcessBuyServer(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	name := strings.TrimSpace(c.PostForm("name"))
	planIDVal, _ := strconv.ParseUint(c.PostForm("plan_id"), 10, 64)
	serverType := domain.ServerType(strings.ToUpper(strings.TrimSpace(c.PostForm("server_type"))))
	serverVersion := strings.TrimSpace(c.PostForm("server_version"))

	cs, _, err := h.billingSvc.CreateServerCheckout(
		c.Request.Context(),
		userID,
		uint(planIDVal),
		name,
		serverType,
		serverVersion,
	)
	if err != nil {
		var plans []domain.Plan
		_ = h.db.Find(&plans)
		h.view.RenderView(c, "server_buy.html", map[string]interface{}{
			"Plans":       plans,
			"ServerTypes": domain.AllServerTypes(),
			"FlashError":  "Failed initiating checkout: " + err.Error(),
		})
		return
	}

	c.Redirect(http.StatusSeeOther, cs.CheckoutURL)
}
