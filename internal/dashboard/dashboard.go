package dashboard

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/jobs"
)

//go:embed templates/*
var templateFS embed.FS

// Service coordinates dashboard views and delegates exclusively to public domain service interfaces.
type Service struct {
	db           *gorm.DB
	hostingSvc   *hosting.Service
	backupSvc    *backup.Service
	billingSvc   *billing.Service
	migrationSvc *hosting.MigrationService
	jobSvc       *jobs.Service
	jwtSecret    string
	tmpl         map[string]*template.Template
}

// NewService instantiates a decoupled dashboard controller.
//
// Arguments:
//   - db: Database handle for queries.
//   - hostingSvc: Hosting lifecycle service.
//   - backupSvc: Backup and restore service.
//   - billingSvc: Subscription and billing service.
//   - migrationSvc: Host migration coordinator.
//   - jobSvc: Async job queue coordinator.
//   - jwtSecret: Secret key to verify and issue session tokens.
//
// Example usage:
//
//	dash := dashboard.NewService(db, hostingSvc, backupSvc, billingSvc, migrationSvc, jobSvc, "jwt-secret")
func NewService(
	db *gorm.DB,
	hostingSvc *hosting.Service,
	backupSvc *backup.Service,
	billingSvc *billing.Service,
	migrationSvc *hosting.MigrationService,
	jobSvc *jobs.Service,
	jwtSecret string,
) *Service {
	s := &Service{
		db:           db,
		hostingSvc:   hostingSvc,
		backupSvc:    backupSvc,
		billingSvc:   billingSvc,
		migrationSvc: migrationSvc,
		jobSvc:       jobSvc,
		jwtSecret:    jwtSecret,
		tmpl:         make(map[string]*template.Template),
	}
	s.loadTemplates()
	return s
}

// loadTemplates parses HTML templates with custom arithmetic functions.
func (s *Service) loadTemplates() {
	funcMap := template.FuncMap{
		"div": func(a, b int64) int64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
	}

	pages := []string{
		"login.html",
		"register.html",
		"servers_list.html",
		"server_create.html",
		"server_detail.html",
		"server_console.html",
		"server_backups.html",
		"server_logs.html",
		"server_settings.html",
		"server_jobs.html",
		"server_host.html",
		"users.html",
		"billing.html",
		"forbidden.html",
	}

	for _, page := range pages {
		tmpl := template.Must(template.New("layout").Funcs(funcMap).ParseFS(
			templateFS,
			"templates/layout.html",
			"templates/server_subnav.html",
			"templates/"+page,
		))
		s.tmpl[page] = tmpl
	}
}

// renderView renders an HTML template page with HTTP 200 and flash message support.
func (s *Service) renderView(c *gin.Context, page string, data map[string]interface{}) {
	s.renderViewWithStatus(c, http.StatusOK, page, data)
}

func (s *Service) renderViewWithStatus(c *gin.Context, status int, page string, data map[string]interface{}) {
	tmpl, exists := s.tmpl[page]
	if !exists {
		c.String(http.StatusInternalServerError, "template %s not found", page)
		return
	}

	if data == nil {
		data = make(map[string]interface{})
	}

	if userVal, exists := c.Get(authkit.ContextEmailKey); exists {
		data["UserEmail"] = userVal
	}
	role := authkit.GetUserRoleFromContext(c)
	data["UserRole"] = role
	data["IsStaffOrAdmin"] = role.IsStaffOrAdmin()
	data["IsAdmin"] = role.IsAdmin()

	if msg := c.Query("success"); msg != "" {
		data["FlashSuccess"] = msg
	}
	if msg := c.Query("error"); msg != "" {
		data["FlashError"] = msg
	}

	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.ExecuteTemplate(c.Writer, "layout", data)
}

// ShowLogin renders the login page.
func (s *Service) ShowLogin(c *gin.Context) {
	s.renderView(c, "login.html", nil)
}

