package dashboard

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dreamoutbox/authkit"
	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/dreamoutbox/authkit/session"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/jobs"
)

const (
	defaultServerRetentionDays = 7
	serverExpiringSoonWindow   = 7 * 24 * time.Hour
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
	authsessions *session.Manager
	tmpl         map[string]*template.Template
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
	s := &Service{
		db:           db,
		hostingSvc:   hostingSvc,
		backupSvc:    backupSvc,
		billingSvc:   billingSvc,
		migrationSvc: migrationSvc,
		jobSvc:       jobSvc,
		authsessions: authsessions,
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
		"formatUSD": func(cents int64) string {
			return fmt.Sprintf("$%.2f", float64(cents)/100.0)
		},
		"pageURL": adminPageURL,
	}

	pages := []string{
		"login.html",
		"register.html",
		"servers_list.html",
		"server_create.html",
		"server_buy.html",
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
		"admin_servers.html",
		"admin_orders.html",
		"admin_payments.html",
		"admin_jobs.html",
		"admin_logs.html",
		"admin_plans.html",
	}

	for _, page := range pages {
		tmpl := template.Must(template.New("layout").Funcs(funcMap).ParseFS(
			templateFS,
			"templates/layout.html",
			"templates/server_subnav.html",
			"templates/admin_controls.html",
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
	role := authmw.GetUserRoleFromContext(c)
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

	if err := s.authsessions.SetSession(c, user.ID); err != nil {
		s.renderView(c, "login.html", map[string]interface{}{"FlashError": "Failed generating session"})
		return
	}

	c.Redirect(http.StatusFound, "/dashboard")
}

// ShowRegister renders the account creation page.
func (s *Service) ShowRegister(c *gin.Context) {
	s.renderView(c, "register.html", nil)
}

// ProcessRegister validates input, creates a user account, and redirects to the login page.
func (s *Service) ProcessRegister(c *gin.Context) {
	_, err := authkit.RegisterUser(s.db, c.PostForm("email"), c.PostForm("password"))
	if err != nil {
		msg := "Failed saving account"
		if errors.Is(err, authkit.ErrInvalidEmail) ||
			errors.Is(err, authkit.ErrPasswordTooShort) ||
			errors.Is(err, authkit.ErrEmailAlreadyExists) {
			msg = err.Error()
		}
		s.renderView(c, "register.html", map[string]interface{}{"FlashError": msg})
		return
	}

	c.Redirect(http.StatusFound, "/login?success=Account+created.+Please+sign+in.")
}

// Logout terminates user browser session.
func (s *Service) Logout(c *gin.Context) {
	_ = s.authsessions.Logout(c)
	c.Redirect(http.StatusFound, "/login?success=Logged+out+successfully")
}

// ExpiringServerAlert represents a dashboard warning entry for a server that is expiring soon or recently expired.
type ExpiringServerAlert struct {
	ServerID   uint
	ServerName string
	ExpiresAt  time.Time
	IsExpired  bool
}

// ListServers displays all game servers owned by the authenticated user.
func (s *Service) ListServers(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	servers, err := s.hostingSvc.ListServers(c.Request.Context(), userID)
	if err != nil {
		s.renderView(c, "servers_list.html", map[string]interface{}{"FlashError": err.Error()})
		return
	}

	now := time.Now()
	var expiringAlerts []ExpiringServerAlert
	for _, srv := range servers {
		if srv.ExpiresAt == nil {
			continue
		}
		isExpired := srv.IsExpired(now)
		if srv.IsExpiringSoon(now, serverExpiringSoonWindow) ||
			(isExpired && srv.IsWithinRetention(now, defaultServerRetentionDays)) {
			expiringAlerts = append(expiringAlerts, ExpiringServerAlert{
				ServerID:   srv.ID,
				ServerName: srv.Name,
				ExpiresAt:  *srv.ExpiresAt,
				IsExpired:  isExpired,
			})
		}
	}

	s.renderView(c, "servers_list.html", map[string]interface{}{
		"Servers":         servers,
		"ExpiringServers": expiringAlerts,
	})
}

// ShowCreateServer displays plan selection and direct server creation form (staff/admin only).
func (s *Service) ShowCreateServer(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Direct server creation is restricted to staff and admin accounts. Please purchase a server via Checkout.",
		})
		return
	}

	var plans []domain.Plan
	_ = s.db.Find(&plans)

	s.renderView(c, "server_create.html", map[string]interface{}{
		"Plans": plans,
	})
}

