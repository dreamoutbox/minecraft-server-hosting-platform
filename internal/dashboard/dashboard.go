package dashboard

import (
	"net/http"

	"github.com/dreamoutbox/go-authkit/session"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/admin"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billings"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/mcservers"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/users"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/jobworker/jobs"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
)

// Service coordinates dashboard views and delegates exclusively to public domain service interfaces.
type Service struct {
	db           *gorm.DB
	hostingSvc   *hosting.Service
	backupSvc    *backup.Service
	billingSvc   *billing.Service
	migrationSvc *hosting.MigrationService
	jobSvc       *jobs.Service
	authsessions *session.Manager
	renderer     *view.Renderer
}

// NewService instantiates a decoupled dashboard controller.
//
// Arguments:
//   - db: Database handle for queries.
//   - hostingSvc: Hosting lifecycle service.
//   - backupSvc: Backup and restore service.
//   - billingSvc: Checkout order and payment history service.
//   - migrationSvc: Host migration coordinator.
//   - jobSvc: Async job queue coordinator.
//   - authsessions: Session manager handling cookie creation, validation, and logout.
//
// Example usage:
//
//	dash := dashboard.NewService(db, hostingSvc, backupSvc, billingSvc, migrationSvc, jobSvc, authsessions)
func NewService(
	db *gorm.DB,
	hostingSvc *hosting.Service,
	backupSvc *backup.Service,
	billingSvc *billing.Service,
	migrationSvc *hosting.MigrationService,
	jobSvc *jobs.Service,
	authsessions *session.Manager,
) *Service {
	return &Service{
		db:           db,
		hostingSvc:   hostingSvc,
		backupSvc:    backupSvc,
		billingSvc:   billingSvc,
		migrationSvc: migrationSvc,
		jobSvc:       jobSvc,
		authsessions: authsessions,
		renderer:     view.NewRenderer(),
	}
}

// RegisterRoutes registers all dashboard web pages and form actions onto the router.
//
// Arguments:
//   - r: Gin engine.
//
// Example usage:
//
//	dashService.RegisterRoutes(router)
func (s *Service) RegisterRoutes(r *gin.Engine) {
	usersHandler := users.NewHandler(s.db, s.authsessions, s.renderer)
	billingsHandler := billings.NewHandler(s.db, s.billingSvc, s.renderer)
	mcHandler := mcservers.NewHandler(s.db, s.hostingSvc, s.backupSvc, s.migrationSvc, s.jobSvc, s.renderer)
	adminHandler := admin.NewHandler(s.db, s.renderer)

	// Public auth pages
	usersHandler.RegisterPublicRoutes(r)

	// Protected dashboard pages
	dash := r.Group("/dashboard")
	dash.Use(s.authsessions.Middleware("/login"))
	{
		mcHandler.RegisterRoutes(dash)
		billingsHandler.RegisterRoutes(dash)
		usersHandler.RegisterDashboardRoutes(dash)

		adminGroup := dash.Group("/admin")
		adminHandler.RegisterRoutes(adminGroup)
	}

	// Redirect root to /dashboard
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})
}
