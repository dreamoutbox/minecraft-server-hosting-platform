package admin

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

// Handler serves administrator-only dashboard list and plan CRUD routes.
type Handler struct {
	db   *gorm.DB
	view *view.Renderer
}

// NewHandler constructs a dashboard admin route handler.
//
// Arguments:
//   - db: Database handle for admin queries and plan CRUD.
//   - renderer: Shared dashboard template renderer.
//
// Example usage:
//
//	adminHandler := admin.NewHandler(db, renderer)
func NewHandler(db *gorm.DB, renderer *view.Renderer) *Handler {
	return &Handler{
		db:   db,
		view: renderer,
	}
}

// RegisterRoutes mounts admin routes under /dashboard/admin.
//
// Arguments:
//   - adminGroup: Authenticated /dashboard/admin router group.
//
// Example usage:
//
//	adminHandler.RegisterRoutes(adminGroup)
func (h *Handler) RegisterRoutes(adminGroup *gin.RouterGroup) {
	adminGroup.GET("/servers", h.AdminListServers)
	adminGroup.GET("/orders", h.AdminListOrders)
	adminGroup.GET("/payments", h.AdminListPayments)
	adminGroup.GET("/jobs", h.AdminListJobs)
	adminGroup.GET("/logs", h.AdminListLogs)
	adminGroup.GET("/plans", h.AdminListPlans)
	adminGroup.POST("/plans", h.AdminCreatePlan)
	adminGroup.POST("/plans/:id", h.AdminUpdatePlan)
	adminGroup.POST("/plans/:id/delete", h.AdminDeletePlan)
}

func parsePlanForm(c *gin.Context) (domain.Plan, error) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		return domain.Plan{}, errors.New("plan name is required")
	}
	if len(name) > 100 {
		return domain.Plan{}, errors.New("plan name must be 100 characters or fewer")
	}

	cpuLimit, err := strconv.Atoi(strings.TrimSpace(c.PostForm("cpu_limit")))
	if err != nil || cpuLimit <= 0 {
		return domain.Plan{}, errors.New("CPU limit must be a positive integer")
	}

	memLimitMB, err := strconv.Atoi(strings.TrimSpace(c.PostForm("mem_limit_mb")))
	if err != nil || memLimitMB <= 0 {
		return domain.Plan{}, errors.New("memory limit (MB) must be a positive integer")
	}

	diskLimitGB, err := strconv.Atoi(strings.TrimSpace(c.PostForm("disk_limit_gb")))
	if err != nil || diskLimitGB <= 0 {
		return domain.Plan{}, errors.New("disk limit (GB) must be a positive integer")
	}

	priceCents, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("price_cents")), 10, 64)
	if err != nil || priceCents < 0 {
		return domain.Plan{}, errors.New("price (cents) must be a non-negative integer")
	}

	return domain.Plan{
		Name:        name,
		CPULimit:    cpuLimit,
		MemLimitMB:  memLimitMB,
		DiskLimitGB: diskLimitGB,
		PriceCents:  priceCents,
	}, nil
}