// ProcessCreateServer queues a new Minecraft game server directly (staff/admin only).
func (s *Service) ProcessCreateServer(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "Direct server creation is restricted to staff and admin accounts.",
		})
		return
	}

	userID, _ := authmw.GetUserIDFromContext(c)
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

// ShowBuyServer displays the Stripe Checkout server purchase page for users.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard/servers/buy
func (s *Service) ShowBuyServer(c *gin.Context) {
	var plans []domain.Plan
	_ = s.db.Find(&plans)

	s.renderView(c, "server_buy.html", map[string]interface{}{
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
func (s *Service) ProcessBuyServer(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	name := strings.TrimSpace(c.PostForm("name"))
	planIDVal, _ := strconv.ParseUint(c.PostForm("plan_id"), 10, 64)
	serverType := domain.ServerType(strings.ToUpper(strings.TrimSpace(c.PostForm("server_type"))))
	serverVersion := strings.TrimSpace(c.PostForm("server_version"))

	cs, _, err := s.billingSvc.CreateServerCheckout(
		c.Request.Context(),
		userID,
		uint(planIDVal),
		name,
		serverType,
		serverVersion,
	)
	if err != nil {
		var plans []domain.Plan
		_ = s.db.Find(&plans)
		s.renderView(c, "server_buy.html", map[string]interface{}{
			"Plans":       plans,
			"ServerTypes": domain.AllServerTypes(),
			"FlashError":  "Failed initiating checkout: " + err.Error(),
		})
		return
	}

	c.Redirect(http.StatusSeeOther, cs.CheckoutURL)
}

// serverExpiryFlags computes expiration and extension eligibility flags for a server.
//
// Arguments:
//   - server: Target server model.
//   - now: Reference timestamp.
//
// Example usage:
//
//	isExpired, canExtend := serverExpiryFlags(server, time.Now())
func serverExpiryFlags(server *domain.Server, now time.Time) (bool, bool) {
	isExpired := server.IsExpired(now)
	canExtend := server.ExpiresAt != nil && server.IsWithinRetention(now, defaultServerRetentionDays)
	return isExpired, canExtend
}

// ExtendServerDuration creates a Stripe Checkout session to extend a server by 30 days.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/servers/:id/extend
func (s *Service) ExtendServerDuration(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	serverIDVal, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	cs, _, err := s.billingSvc.CreateExtendCheckout(c.Request.Context(), userID, serverIDVal)
	if err != nil {
		c.Redirect(http.StatusFound, serverRedirectURL(serverIDVal, "/settings", "error", err.Error())+"#extend-server")
		return
	}

	c.Redirect(http.StatusSeeOther, cs.CheckoutURL)
}

// ShowServerDetail renders the server overview page.
func (s *Service) ShowServerDetail(c *gin.Context) {
	userID := s.effectiveServerUserID(c)
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

	now := time.Now()
	isExpired, canExtend := serverExpiryFlags(server, now)
	s.renderView(c, "server_detail.html", map[string]interface{}{
		"ActiveTab":      "overview",
		"Server":         server,
		"LiveStatus":     liveStatus,
		"IsExpired":      isExpired,
		"IsExpiringSoon": server.IsExpiringSoon(now, serverExpiringSoonWindow),
		"CanExtend":      canExtend,
		"RetentionDays":  defaultServerRetentionDays,
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
	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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

	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
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
	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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

	isExpired, canExtend := serverExpiryFlags(server, time.Now())
	s.renderView(c, "server_settings.html", map[string]interface{}{
		"ActiveTab":     "settings",
		"Server":        server,
		"ServerTypes":   domain.AllServerTypes(),
		"IsExpired":     isExpired,
		"CanExtend":     canExtend,
		"RetentionDays": defaultServerRetentionDays,
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
	userID := s.effectiveServerUserID(c)
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
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
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
	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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
	userID := s.effectiveServerUserID(c)
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
	userID, _ := authmw.GetUserIDFromContext(c)
	role := authmw.GetUserRoleFromContext(c)
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

	returnTo := c.PostForm("return_to")
	redirectFor := func(kind string, msg string) string {
		if returnTo == "/dashboard/admin/jobs" && role.IsAdmin() {
			return fmt.Sprintf("/dashboard/admin/jobs?%s=%s", kind, url.QueryEscape(msg))
		}
		subpath := "/jobs"
		if strings.HasPrefix(returnTo, fmt.Sprintf("/dashboard/servers/%d/backups", serverIDVal)) {
			subpath = "/backups"
		}
		return serverRedirectURL(serverIDVal, subpath, kind, msg)
	}

	if _, err := s.jobSvc.RetryJob(c.Request.Context(), userID, role.IsStaffOrAdmin(), jobIDVal); err != nil {
		c.Redirect(http.StatusFound, redirectFor("error", err.Error()))
		return
	}

	c.Redirect(http.StatusFound, redirectFor("success", "Job re-queued"))
}

// TriggerMigration moves the server to a different host. Restricted to staff and admin roles.
func (s *Service) TriggerMigration(c *gin.Context) {
	if !authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
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
	userID, _ := authmw.GetUserIDFromContext(c)
	allowNonOwner := authmw.GetUserRoleFromContext(c).IsStaffOrAdmin()
	serverIDVal, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || serverIDVal == 0 {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	targetURL, err := s.hostingSvc.OpenFileManagerURL(c.Request.Context(), userID, uint(serverIDVal), allowNonOwner)
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, url.QueryEscape(err.Error())))
		return
	}

	c.Redirect(http.StatusFound, targetURL)
}

// ShowBilling displays customer checkout order history and payment records.
func (s *Service) ShowBilling(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)

	orders, _ := s.billingSvc.ListUserOrders(c.Request.Context(), userID)
	payments, _ := s.billingSvc.ListUserPayments(c.Request.Context(), userID)

	s.renderView(c, "billing.html", map[string]interface{}{
		"Orders":   orders,
		"Payments": payments,
	})
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
	if !authmw.GetUserRoleFromContext(c).IsAdmin() {
		s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
			"ErrorMessage": "User role management is restricted to administrator accounts.",
		})
		return
	}

	var users []domain.User
	_ = s.db.WithContext(c.Request.Context()).Order("id asc").Find(&users).Error

	s.renderAdminView(c, "users.html", map[string]interface{}{
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
	if !authmw.GetUserRoleFromContext(c).IsAdmin() {
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
	dash.Use(s.authsessions.Middleware("/login"))
	{
		dash.GET("", s.ListServers)
		dash.GET("/servers/new", s.ShowCreateServer)
		dash.POST("/servers/new", s.ProcessCreateServer)
		dash.GET("/servers/buy", s.ShowBuyServer)
		dash.POST("/servers/buy", s.ProcessBuyServer)
		dash.GET("/servers/:id", s.ShowServerDetail)
		dash.POST("/servers/:id/extend", s.ExtendServerDuration)
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

		dash.GET("/users", s.ListUsers)
		dash.POST("/users/:id/role", s.UpdateUserRole)

		admin := dash.Group("/admin")
		{
			admin.GET("/servers", s.AdminListServers)
			admin.GET("/orders", s.AdminListOrders)
			admin.GET("/payments", s.AdminListPayments)
			admin.GET("/jobs", s.AdminListJobs)
			admin.GET("/logs", s.AdminListLogs)
			admin.GET("/plans", s.AdminListPlans)
			admin.POST("/plans", s.AdminCreatePlan)
			admin.POST("/plans/:id", s.AdminUpdatePlan)
			admin.POST("/plans/:id/delete", s.AdminDeletePlan)
		}
	}

	// Redirect root to /dashboard
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})
}