// ProcessLogin authenticates credentials and establishes browser session cookie.
func (s *Service) ProcessLogin(c *gin.Context) {
	email := c.PostForm("email")
	password := c.PostForm("password")

	user, err := authkit.Authenticate(s.db, email, password)
	if err != nil {
		s.renderView(c, "login.html", map[string]interface{}{
			"FlashError": "Invalid email or password",
		})
		return
	}

	tokens, err := authkit.GenerateTokenPair(user.ID, user.Email, user.Role, s.jwtSecret)
	if err != nil {
		s.renderView(c, "login.html", map[string]interface{}{"FlashError": "Failed generating session"})
		return
	}

	authkit.SetSessionCookie(c, "token", tokens.AccessToken, 3600*24, false)
	c.Redirect(http.StatusFound, "/dashboard")
}

// ShowRegister renders the account creation page.
func (s *Service) ShowRegister(c *gin.Context) {
	s.renderView(c, "register.html", nil)
}

// ProcessRegister validates input, creates user account, and signs in automatically.
func (s *Service) ProcessRegister(c *gin.Context) {
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	password := c.PostForm("password")

	if len(password) < 8 {
		s.renderView(c, "register.html", map[string]interface{}{
			"FlashError": "Password must be at least 8 characters",
		})
		return
	}

	var existing domain.User
	if err := s.db.Where("email = ?", email).First(&existing).Error; err == nil {
		s.renderView(c, "register.html", map[string]interface{}{
			"FlashError": "An account with this email already exists",
		})
		return
	}

	hashed, err := authkit.HashPassword(password)
	if err != nil {
		s.renderView(c, "register.html", map[string]interface{}{"FlashError": "Failed encrypting credentials"})
		return
	}

	newUser := domain.User{
		Email:        email,
		PasswordHash: hashed,
		Role:         domain.UserRoleUser,
	}
	if err := s.db.Create(&newUser).Error; err != nil {
		s.renderView(c, "register.html", map[string]interface{}{"FlashError": "Failed saving account"})
		return
	}

	tokens, _ := authkit.GenerateTokenPair(newUser.ID, newUser.Email, newUser.Role, s.jwtSecret)
	authkit.SetSessionCookie(c, "token", tokens.AccessToken, 3600*24, false)
	c.Redirect(http.StatusFound, "/dashboard?success=Welcome+to+your+dashboard!")
}

// Logout terminates user browser session.
func (s *Service) Logout(c *gin.Context) {
	authkit.ClearSessionCookie(c, "token")
	c.Redirect(http.StatusFound, "/login?success=Logged+out+successfully")
}

// ListServers displays all game servers owned by the authenticated user.
func (s *Service) ListServers(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	servers, err := s.hostingSvc.ListServers(c.Request.Context(), userID)
	if err != nil {
		s.renderView(c, "servers_list.html", map[string]interface{}{"FlashError": err.Error()})
		return
	}

	s.renderView(c, "servers_list.html", map[string]interface{}{
		"Servers": servers,
	})
}

// ShowCreateServer displays plan selection and server configuration form.
func (s *Service) ShowCreateServer(c *gin.Context) {
	var plans []domain.Plan
	_ = s.db.Find(&plans)

	s.renderView(c, "server_create.html", map[string]interface{}{
		"Plans": plans,
	})
}

// ProcessCreateServer queues a new Minecraft game server so the user does not wait for container provisioning.
func (s *Service) ProcessCreateServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	name := strings.TrimSpace(c.PostForm("name"))
	planIDVal, _ := strconv.ParseUint(c.PostForm("plan_id"), 10, 64)

	server, _, err := s.jobSvc.EnqueueCreateServer(
		c.Request.Context(),
		userID,
		name,
		uint(planIDVal),
		domain.ServerTypeVanilla,
		domain.DefaultServerVersion,
	)
	if err != nil {
		var plans []domain.Plan
		_ = s.db.Find(&plans)
		s.renderView(c, "server_create.html", map[string]interface{}{
			"Plans":      plans,
			"FlashError": "Failed queueing server creation: " + err.Error(),
		})
		return
	}

	c.Redirect(http.StatusFound, serverRedirectURL(server.ID, "", "success", "Server creation queued!"))
}

