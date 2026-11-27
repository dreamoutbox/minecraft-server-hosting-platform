package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// AdminCreatePlan creates a new hosting plan tier.
//
// Arguments:
//   - c: Active Gin request context with form fields name, cpu_limit, mem_limit_mb, disk_limit_gb, and price_cents.
//
// Example usage:
//
//	POST /dashboard/admin/plans
func (h *Handler) AdminCreatePlan(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	plan, err := parsePlanForm(c)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}

	var existingCount int64
	if err := h.db.WithContext(c.Request.Context()).Model(&domain.Plan{}).Where("LOWER(name) = ?", strings.ToLower(plan.Name)).Count(&existingCount).Error; err == nil && existingCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(fmt.Sprintf("Plan %q already exists", plan.Name)))
		return
	}

	if err := h.db.WithContext(c.Request.Context()).Create(&plan).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Failed creating plan: "+err.Error()))
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/admin/plans?success=Plan+created")
}
