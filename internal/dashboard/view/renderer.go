package view

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"

	"github.com/dreamoutbox/go-authkit"
	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"
)

//go:embed templates/*
var templateFS embed.FS

// Renderer parses and executes embedded dashboard HTML templates with common layout context.
type Renderer struct {
	tmpl map[string]*template.Template
}

// NewRenderer compiles all embedded dashboard templates with shared helper functions.
//
// Example usage:
//
//	r := view.NewRenderer()
func NewRenderer() *Renderer {
	r := &Renderer{
		tmpl: make(map[string]*template.Template),
	}
	r.loadTemplates()
	return r
}

func (r *Renderer) loadTemplates() {
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
		"pageURL": AdminPageURL,
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
		r.tmpl[page] = tmpl
	}
}

// RenderView renders an HTML template page with HTTP 200 and flash message support.
//
// Arguments:
//   - c: Active Gin request context.
//   - page: Template filename (e.g. "servers_list.html").
//   - data: Optional template context map.
//
// Example usage:
//
//	r.RenderView(c, "login.html", nil)
func (r *Renderer) RenderView(c *gin.Context, page string, data map[string]interface{}) {
	r.RenderViewWithStatus(c, http.StatusOK, page, data)
}

// RenderViewWithStatus renders an HTML template page with an explicit HTTP status code.
//
// Arguments:
//   - c: Active Gin request context.
//   - status: HTTP status code to emit.
//   - page: Template filename (e.g. "forbidden.html").
//   - data: Optional template context map.
//
// Example usage:
//
//	r.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{"ErrorMessage": "Denied"})
func (r *Renderer) RenderViewWithStatus(c *gin.Context, status int, page string, data map[string]interface{}) {
	tmpl, exists := r.tmpl[page]
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

// RenderAdminView renders a full-width admin template page with HTTP 200.
//
// Arguments:
//   - c: Active Gin request context.
//   - page: Admin template filename (e.g. "admin_servers.html").
//   - data: Optional template context map.
//
// Example usage:
//
//	r.RenderAdminView(c, "admin_servers.html", data)
func (r *Renderer) RenderAdminView(c *gin.Context, page string, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}
	data["FullWidth"] = true
	r.RenderView(c, page, data)
}