// ShowServerDetail renders the server overview page.
func (s *Service) ShowServerDetail(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, liveStatus, err := s.hostingSvc.GetServer(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	s.renderView(c, "server_detail.html", map[string]interface{}{
		"ActiveTab":  "overview",
		"Server":     server,
		"LiveStatus": liveStatus,
	})
}

// ShowServerConsole renders the live container console logs and RCON command form for a server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and optional query param 'tail'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/console?tail=200
func (s *Service) ShowServerConsole(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := s.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	tail := 200
	if rawTail := c.Query("tail"); rawTail != "" {
		if parsed, convErr := strconv.Atoi(rawTail); convErr == nil && parsed > 0 {
			tail = parsed
		}
	}

	var consoleLogs string
	var logsErr string
	if server.Status == domain.ServerStatusRunning && server.ContainerID != "" {
		logsResp, fetchErr := s.hostingSvc.GetServerConsoleLogs(c.Request.Context(), userID, server.ID, tail)
		if fetchErr != nil {
			logsErr = fetchErr.Error()
		} else if logsResp != nil {
			consoleLogs = logsResp.Logs
		}
	}

	s.renderView(c, "server_console.html", map[string]interface{}{
		"ActiveTab":   "console",
		"Server":      server,
		"ConsoleLogs": consoleLogs,
		"LogsError":   logsErr,
		"Tail":        tail,
	})
}

// GetServerConsoleLogsJSON returns trailing container logs as JSON for live polling in the web console.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and optional query param 'tail'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/console/logs?tail=200
func (s *Service) GetServerConsoleLogsJSON(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid server ID"})
		return
	}

	server, _, err := s.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Server not found"})
		return
	}

	if server.Status != domain.ServerStatusRunning || server.ContainerID == "" {
		c.JSON(http.StatusOK, gin.H{
			"status": server.Status,
			"logs":   "",
		})
		return
	}

	tail := 200
	if rawTail := c.Query("tail"); rawTail != "" {
		if parsed, convErr := strconv.Atoi(rawTail); convErr == nil && parsed > 0 {
			tail = parsed
		}
	}

	logsResp, fetchErr := s.hostingSvc.GetServerConsoleLogs(c.Request.Context(), userID, server.ID, tail)
	if fetchErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"status": server.Status,
			"logs":   "",
			"error":  fetchErr.Error(),
		})
		return
	}

	logs := ""
	if logsResp != nil {
		logs = logsResp.Logs
	}
	c.JSON(http.StatusOK, gin.H{
		"status": server.Status,
		"logs":   logs,
	})
}

// SendConsoleCommand executes a Minecraft console command on the running server.
// Returns JSON when requested via fetch/AJAX, or redirects back to /console for plain form submissions.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form field 'command'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/console/command
func (s *Service) SendConsoleCommand(c *gin.Context) {
	wantsJSON := strings.Contains(c.GetHeader("Accept"), "application/json") || c.GetHeader("X-Requested-With") == "XMLHttpRequest"

	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		if wantsJSON {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid server ID"})
			return
		}
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	command := c.PostForm("command")
	tail := c.PostForm("tail")
	_, err := s.hostingSvc.SendServerCommand(c.Request.Context(), userID, serverIDVal, command)
	if err != nil {
		if wantsJSON {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/console", "error", err.Error()))
		return
	}

	if wantsJSON {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	target := fmt.Sprintf("/dashboard/servers/%d/console?success=%s", serverIDVal, url.QueryEscape("Command sent"))
	if tail != "" {
		target += "&tail=" + url.QueryEscape(tail)
	}
	c.Redirect(http.StatusFound, target)
}

// ShowServerLogs renders PostgreSQL operation logs for a server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/logs
func (s *Service) ShowServerLogs(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := s.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	logs, _ := domain.ListServerLogs(c.Request.Context(), s.db, server.ID, 50)
	s.renderView(c, "server_logs.html", map[string]interface{}{
		"ActiveTab": "logs",
		"Server":    server,
		"Logs":      logs,
	})
}

// ShowServerJobs renders the background job queue for a server exclusively to staff and admin users.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/jobs
func (s *Service) ShowServerJobs(c *gin.Context) {
	if !authkit.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Background job queue is only available to staff and admin users.",
		})
		return
	}

	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := s.hostingSvc.GetServer(c.Request.Context(), 0, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	var jobList []domain.ServerJob
	if s.jobSvc != nil {
		jobList, _ = s.jobSvc.ListServerJobs(c.Request.Context(), server.ID, 30)
	}
	s.renderView(c, "server_jobs.html", map[string]interface{}{
		"ActiveTab": "jobs",
		"Server":    server,
		"Jobs":      jobList,
	})
}

