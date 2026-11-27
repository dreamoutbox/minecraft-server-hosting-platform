package billings

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// Handler serves billing, checkout purchase, and server duration extension dashboard routes.
type Handler struct {
	db         *gorm.DB
	billingSvc *billing.Service
	view       *view.Renderer
}

// NewHandler constructs a dashboard billings route handler.
//
// Arguments:
//   - db: Database handle for plan queries.
//   - billingSvc: Billing service for checkout sessions, orders, and payments.
//   - renderer: Shared dashboard template renderer.
//
// Example usage:
//
//	billingsHandler := billings.NewHandler(db, billingSvc, renderer)
func NewHandler(db *gorm.DB, billingSvc *billing.Service, renderer *view.Renderer) *Handler {
	return &Handler{
		db:         db,
		billingSvc: billingSvc,
		view:       renderer,
	}
}

// RegisterRoutes mounts billing and purchase routes under /dashboard.
//
// Arguments:
//   - dash: Authenticated /dashboard router group.
//
// Example usage:
//
//	billingsHandler.RegisterRoutes(dash)
func (h *Handler) RegisterRoutes(dash *gin.RouterGroup) {
	dash.GET("/servers/buy", h.ShowBuyServer)
	dash.POST("/servers/buy", h.ProcessBuyServer)
	dash.POST("/servers/:id/extend", h.ExtendServerDuration)
	dash.GET("/billing", h.ShowBilling)
}
