package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

const (
	defaultAdminPageSize = 20
	maxAdminPageSize     = 100
	adminPageWindow      = 5
)

// Pagination holds search query state and page navigation metadata for admin list views.
type Pagination struct {
	Query      string
	Page       int
	PageSize   int
	TotalCount int64
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
	Pages      []int
	BaseURL    string
}

func (s *Service) requireAdmin(c *gin.Context, message string) bool {
	if authmw.GetUserRoleFromContext(c).IsAdmin() {
		return true
	}
	s.renderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
		"ErrorMessage": message,
	})
	return false
}

func (s *Service) renderAdminView(c *gin.Context, page string, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}
	data["FullWidth"] = true
	s.renderView(c, page, data)
}

// effectiveServerUserID returns 0 for staff and admin users so they can inspect and manage
// any server, and returns the authenticated user ID for regular users.
func (s *Service) effectiveServerUserID(c *gin.Context) uint {
	if authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		return 0
	}
	userID, _ := authmw.GetUserIDFromContext(c)
	return userID
}

func parseSearchAndPage(c *gin.Context) (string, int, int) {
	query := strings.TrimSpace(c.Query("q"))

	page := 1
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			page = parsed
		}
	}

	pageSize := defaultAdminPageSize
	if rawSize := strings.TrimSpace(c.Query("page_size")); rawSize != "" {
		if parsed, err := strconv.Atoi(rawSize); err == nil && parsed > 0 {
			if parsed > maxAdminPageSize {
				parsed = maxAdminPageSize
			}
			pageSize = parsed
		}
	}

	return query, page, pageSize
}