// ShowServerBackups renders the dedicated World Backups page for a server.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/backups
func (s *Service) ShowServerBackups(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := s.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	backups, _ := s.backupSvc.ListBackups(c.Request.Context(), server.ID)
	var backupJobs []domain.ServerJob
	if s.jobSvc != nil {
		allJobs, _ := s.jobSvc.ListServerJobs(c.Request.Context(), server.ID, 20)
		for _, j := range allJobs {
			if j.Type == domain.JobTypeBackup || j.Type == domain.JobTypeRestore {
				backupJobs = append(backupJobs, j)
			}
		}
	}

	s.renderView(c, "server_backups.html", map[string]interface{}{
		"ActiveTab": "backups",
		"Server":    server,
		"Backups":   backups,
		"Jobs":      backupJobs,
	})
}

// ShowServerSettings renders the server settings page (server jar/version selector and Danger Zone).
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/settings
func (s *Service) ShowServerSettings(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, _, err := s.hostingSvc.GetServerForOwner(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	s.renderView(c, "server_settings.html", map[string]interface{}{
		"ActiveTab":   "settings",
		"Server":      server,
		"ServerTypes": domain.AllServerTypes(),
	})
}

// UpdateServerSettings persists user-chosen server jar type and version from the settings page.
//
// Arguments:
//   - c: Active Gin request context with form fields 'server_type' and 'server_version'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/settings
func (s *Service) UpdateServerSettings(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	serverType := c.PostForm("server_type")
	serverVersion := c.PostForm("server_version")

	if _, err := s.hostingSvc.UpdateServerSettings(c.Request.Context(), userID, serverIDVal, serverType, serverVersion); err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/settings", "error", err.Error()))
		return
	}

	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/settings", "success", "Settings saved! Start or restart the server to apply."))
}

// ShowServerHost renders Host Worker Status and Host Migration panels exclusively to staff and admin users.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/host
func (s *Service) ShowServerHost(c *gin.Context) {
	if !authkit.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Host worker status and migration are only available to staff and admin users.",
		})
		return
	}

	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	server, liveStatus, err := s.hostingSvc.GetServer(c.Request.Context(), 0, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	hosts, _ := s.hostingSvc.ListHosts(c.Request.Context())
	s.renderView(c, "server_host.html", map[string]interface{}{
		"ActiveTab":      "host",
		"Server":         server,
		"LiveStatus":     liveStatus,
		"AvailableHosts": hosts,
	})
}

// StartServer resumes a stopped server.
func (s *Service) StartServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := parseIDParam(c, "id")

	_, err := s.hostingSvc.StartServer(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "", "success", "Server started"))
}

// StopServer halts a running server container.
func (s *Service) StopServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := parseIDParam(c, "id")

	_, err := s.hostingSvc.StopServer(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "", "success", "Server stopped"))
}

// DeleteServer enqueues asynchronous teardown and deletion of a server instance.
func (s *Service) DeleteServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	if _, err := s.jobSvc.EnqueueDeleteServer(c.Request.Context(), userID, serverIDVal); err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/settings", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/dashboard?success=Server+deletion+queued")
}

