package admin

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminDeletePlan deletes a hosting plan if no servers or orders reference it.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/admin/plans/:id/delete
func (h *Handler) AdminDeletePlan(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	planID, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Invalid+plan+ID")
		return
	}

	var serverCount int64
	if err := h.db.WithContext(c.Request.Context()).Model(&domain.Server{}).Where("plan_id = ?", planID).Count(&serverCount).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}
	if serverCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Cannot delete plan in use by existing servers"))
		return
	}

	var orderCount int64
	if err := h.db.WithContext(c.Request.Context()).Model(&domain.Order{}).Where("plan_id = ?", planID).Count(&orderCount).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}
	if orderCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Cannot delete plan referenced by existing orders"))
		return
	}

	res := h.db.WithContext(c.Request.Context()).Delete(&domain.Plan{}, planID)
	if res.Error != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Failed deleting plan: "+res.Error.Error()))
		return
	}
	if res.RowsAffected == 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Plan+not+found")
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/admin/plans?success=Plan+deleted")
}
