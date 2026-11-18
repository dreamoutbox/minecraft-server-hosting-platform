package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

func doDashboardReq(r *gin.Engine, method, target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAdminMenuSearchPaginationAndPlanCRUD(t *testing.T) {
	r, dashSvc, _ := setupDashboardTestEnv(t)
	if r == nil {
		return
	}

	suffix := time.Now().UnixNano()
	userEmail := fmt.Sprintf("admin-test-user-%d@example.com", suffix)
	adminEmail := fmt.Sprintf("admin-test-owner-%d@example.com", suffix)
	userRecord := domain.User{Email: userEmail, PasswordHash: "hash", Role: domain.UserRoleUser}
	adminRecord := domain.User{Email: adminEmail, PasswordHash: "hash", Role: domain.UserRoleAdmin}
	if err := dashSvc.db.Create(&userRecord).Error; err != nil {
		t.Fatalf("failed creating test user: %v", err)
	}
	if err := dashSvc.db.Create(&adminRecord).Error; err != nil {
		t.Fatalf("failed creating test admin: %v", err)
	}

	var defaultPlan domain.Plan
	if err := dashSvc.db.Order("id asc").First(&defaultPlan).Error; err != nil {
		t.Fatalf("expected seeded plan: %v", err)
	}

	var host domain.Host
	if err := dashSvc.db.First(&host).Error; err != nil {
		t.Fatalf("expected seeded host: %v", err)
	}

	srv1 := domain.Server{
		UserID:        userRecord.ID,
		PlanID:        defaultPlan.ID,
		HostID:        host.ID,
		Name:          fmt.Sprintf("AlphaWorld-%d", suffix),
		Status:        domain.ServerStatusRunning,
		Port:          25741,
		ServerType:    domain.ServerTypePaper,
		ServerVersion: "1.21.4",
	}
	srv2 := domain.Server{
		UserID:        adminRecord.ID,
		PlanID:        defaultPlan.ID,
		HostID:        host.ID,
		Name:          fmt.Sprintf("BetaWorld-%d", suffix),
		Status:        domain.ServerStatusStopped,
		Port:          25742,
		ServerType:    domain.ServerTypeVanilla,
		ServerVersion: "LATEST",
	}
	srv3 := domain.Server{
		UserID:        userRecord.ID,
		PlanID:        defaultPlan.ID,
		HostID:        host.ID,
		Name:          fmt.Sprintf("GammaWorld-%d", suffix),
		Status:        domain.ServerStatusError,
		Port:          25743,
		ServerType:    domain.ServerTypeFabric,
		ServerVersion: "1.20.4",
	}
	for _, srv := range []*domain.Server{&srv1, &srv2, &srv3} {
		if err := dashSvc.db.Create(srv).Error; err != nil {
			t.Fatalf("failed creating test server: %v", err)
		}
	}

	order1 := domain.Order{
		UserID:          userRecord.ID,
		PlanID:          defaultPlan.ID,
		ServerID:        &srv1.ID,
		OrderType:       domain.OrderTypeCreateServer,
		ServerName:      srv1.Name,
		ServerType:      srv1.ServerType,
		ServerVersion:   srv1.ServerVersion,
		DurationDays:    30,
		StripeSessionID: fmt.Sprintf("cs_admin_alpha_%d", suffix),
		AmountCents:     defaultPlan.PriceCents,
		Status:          domain.OrderStatusFulfilled,
	}
	order2 := domain.Order{
		UserID:          adminRecord.ID,
		PlanID:          defaultPlan.ID,
		ServerID:        &srv2.ID,
		OrderType:       domain.OrderTypeExtendServer,
		ServerName:      srv2.Name,
		ServerType:      srv2.ServerType,
		ServerVersion:   srv2.ServerVersion,
		DurationDays:    30,
		StripeSessionID: fmt.Sprintf("cs_admin_beta_%d", suffix),
		AmountCents:     defaultPlan.PriceCents,
		Status:          domain.OrderStatusPending,
	}
	if err := dashSvc.db.Create(&order1).Error; err != nil {
		t.Fatalf("failed creating order1: %v", err)
	}
	if err := dashSvc.db.Create(&order2).Error; err != nil {
		t.Fatalf("failed creating order2: %v", err)
	}

	pay1 := domain.PaymentRecord{
		UserID:       userRecord.ID,
		OrderID:      &order1.ID,
		AmountCents:  500,
		Status:       domain.PaymentStatusSucceeded,
		ProviderTxID: fmt.Sprintf("pi_alpha_%d", suffix),
	}
	pay2 := domain.PaymentRecord{
		UserID:       adminRecord.ID,
		OrderID:      &order2.ID,
		AmountCents:  1000,
		Status:       domain.PaymentStatusFailed,
		ProviderTxID: fmt.Sprintf("pi_beta_%d", suffix),
	}
	if err := dashSvc.db.Create(&pay1).Error; err != nil {
		t.Fatalf("failed creating pay1: %v", err)
	}
	if err := dashSvc.db.Create(&pay2).Error; err != nil {
		t.Fatalf("failed creating pay2: %v", err)
	}

	job1 := domain.ServerJob{
		Type:        domain.JobTypeBackup,
		Status:      domain.JobStatusCompleted,
		UserID:      userRecord.ID,
		ServerID:    srv1.ID,
		Attempts:    1,
		MaxAttempts: 3,
		NextRunAt:   time.Now(),
	}
	job2 := domain.ServerJob{
		Type:        domain.JobTypeRestore,
		Status:      domain.JobStatusFailed,
		UserID:      adminRecord.ID,
		ServerID:    srv2.ID,
		Attempts:    3,
		MaxAttempts: 3,
		LastError:   fmt.Sprintf("restore-boom-%d", suffix),
		NextRunAt:   time.Now(),
	}
	if err := dashSvc.db.Create(&job1).Error; err != nil {
		t.Fatalf("failed creating job1: %v", err)
	}
	if err := dashSvc.db.Create(&job2).Error; err != nil {
		t.Fatalf("failed creating job2: %v", err)
	}

	log1 := domain.ServerOperationLog{
		ServerID:  srv1.ID,
		UserID:    userRecord.ID,
		Operation: domain.ServerOpBackup,
		Status:    domain.ServerLogStatusSuccess,
		Message:   fmt.Sprintf("snapshot-ok-%d", suffix),
	}
	log2 := domain.ServerOperationLog{
		ServerID:  srv2.ID,
		UserID:    adminRecord.ID,
		Operation: domain.ServerOpRestore,
		Status:    domain.ServerLogStatusError,
		Message:   fmt.Sprintf("restore-err-%d", suffix),
	}
	if err := dashSvc.db.Create(&log1).Error; err != nil {
		t.Fatalf("failed creating log1: %v", err)
	}
	if err := dashSvc.db.Create(&log2).Error; err != nil {
		t.Fatalf("failed creating log2: %v", err)
	}

	t.Cleanup(func() {
		dashSvc.db.Delete(&domain.ServerOperationLog{}, []uint{log1.ID, log2.ID})
		dashSvc.db.Delete(&domain.ServerJob{}, []uint{job1.ID, job2.ID})
		dashSvc.db.Delete(&domain.PaymentRecord{}, []uint{pay1.ID, pay2.ID})
		dashSvc.db.Delete(&domain.Order{}, []uint{order1.ID, order2.ID})
		dashSvc.db.Delete(&domain.Server{}, []uint{srv1.ID, srv2.ID, srv3.ID})
		dashSvc.db.Delete(&domain.User{}, []uint{userRecord.ID, adminRecord.ID})
	})

	userTokens, _ := authkit.GenerateTokenPair(userRecord.ID, userEmail, domain.UserRoleUser, dashSvc.jwtSecret)
	staffTokens, _ := authkit.GenerateTokenPair(999997, "staff-admin-test@example.com", domain.UserRoleStaff, dashSvc.jwtSecret)
	adminTokens, _ := authkit.GenerateTokenPair(adminRecord.ID, adminEmail, domain.UserRoleAdmin, dashSvc.jwtSecret)

	userCookie := &http.Cookie{Name: "token", Value: userTokens.AccessToken}
	staffCookie := &http.Cookie{Name: "token", Value: staffTokens.AccessToken}
	adminCookie := &http.Cookie{Name: "token", Value: adminTokens.AccessToken}

	// 1. Navbar visibility: regular/staff do not see Admin dropdown; admin does
	wDashStaff := doDashboardReq(r, http.MethodGet, "/dashboard", staffCookie, nil)
	if wDashStaff.Code != http.StatusOK || strings.Contains(wDashStaff.Body.String(), "/dashboard/admin/servers") {
		t.Fatalf("staff should not see admin navbar menu (code=%d)", wDashStaff.Code)
	}
	wDashAdmin := doDashboardReq(r, http.MethodGet, "/dashboard", adminCookie, nil)
	dashBody := wDashAdmin.Body.String()
	if !strings.Contains(dashBody, ">|</li>") || strings.Contains(dashBody, "dropdown-toggle") {
		t.Errorf("expected flat admin navbar separated with '|' and no dropdown")
	}
	for _, path := range []string{
		"/dashboard/users",
		"/dashboard/admin/servers",
		"/dashboard/admin/orders",
		"/dashboard/admin/payments",
		"/dashboard/admin/jobs",
		"/dashboard/admin/logs",
		"/dashboard/admin/plans",
	} {
		if !strings.Contains(dashBody, path) {
			t.Errorf("expected admin navbar to contain %s", path)
		}
	}

	// 2. RBAC enforcement on all admin routes (user & staff -> 403)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/dashboard/admin/servers"},
		{http.MethodGet, "/dashboard/admin/orders"},
		{http.MethodGet, "/dashboard/admin/payments"},
		{http.MethodGet, "/dashboard/admin/jobs"},
		{http.MethodGet, "/dashboard/admin/logs"},
		{http.MethodGet, "/dashboard/admin/plans"},
		{http.MethodPost, "/dashboard/admin/plans"},
		{http.MethodPost, fmt.Sprintf("/dashboard/admin/plans/%d", defaultPlan.ID)},
		{http.MethodPost, fmt.Sprintf("/dashboard/admin/plans/%d/delete", defaultPlan.ID)},
	} {
		for _, cookie := range []*http.Cookie{userCookie, staffCookie} {
			w := doDashboardReq(r, tc.method, tc.path, cookie, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for non-admin on %s %s, got %d", tc.method, tc.path, w.Code)
			}
		}
	}

	// 3. All Servers: search + pagination + admin detail access for another user's server
	wSrvSearch := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/servers?q=%s", url.QueryEscape(srv1.Name)), adminCookie, nil)
	if wSrvSearch.Code != http.StatusOK || !strings.Contains(wSrvSearch.Body.String(), srv1.Name) || strings.Contains(wSrvSearch.Body.String(), srv2.Name) {
		t.Fatalf("admin server search failed (code=%d)", wSrvSearch.Code)
	}
	wSrvPage1 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/servers?q=%d&page=1&page_size=2", suffix), adminCookie, nil)
	wSrvPage2 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/servers?q=%d&page=2&page_size=2", suffix), adminCookie, nil)
	if wSrvPage1.Code != http.StatusOK || wSrvPage2.Code != http.StatusOK {
		t.Fatalf("admin servers pagination failed: p1=%d p2=%d", wSrvPage1.Code, wSrvPage2.Code)
	}
	if !strings.Contains(wSrvPage1.Body.String(), `<main class="container-fluid px-4">`) {
		t.Errorf("expected admin table page to render full-width main container")
	}
	if !strings.Contains(wSrvPage1.Body.String(), srv3.Name) || !strings.Contains(wSrvPage1.Body.String(), srv2.Name) || strings.Contains(wSrvPage1.Body.String(), srv1.Name) {
		t.Errorf("expected page 1 (page_size=2, desc) to contain srv3 and srv2 only")
	}
	if !strings.Contains(wSrvPage2.Body.String(), srv1.Name) || strings.Contains(wSrvPage2.Body.String(), srv3.Name) {
		t.Errorf("expected page 2 (page_size=2, desc) to contain srv1 only")
	}
	wSrvDetailAdmin := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/servers/%d", srv1.ID), adminCookie, nil)
	if wSrvDetailAdmin.Code != http.StatusOK || !strings.Contains(wSrvDetailAdmin.Body.String(), srv1.Name) {
		t.Fatalf("expected admin to view user server detail page, got %d", wSrvDetailAdmin.Code)
	}

	// 4. Orders: search + pagination
	wOrdPage1 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/orders?q=%d&page=1&page_size=1", suffix), adminCookie, nil)
	wOrdPage2 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/orders?q=%d&page=2&page_size=1", suffix), adminCookie, nil)
	if !strings.Contains(wOrdPage1.Body.String(), order2.StripeSessionID) || strings.Contains(wOrdPage1.Body.String(), order1.StripeSessionID) {
		t.Errorf("expected orders page 1 to show order2 only")
	}
	if !strings.Contains(wOrdPage2.Body.String(), order1.StripeSessionID) || strings.Contains(wOrdPage2.Body.String(), order2.StripeSessionID) {
		t.Errorf("expected orders page 2 to show order1 only")
	}

	// 5. Payment Records: search + pagination
	wPaySearch := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/payments?q=%s", url.QueryEscape(pay1.ProviderTxID)), adminCookie, nil)
	if wPaySearch.Code != http.StatusOK || !strings.Contains(wPaySearch.Body.String(), pay1.ProviderTxID) || strings.Contains(wPaySearch.Body.String(), pay2.ProviderTxID) {
		t.Errorf("payment search failed")
	}
	wPayPage2 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/payments?q=%d&page=2&page_size=1", suffix), adminCookie, nil)
	if !strings.Contains(wPayPage2.Body.String(), pay1.ProviderTxID) {
		t.Errorf("expected payments page 2 to show pay1")
	}

	// 6. Server Jobs: search + pagination
	wJobSearch := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/jobs?q=%s", url.QueryEscape(job2.LastError)), adminCookie, nil)
	if wJobSearch.Code != http.StatusOK || !strings.Contains(wJobSearch.Body.String(), job2.LastError) {
		t.Errorf("server jobs search failed")
	}
	wJobPage2 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/jobs?q=%s&page=2&page_size=1", url.QueryEscape(userEmail)), adminCookie, nil)
	wJobPage1All := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/jobs?q=%s&page=1&page_size=1", url.QueryEscape(fmt.Sprintf("World-%d", suffix))), adminCookie, nil)
	wJobPage2All := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/jobs?q=%s&page=2&page_size=1", url.QueryEscape(fmt.Sprintf("World-%d", suffix))), adminCookie, nil)
	if wJobPage2.Code != http.StatusOK || !strings.Contains(wJobPage1All.Body.String(), srv2.Name) || !strings.Contains(wJobPage2All.Body.String(), srv1.Name) {
		t.Errorf("server jobs pagination failed")
	}

	// 7. Operation Logs: search + pagination
	wLogSearch := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/logs?q=%s", url.QueryEscape(log1.Message)), adminCookie, nil)
	if wLogSearch.Code != http.StatusOK || !strings.Contains(wLogSearch.Body.String(), log1.Message) || strings.Contains(wLogSearch.Body.String(), log2.Message) {
		t.Errorf("operation logs search failed")
	}
	wLogPage2 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/logs?q=%d&page=2&page_size=1", suffix), adminCookie, nil)
	if !strings.Contains(wLogPage2.Body.String(), log1.Message) {
		t.Errorf("expected operation logs page 2 to show log1")
	}

	// 8. Plans CRUD + Search + Pagination
	planName1 := fmt.Sprintf("CustomTierA-%d", suffix)
	planName2 := fmt.Sprintf("CustomTierB-%d", suffix)
	planNameUpdated := fmt.Sprintf("CustomTierA-Edited-%d", suffix)

	// Invalid plan create -> error redirect
	wInvalidPlan := doDashboardReq(r, http.MethodPost, "/dashboard/admin/plans", adminCookie, url.Values{
		"name":          {""},
		"cpu_limit":     {"0"},
		"mem_limit_mb":  {"1024"},
		"disk_limit_gb": {"10"},
		"price_cents":   {"500"},
	})
	if wInvalidPlan.Code != http.StatusFound || !strings.Contains(wInvalidPlan.Header().Get("Location"), "error=") {
		t.Fatalf("expected error redirect for invalid plan, got %d %s", wInvalidPlan.Code, wInvalidPlan.Header().Get("Location"))
	}

	// Create two plans
	for _, pName := range []string{planName1, planName2} {
		wCreate := doDashboardReq(r, http.MethodPost, "/dashboard/admin/plans", adminCookie, url.Values{
			"name":          {pName},
			"cpu_limit":     {"3"},
			"mem_limit_mb":  {"3072"},
			"disk_limit_gb": {"30"},
			"price_cents":   {"1500"},
		})
		if wCreate.Code != http.StatusFound || !strings.Contains(wCreate.Header().Get("Location"), "success=") {
			t.Fatalf("expected plan create success redirect, got %d %s", wCreate.Code, wCreate.Header().Get("Location"))
		}
	}

	var createdPlan1, createdPlan2 domain.Plan
	if err := dashSvc.db.Where("name = ?", planName1).First(&createdPlan1).Error; err != nil {
		t.Fatalf("created plan 1 missing from db: %v", err)
	}
	if err := dashSvc.db.Where("name = ?", planName2).First(&createdPlan2).Error; err != nil {
		t.Fatalf("created plan 2 missing from db: %v", err)
	}
	t.Cleanup(func() {
		dashSvc.db.Delete(&domain.Plan{}, []uint{createdPlan1.ID, createdPlan2.ID})
	})

	// Pagination & Search on Plans
	wPlansPage1 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/plans?q=%d&page=1&page_size=1", suffix), adminCookie, nil)
	wPlansPage2 := doDashboardReq(r, http.MethodGet, fmt.Sprintf("/dashboard/admin/plans?q=%d&page=2&page_size=1", suffix), adminCookie, nil)
	if !strings.Contains(wPlansPage1.Body.String(), planName1) || !strings.Contains(wPlansPage2.Body.String(), planName2) {
		t.Errorf("expected plans pagination to split plan1 and plan2 across pages")
	}

	// Update plan1
	wUpdate := doDashboardReq(r, http.MethodPost, fmt.Sprintf("/dashboard/admin/plans/%d", createdPlan1.ID), adminCookie, url.Values{
		"name":          {planNameUpdated},
		"cpu_limit":     {"6"},
		"mem_limit_mb":  {"6144"},
		"disk_limit_gb": {"60"},
		"price_cents":   {"2500"},
	})
	if wUpdate.Code != http.StatusFound || !strings.Contains(wUpdate.Header().Get("Location"), "success=") {
		t.Fatalf("expected plan update success redirect, got %d %s", wUpdate.Code, wUpdate.Header().Get("Location"))
	}
	var refreshed domain.Plan
	_ = dashSvc.db.First(&refreshed, createdPlan1.ID)
	if refreshed.Name != planNameUpdated || refreshed.CPULimit != 6 || refreshed.PriceCents != 2500 {
		t.Errorf("plan update did not persist: %+v", refreshed)
	}

	// Delete plan in use by server -> blocked cleanly
	wDelInUse := doDashboardReq(r, http.MethodPost, fmt.Sprintf("/dashboard/admin/plans/%d/delete", defaultPlan.ID), adminCookie, nil)
	if wDelInUse.Code != http.StatusFound || !strings.Contains(wDelInUse.Header().Get("Location"), "error=") {
		t.Fatalf("expected delete of in-use plan to fail cleanly, got %d %s", wDelInUse.Code, wDelInUse.Header().Get("Location"))
	}

	// Delete unused plan -> succeeds
	wDelUnused := doDashboardReq(r, http.MethodPost, fmt.Sprintf("/dashboard/admin/plans/%d/delete", createdPlan2.ID), adminCookie, nil)
	if wDelUnused.Code != http.StatusFound || !strings.Contains(wDelUnused.Header().Get("Location"), "success=") {
		t.Fatalf("expected delete of unused plan to succeed, got %d %s", wDelUnused.Code, wDelUnused.Header().Get("Location"))
	}
	if err := dashSvc.db.First(&domain.Plan{}, createdPlan2.ID).Error; err == nil {
		t.Errorf("expected deleted plan %d to be removed from database", createdPlan2.ID)
	}
}