// TriggerBackup queues an on-demand snapshot backup job for the server.
func (s *Service) TriggerBackup(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	if _, _, err := s.jobSvc.EnqueueBackup(c.Request.Context(), userID, serverIDVal); err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/backups", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/backups", "success", "Backup queued"))
}

// RestoreBackup queues a job reverting server storage to the chosen snapshot.
func (s *Service) RestoreBackup(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}
	backupIDVal, ok := parseIDParam(c, "backup_id")
	if !ok {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/backups", "error", "Invalid backup ID"))
		return
	}

	if _, err := s.jobSvc.EnqueueRestore(c.Request.Context(), userID, serverIDVal, backupIDVal); err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/backups", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/backups", "success", "Restore queued"))
}

// RetryJob re-queues a failed background job from the dashboard UI.
//
// Arguments:
//   - c: Active Gin request context with URL params 'id' and 'job_id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/jobs/:job_id/retry
func (s *Service) RetryJob(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	role := authkit.GetUserRoleFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}
	jobIDVal, ok := parseIDParam(c, "job_id")
	if !ok {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "", "error", "Invalid job ID"))
		return
	}

	subpath := "/jobs"
	if strings.HasPrefix(c.PostForm("return_to"), fmt.Sprintf("/dashboard/servers/%d/backups", serverIDVal)) {
		subpath = "/backups"
	}

	if _, err := s.jobSvc.RetryJob(c.Request.Context(), userID, role.IsStaffOrAdmin(), jobIDVal); err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, subpath, "error", err.Error()))
		return
	}

	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, subpath, "success", "Job re-queued"))
}

// TriggerMigration moves the server to a different host. Restricted to staff and admin roles.
func (s *Service) TriggerMigration(c *gin.Context) {
	if !authkit.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Host migration is restricted to staff and admin accounts.",
		})
		return
	}

	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}
	destHostID, _ := strconv.ParseUint(c.PostForm("destination_host_id"), 10, 64)

	err := s.migrationSvc.MigrateServer(c.Request.Context(), 0, serverIDVal, uint(destHostID))
	if err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/host", "error", err.Error()))
		return
	}
	c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/host", "success", "Server migrated successfully"))
}

func parseIDParam(c *gin.Context, name string) (uint, bool) {
	val, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || val == 0 {
		return 0, false
	}
	return uint(val), true
}

func serverRedirectURL(serverID uint, subpath, flashKey, message string) string {
	base := fmt.Sprintf("/dashboard/servers/%d%s", serverID, subpath)
	if flashKey == "" || message == "" {
		return base
	}
	return fmt.Sprintf("%s?%s=%s", base, flashKey, url.QueryEscape(message))
}

// OpenFileManager provisions fileserver access for the server and redirects to the node's file manager.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/files/open
func (s *Service) OpenFileManager(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || serverIDVal == 0 {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	targetURL, err := s.hostingSvc.OpenFileManagerURL(c.Request.Context(), userID, uint(serverIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, url.QueryEscape(err.Error())))
		return
	}

	c.Redirect(http.StatusFound, targetURL)
}

// ShowBilling displays customer subscriptions and payment method status.
func (s *Service) ShowBilling(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)

	var user domain.User
	_ = s.db.First(&user, userID)

	subs, _ := s.billingSvc.ListUserSubscriptions(c.Request.Context(), userID)

	s.renderView(c, "billing.html", map[string]interface{}{
		"User":          user,
		"Subscriptions": subs,
	})
}

