package mcservers

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/jobworker/jobs"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/hosting"
)

// Handler serves Minecraft server management dashboard routes.
type Handler struct {
	db           *gorm.DB
	hostingSvc   *hosting.Service
	backupSvc    *backup.Service
	migrationSvc *hosting.MigrationService
	jobSvc       *jobs.Service
	view         *view.Renderer
}

// NewHandler constructs a dashboard Minecraft server route handler.
//
// Arguments:
//   - db: Database handle.
//   - hostingSvc: Hosting lifecycle service.
//   - backupSvc: Backup and restore service.
//   - migrationSvc: Host migration service.
//   - jobSvc: Async job queue service.
//   - renderer: Shared dashboard template renderer.
//
// Example usage:
//
//	mcHandler := mcservers.NewHandler(db, hostingSvc, backupSvc, migrationSvc, jobSvc, renderer)
func NewHandler(
	db *gorm.DB,
	hostingSvc *hosting.Service,
	backupSvc *backup.Service,
	migrationSvc *hosting.MigrationService,
	jobSvc *jobs.Service,
	renderer *view.Renderer,
) *Handler {
	return &Handler{
		db:           db,
		hostingSvc:   hostingSvc,
		backupSvc:    backupSvc,
		migrationSvc: migrationSvc,
		jobSvc:       jobSvc,
		view:         renderer,
	}
}

// RegisterRoutes mounts all Minecraft server dashboard routes under /dashboard.
//
// Arguments:
//   - dash: Authenticated /dashboard router group.
//
// Example usage:
//
//	mcHandler.RegisterRoutes(dash)
func (h *Handler) RegisterRoutes(dash *gin.RouterGroup) {
	dash.GET("", h.ListServers)
	dash.GET("/servers/new", h.ShowCreateServer)
	dash.POST("/servers/new", h.ProcessCreateServer)
	dash.GET("/servers/:id", h.ShowServerDetail)
	dash.GET("/servers/:id/console", h.ShowServerConsole)
	dash.GET("/servers/:id/console/logs", h.GetServerConsoleLogsJSON)
	dash.POST("/servers/:id/console/command", h.SendConsoleCommand)
	dash.GET("/servers/:id/backups", h.ShowServerBackups)
	dash.GET("/servers/:id/logs", h.ShowServerLogs)
	dash.GET("/servers/:id/settings", h.ShowServerSettings)
	dash.POST("/servers/:id/settings", h.UpdateServerSettings)
	dash.GET("/servers/:id/jobs", h.ShowServerJobs)
	dash.GET("/servers/:id/host", h.ShowServerHost)
	dash.POST("/servers/:id/start", h.StartServer)
	dash.POST("/servers/:id/stop", h.StopServer)
	dash.POST("/servers/:id/delete", h.DeleteServer)

	dash.POST("/servers/:id/backups/now", h.TriggerBackup)
	dash.POST("/servers/:id/backups/:backup_id/restore", h.RestoreBackup)
	dash.POST("/servers/:id/jobs/:job_id/retry", h.RetryJob)
	dash.POST("/servers/:id/migrate", h.TriggerMigration)

	dash.GET("/servers/:id/files/open", h.OpenFileManager)
}
