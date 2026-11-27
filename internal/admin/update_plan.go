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

// AdminUpdatePlan updates an existing hosting plan tier.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form fields name, cpu_limit, mem_limit_mb, disk_limit_gb, price_cents.
//
// Example usage:
//
//	POST /dashboard/admin/plans/:id
func (h *Handler) AdminUpdatePlan(c *gin.Context) {
	if !view.RequireAdmin(c, h.view, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	planID, ok := view.ParseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Invalid+plan+ID")
		return
	}

	var plan domain.Plan
	if err := h.db.WithContext(c.Request.Context()).First(&plan, planID).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Plan+not+found")
		return
	}

	parsed, err := parsePlanForm(c)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}

	var duplicateCount int64
	if err := h.db.WithContext(c.Request.Context()).Model(&domain.Plan{}).
		Where("LOWER(name) = ? AND id <> ?", strings.ToLower(parsed.Name), planID).
		Count(&duplicateCount).Error; err == nil && duplicateCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(fmt.Sprintf("Plan %q already exists", parsed.Name)))
		return
	}

	plan.Name = parsed.Name
	plan.CPULimit = parsed.CPULimit
	plan.MemLimitMB = parsed.MemLimitMB
	plan.DiskLimitGB = parsed.DiskLimitGB
	plan.PriceCents = parsed.PriceCents

	if err := h.db.WithContext(c.Request.Context()).Save(&plan).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Failed updating plan: "+err.Error()))
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/admin/plans?success=Plan+updated")
}
