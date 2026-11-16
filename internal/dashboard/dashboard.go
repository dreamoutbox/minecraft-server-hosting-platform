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
//   - jwtSecret: Secret key to verify and issue session tokens.
//
// Example usage:
//   dash := dashboard.NewService(db, hostingSvc, backupSvc, billingSvc, migrationSvc, "jwt-secret")
func NewService(
	db *gorm.DB,
	hostingSvc *hosting.Service,
	backupSvc *backup.Service,
	billingSvc *billing.Service,
	migrationSvc *hosting.MigrationService,
	jwtSecret string,
) *Service {
	s := &Service{
		db:           db,
		hostingSvc:   hostingSvc,
		backupSvc:    backupSvc,
		billingSvc:   billingSvc,
		migrationSvc: migrationSvc,
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
		"billing.html",
	}

	for _, page := range pages {
		tmpl := template.Must(template.New("layout").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/"+page))
		s.tmpl[page] = tmpl
	}
}

// renderView renders an HTML template page with flash message support.
func (s *Service) renderView(c *gin.Context, page string, data map[string]interface{}) {
	tmpl, exists := s.tmpl[page]
	if !exists {
		c.String(http.StatusInternalServerError, "template %s not found", page)
		return
	}

	if data == nil {
		data = make(map[string]interface{})
	}

	// Attach user email and query flash messages if present
	if userVal, exists := c.Get(authkit.ContextEmailKey); exists {
		data["UserEmail"] = userVal
	}
	if msg := c.Query("success"); msg != "" {
		data["FlashSuccess"] = msg
	}
	if msg := c.Query("error"); msg != "" {
		data["FlashError"] = msg
	}

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

// ProcessCreateServer provisions a new Minecraft game server via hosting service.
func (s *Service) ProcessCreateServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	name := strings.TrimSpace(c.PostForm("name"))
	planIDVal, _ := strconv.ParseUint(c.PostForm("plan_id"), 10, 64)

	server, err := s.hostingSvc.CreateServer(c.Request.Context(), userID, name, uint(planIDVal))
	if err != nil {
		var plans []domain.Plan
		_ = s.db.Find(&plans)
		s.renderView(c, "server_create.html", map[string]interface{}{
			"Plans":      plans,
			"FlashError": "Failed deploying server: " + err.Error(),
		})
		return
	}

	c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?success=Server+deployed+successfully!", server.ID))
}

// ShowServerDetail renders server diagnostics, backups, and migration tools.
func (s *Service) ShowServerDetail(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	server, liveStatus, err := s.hostingSvc.GetServer(c.Request.Context(), userID, uint(serverIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard?error=Server+not+found")
		return
	}

	backups, _ := s.backupSvc.ListBackups(c.Request.Context(), server.ID)
	hosts, _ := s.hostingSvc.ListHosts(c.Request.Context())

	s.renderView(c, "server_detail.html", map[string]interface{}{
		"Server":         server,
		"LiveStatus":     liveStatus,
		"Backups":        backups,
		"AvailableHosts": hosts,
	})
}

// StartServer resumes a stopped server.
func (s *Service) StartServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	_, err := s.hostingSvc.StartServer(c.Request.Context(), userID, uint(serverIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, err.Error()))
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?success=Server+started", serverIDVal))
}

// StopServer halts a running server container.
func (s *Service) StopServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	_, err := s.hostingSvc.StopServer(c.Request.Context(), userID, uint(serverIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, err.Error()))
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?success=Server+stopped", serverIDVal))
}

// DeleteServer destroys a server instance.
func (s *Service) DeleteServer(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	err := s.hostingSvc.DeleteServer(c.Request.Context(), userID, uint(serverIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/dashboard?success=Server+deleted+successfully")
}

// TriggerBackup creates an on-demand snapshot for the server.
func (s *Service) TriggerBackup(c *gin.Context) {
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	_, err := s.backupSvc.BackupServer(c.Request.Context(), uint(serverIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, err.Error()))
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?success=Backup+created+successfully", serverIDVal))
}

// RestoreBackup reverts server storage to the chosen snapshot.
func (s *Service) RestoreBackup(c *gin.Context) {
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	backupIDVal, _ := strconv.ParseUint(c.Param("backup_id"), 10, 64)

	err := s.backupSvc.RestoreServer(c.Request.Context(), uint(serverIDVal), uint(backupIDVal))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, err.Error()))
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?success=Server+restored+from+backup", serverIDVal))
}

// TriggerMigration moves the server to a different host.
func (s *Service) TriggerMigration(c *gin.Context) {
	userID, _ := authkit.GetUserIDFromContext(c)
	serverIDVal, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	destHostID, _ := strconv.ParseUint(c.PostForm("destination_host_id"), 10, 64)

	err := s.migrationSvc.MigrateServer(c.Request.Context(), userID, uint(serverIDVal), uint(destHostID))
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, err.Error()))
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?success=Server+migrated+successfully", serverIDVal))
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

// RegisterRoutes registers all dashboard web pages and form actions onto the router.
//
// Arguments:
//   - r: Gin engine.
//
// Example usage:
//   dashService.RegisterRoutes(router)
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
		dash.POST("/servers/:id/start", s.StartServer)
		dash.POST("/servers/:id/stop", s.StopServer)
		dash.POST("/servers/:id/delete", s.DeleteServer)

		dash.POST("/servers/:id/backups/now", s.TriggerBackup)
		dash.POST("/servers/:id/backups/:backup_id/restore", s.RestoreBackup)
		dash.POST("/servers/:id/migrate", s.TriggerMigration)

		dash.GET("/servers/:id/files/open", s.OpenFileManager)

		dash.GET("/billing", s.ShowBilling)
		dash.POST("/billing/payment-methods", s.SavePaymentMethod)
	}

	// Redirect root to /dashboard
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/dashboard")
	})
}