// SavePaymentMethod attaches a new payment method via billing service.
func (s *Service) SavePaymentMethod(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	pmID := strings.TrimSpace(c.PostForm("payment_method_id"))

	if err := s.billingSvc.SavePaymentMethod(c.Request.Context(), userID, pmID); err != nil {
		c.Redirect(http.StatusFound, "/dashboard/billing?error="+err.Error())
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/billing?success=Payment+method+saved+successfully")
}

// ListUsers renders the admin-only user role management page.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard/users
func (s *Service) ListUsers(c *gin.Context) {
	if !authkit.GetUserRoleFromContext(c).IsAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "User role management is restricted to administrator accounts.",
		})
		return
	}

	var users []domain.User
	_ = s.db.WithContext(c.Request.Context()).Order("id asc").Find(&users).Error

	s.renderView(c, "users.html", map[string]interface{}{
		"Users": users,
		"Roles": []domain.UserRole{domain.UserRoleUser, domain.UserRoleStaff, domain.UserRoleAdmin},
	})
}

// UpdateUserRole updates a user's platform role (user, staff, or admin).
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form field 'role'.
//
// Example usage:
//
//	POST /dashboard/users/:id/role
func (s *Service) UpdateUserRole(c *gin.Context) {
	if !authkit.GetUserRoleFromContext(c).IsAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "User role management is restricted to administrator accounts.",
		})
		return
	}

	targetUserID, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/users?error=Invalid+user+ID")
		return
	}

	newRole := domain.UserRole(strings.TrimSpace(c.PostForm("role")))
	if !newRole.IsValid() {
		c.Redirect(http.StatusFound, "/dashboard/users?error=Invalid+role")
		return
	}

	res := s.db.WithContext(c.Request.Context()).Model(&domain.User{}).Where("id = ?", targetUserID).Update("role", newRole)
	if res.Error != nil {
		c.Redirect(http.StatusFound, "/dashboard/users?error="+url.QueryEscape(res.Error.Error()))
		return
	}
	if res.RowsAffected == 0 {
		c.Redirect(http.StatusFound, "/dashboard/users?error=User+not+found")
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/users?success=User+role+updated")
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
	// Public auth pages
	r.GET("/login", s.ShowLogin)
	r.POST("/login", s.ProcessLogin)
	r.GET("/register", s.ShowRegister)
	r.POST("/register", s.ProcessRegister)
	r.GET("/logout", s.Logout)

	// Protected dashboard pages
	dash := r.Group("/dashboard")
	dash.Use(authkit.CookieAuthMiddleware(s.jwtSecret, "token", "/login"))
	{
		dash.GET("", s.ListServers)
		dash.GET("/servers/new", s.ShowCreateServer)
		dash.POST("/servers/new", s.ProcessCreateServer)
		dash.GET("/servers/:id", s.ShowServerDetail)
		dash.GET("/servers/:id/console", s.ShowServerConsole)
		dash.GET("/servers/:id/console/logs", s.GetServerConsoleLogsJSON)
		dash.POST("/servers/:id/console/command", s.SendConsoleCommand)
		dash.GET("/servers/:id/backups", s.ShowServerBackups)
		dash.GET("/servers/:id/logs", s.ShowServerLogs)
		dash.GET("/servers/:id/settings", s.ShowServerSettings)
		dash.POST("/servers/:id/settings", s.UpdateServerSettings)
		dash.GET("/servers/:id/jobs", s.ShowServerJobs)
		dash.GET("/servers/:id/host", s.ShowServerHost)
		dash.POST("/servers/:id/start", s.StartServer)
		dash.POST("/servers/:id/stop", s.StopServer)
		dash.POST("/servers/:id/delete", s.DeleteServer)

		dash.POST("/servers/:id/backups/now", s.TriggerBackup)
		dash.POST("/servers/:id/backups/:backup_id/restore", s.RestoreBackup)
		dash.POST("/servers/:id/jobs/:job_id/retry", s.RetryJob)
		dash.POST("/servers/:id/migrate", s.TriggerMigration)

		dash.GET("/servers/:id/files/open", s.OpenFileManager)

		dash.GET("/billing", s.ShowBilling)
		dash.POST("/billing/payment-methods", s.SavePaymentMethod)

		dash.GET("/users", s.ListUsers)
		dash.POST("/users/:id/role", s.UpdateUserRole)
	}

	// Redirect root to /dashboard
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})
}