func applySearchFilter(db *gorm.DB, query string, conditions ...string) *gorm.DB {
	if query == "" || len(conditions) == 0 {
		return db
	}
	pattern := "%" + strings.ToLower(query) + "%"
	args := make([]interface{}, len(conditions))
	for i := range conditions {
		args[i] = pattern
	}
	return db.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

func paginateQuery(query *gorm.DB, search string, page, pageSize int, baseURL, orderBy string, dest interface{}) (Pagination, error) {
	var totalCount int64
	if err := query.Count(&totalCount).Error; err != nil {
		return Pagination{}, err
	}

	totalPages := int((totalCount + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	if page < 1 {
		page = 1
	}

	offset := (page - 1) * pageSize
	if err := query.Order(orderBy).Offset(offset).Limit(pageSize).Find(dest).Error; err != nil {
		return Pagination{}, err
	}

	startPage := page - adminPageWindow/2
	if startPage < 1 {
		startPage = 1
	}
	endPage := startPage + adminPageWindow - 1
	if endPage > totalPages {
		endPage = totalPages
		startPage = endPage - adminPageWindow + 1
		if startPage < 1 {
			startPage = 1
		}
	}
	pages := make([]int, 0, endPage-startPage+1)
	for p := startPage; p <= endPage; p++ {
		pages = append(pages, p)
	}

	return Pagination{
		Query:      search,
		Page:       page,
		PageSize:   pageSize,
		TotalCount: totalCount,
		TotalPages: totalPages,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		PrevPage:   page - 1,
		NextPage:   page + 1,
		Pages:      pages,
		BaseURL:    baseURL,
	}, nil
}

func adminPageURL(baseURL, query string, page, pageSize int) string {
	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	if page > 1 {
		params.Set("page", strconv.Itoa(page))
	}
	if pageSize > 0 && pageSize != defaultAdminPageSize {
		params.Set("page_size", strconv.Itoa(pageSize))
	}
	encoded := params.Encode()
	if encoded == "" {
		return baseURL
	}
	return baseURL + "?" + encoded
}

func (s *Service) loadUserEmailMap(ctx context.Context, userIDs []uint) map[uint]string {
	result := make(map[uint]string)
	unique := uniquePositiveIDs(userIDs)
	if len(unique) == 0 {
		return result
	}
	var users []domain.User
	_ = s.db.WithContext(ctx).Select("id, email").Where("id IN ?", unique).Find(&users).Error
	for _, u := range users {
		result[u.ID] = u.Email
	}
	return result
}

func (s *Service) loadServerNameMap(ctx context.Context, serverIDs []uint) map[uint]string {
	result := make(map[uint]string)
	unique := uniquePositiveIDs(serverIDs)
	if len(unique) == 0 {
		return result
	}
	var servers []domain.Server
	_ = s.db.WithContext(ctx).Select("id, name").Where("id IN ?", unique).Find(&servers).Error
	for _, srv := range servers {
		result[srv.ID] = srv.Name
	}
	return result
}

func uniquePositiveIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// AdminListServers renders all provisioned Minecraft servers across all users with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/servers?q=survival&page=1
func (s *Service) AdminListServers(c *gin.Context) {
	if !s.requireAdmin(c, "Admin server management is restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := parseSearchAndPage(c)
	query := s.db.WithContext(c.Request.Context()).
		Model(&domain.Server{}).
		Preload("Plan").
		Preload("Host")
	query = applySearchFilter(
		query,
		search,
		"LOWER(name) LIKE ?",
		"LOWER(status) LIKE ?",
		"LOWER(server_type) LIKE ?",
		"LOWER(server_version) LIKE ?",
		"LOWER(COALESCE(container_id, '')) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"CAST(port AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"host_id IN (SELECT id FROM hosts WHERE LOWER(hostname) LIKE ?)",
		"plan_id IN (SELECT id FROM plans WHERE LOWER(name) LIKE ?)",
	)

	var servers []domain.Server
	pagination, err := paginateQuery(query, search, page, pageSize, "/dashboard/admin/servers", "id desc", &servers)
	if err != nil {
		s.renderAdminView(c, "admin_servers.html", map[string]interface{}{
			"FlashError": "Failed loading servers: " + err.Error(),
			"Pagination": Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/servers"},
		})
		return
	}

	userIDs := make([]uint, 0, len(servers))
	for _, srv := range servers {
		userIDs = append(userIDs, srv.UserID)
	}

	s.renderAdminView(c, "admin_servers.html", map[string]interface{}{
		"Servers":    servers,
		"UserEmails": s.loadUserEmailMap(c.Request.Context(), userIDs),
		"Pagination": pagination,
	})
}

// AdminListOrders renders all Stripe checkout orders across all users with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/orders?q=fulfilled&page=1
func (s *Service) AdminListOrders(c *gin.Context) {
	if !s.requireAdmin(c, "Admin order history is restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := parseSearchAndPage(c)
	query := s.db.WithContext(c.Request.Context()).
		Model(&domain.Order{}).
		Preload("Plan")
	query = applySearchFilter(
		query,
		search,
		"LOWER(server_name) LIKE ?",
		"LOWER(stripe_session_id) LIKE ?",
		"LOWER(order_type) LIKE ?",
		"LOWER(status) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"plan_id IN (SELECT id FROM plans WHERE LOWER(name) LIKE ?)",
	)

	var orders []domain.Order
	pagination, err := paginateQuery(query, search, page, pageSize, "/dashboard/admin/orders", "id desc", &orders)
	if err != nil {
		s.renderAdminView(c, "admin_orders.html", map[string]interface{}{
			"FlashError": "Failed loading orders: " + err.Error(),
			"Pagination": Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/orders"},
		})
		return
	}

	userIDs := make([]uint, 0, len(orders))
	for _, o := range orders {
		userIDs = append(userIDs, o.UserID)
	}

	s.renderAdminView(c, "admin_orders.html", map[string]interface{}{
		"Orders":     orders,
		"UserEmails": s.loadUserEmailMap(c.Request.Context(), userIDs),
		"Pagination": pagination,
	})
}

// AdminListPayments renders all payment_records across all users with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/payments?q=succeeded&page=1
func (s *Service) AdminListPayments(c *gin.Context) {
	if !s.requireAdmin(c, "Admin payment records are restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := parseSearchAndPage(c)
	query := s.db.WithContext(c.Request.Context()).Model(&domain.PaymentRecord{})
	query = applySearchFilter(
		query,
		search,
		"LOWER(provider_tx_id) LIKE ?",
		"LOWER(status) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"(order_id IS NOT NULL AND CAST(order_id AS TEXT) LIKE ?)",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
	)

	var payments []domain.PaymentRecord
	pagination, err := paginateQuery(query, search, page, pageSize, "/dashboard/admin/payments", "id desc", &payments)
	if err != nil {
		s.renderAdminView(c, "admin_payments.html", map[string]interface{}{
			"FlashError": "Failed loading payment records: " + err.Error(),
			"Pagination": Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/payments"},
		})
		return
	}

	userIDs := make([]uint, 0, len(payments))
	for _, p := range payments {
		userIDs = append(userIDs, p.UserID)
	}

	s.renderAdminView(c, "admin_payments.html", map[string]interface{}{
		"Payments":   payments,
		"UserEmails": s.loadUserEmailMap(c.Request.Context(), userIDs),
		"Pagination": pagination,
	})
}

// AdminListJobs renders all queued and historical server_jobs with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/jobs?q=failed&page=1
func (s *Service) AdminListJobs(c *gin.Context) {
	if !s.requireAdmin(c, "Admin server jobs are restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := parseSearchAndPage(c)
	query := s.db.WithContext(c.Request.Context()).Model(&domain.ServerJob{})
	query = applySearchFilter(
		query,
		search,
		"LOWER(type) LIKE ?",
		"LOWER(status) LIKE ?",
		"LOWER(COALESCE(last_error, '')) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"CAST(server_id AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"server_id IN (SELECT id FROM servers WHERE LOWER(name) LIKE ?)",
	)

	var jobList []domain.ServerJob
	pagination, err := paginateQuery(query, search, page, pageSize, "/dashboard/admin/jobs", "id desc", &jobList)
	if err != nil {
		s.renderAdminView(c, "admin_jobs.html", map[string]interface{}{
			"FlashError": "Failed loading server jobs: " + err.Error(),
			"Pagination": Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/jobs"},
		})
		return
	}

	userIDs := make([]uint, 0, len(jobList))
	serverIDs := make([]uint, 0, len(jobList))
	for _, j := range jobList {
		userIDs = append(userIDs, j.UserID)
		serverIDs = append(serverIDs, j.ServerID)
	}

	s.renderAdminView(c, "admin_jobs.html", map[string]interface{}{
		"Jobs":        jobList,
		"UserEmails":  s.loadUserEmailMap(c.Request.Context(), userIDs),
		"ServerNames": s.loadServerNameMap(c.Request.Context(), serverIDs),
		"Pagination":  pagination,
	})
}

// AdminListLogs renders all server_operation_logs across the cluster with search and pagination.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/logs?q=backup&page=1
func (s *Service) AdminListLogs(c *gin.Context) {
	if !s.requireAdmin(c, "Admin operation logs are restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := parseSearchAndPage(c)
	query := s.db.WithContext(c.Request.Context()).Model(&domain.ServerOperationLog{})
	query = applySearchFilter(
		query,
		search,
		"LOWER(operation) LIKE ?",
		"LOWER(status) LIKE ?",
		"LOWER(message) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
		"CAST(server_id AS TEXT) LIKE ?",
		"user_id IN (SELECT id FROM users WHERE LOWER(email) LIKE ?)",
		"server_id IN (SELECT id FROM servers WHERE LOWER(name) LIKE ?)",
	)

	var logs []domain.ServerOperationLog
	pagination, err := paginateQuery(query, search, page, pageSize, "/dashboard/admin/logs", "id desc", &logs)
	if err != nil {
		s.renderAdminView(c, "admin_logs.html", map[string]interface{}{
			"FlashError": "Failed loading operation logs: " + err.Error(),
			"Pagination": Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/logs"},
		})
		return
	}

	userIDs := make([]uint, 0, len(logs))
	serverIDs := make([]uint, 0, len(logs))
	for _, l := range logs {
		userIDs = append(userIDs, l.UserID)
		serverIDs = append(serverIDs, l.ServerID)
	}

	s.renderAdminView(c, "admin_logs.html", map[string]interface{}{
		"Logs":        logs,
		"UserEmails":  s.loadUserEmailMap(c.Request.Context(), userIDs),
		"ServerNames": s.loadServerNameMap(c.Request.Context(), serverIDs),
		"Pagination":  pagination,
	})
}

// AdminListPlans renders all hosting plans with search, pagination, and CRUD controls.
//
// Arguments:
//   - c: Active Gin request context with optional query parameters 'q', 'page', and 'page_size'.
//
// Example usage:
//
//	GET /dashboard/admin/plans?q=small&page=1
func (s *Service) AdminListPlans(c *gin.Context) {
	if !s.requireAdmin(c, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	search, page, pageSize := parseSearchAndPage(c)
	query := s.db.WithContext(c.Request.Context()).Model(&domain.Plan{})
	query = applySearchFilter(
		query,
		search,
		"LOWER(name) LIKE ?",
		"CAST(id AS TEXT) LIKE ?",
	)

	var plans []domain.Plan
	pagination, err := paginateQuery(query, search, page, pageSize, "/dashboard/admin/plans", "id asc", &plans)
	if err != nil {
		s.renderAdminView(c, "admin_plans.html", map[string]interface{}{
			"FlashError": "Failed loading plans: " + err.Error(),
			"Pagination": Pagination{Query: search, Page: 1, TotalPages: 1, BaseURL: "/dashboard/admin/plans"},
		})
		return
	}

	s.renderAdminView(c, "admin_plans.html", map[string]interface{}{
		"Plans":      plans,
		"Pagination": pagination,
	})
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

// AdminCreatePlan creates a new hosting plan tier.
//
// Arguments:
//   - c: Active Gin request context with form fields name, cpu_limit, mem_limit_mb, disk_limit_gb, and price_cents.
//
// Example usage:
//
//	POST /dashboard/admin/plans
func (s *Service) AdminCreatePlan(c *gin.Context) {
	if !s.requireAdmin(c, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	plan, err := parsePlanForm(c)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}

	var existingCount int64
	if err := s.db.WithContext(c.Request.Context()).Model(&domain.Plan{}).Where("LOWER(name) = ?", strings.ToLower(plan.Name)).Count(&existingCount).Error; err == nil && existingCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(fmt.Sprintf("Plan %q already exists", plan.Name)))
		return
	}

	if err := s.db.WithContext(c.Request.Context()).Create(&plan).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Failed creating plan: "+err.Error()))
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/admin/plans?success=Plan+created")
}

// AdminUpdatePlan updates an existing hosting plan tier.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id' and form fields name, cpu_limit, mem_limit_mb, disk_limit_gb, price_cents.
//
// Example usage:
//
//	POST /dashboard/admin/plans/:id
func (s *Service) AdminUpdatePlan(c *gin.Context) {
	if !s.requireAdmin(c, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	planID, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Invalid+plan+ID")
		return
	}

	var plan domain.Plan
	if err := s.db.WithContext(c.Request.Context()).First(&plan, planID).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Plan+not+found")
		return
	}

	parsed, err := parsePlanForm(c)
	if err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}

	var duplicateCount int64
	if err := s.db.WithContext(c.Request.Context()).Model(&domain.Plan{}).
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

	if err := s.db.WithContext(c.Request.Context()).Save(&plan).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Failed updating plan: "+err.Error()))
		return
	}

	c.Redirect(http.StatusFound, "/dashboard/admin/plans?success=Plan+updated")
}

// AdminDeletePlan deletes a hosting plan if no servers or orders reference it.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	POST /dashboard/admin/plans/:id/delete
func (s *Service) AdminDeletePlan(c *gin.Context) {
	if !s.requireAdmin(c, "Admin plan management is restricted to administrator accounts.") {
		return
	}

	planID, ok := parseIDParam(c, "id")
	if !ok {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error=Invalid+plan+ID")
		return
	}

	var serverCount int64
	if err := s.db.WithContext(c.Request.Context()).Model(&domain.Server{}).Where("plan_id = ?", planID).Count(&serverCount).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}
	if serverCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Cannot delete plan in use by existing servers"))
		return
	}

	var orderCount int64
	if err := s.db.WithContext(c.Request.Context()).Model(&domain.Order{}).Where("plan_id = ?", planID).Count(&orderCount).Error; err != nil {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape(err.Error()))
		return
	}
	if orderCount > 0 {
		c.Redirect(http.StatusFound, "/dashboard/admin/plans?error="+url.QueryEscape("Cannot delete plan referenced by existing orders"))
		return
	}

	res := s.db.WithContext(c.Request.Context()).Delete(&domain.Plan{}, planID)
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
