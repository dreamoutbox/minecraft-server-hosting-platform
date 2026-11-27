package view

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

const (
	// DefaultServerRetentionDays defines the grace period after server expiration.
	DefaultServerRetentionDays = 7
	// ServerExpiringSoonWindow highlights servers expiring within 7 days.
	ServerExpiringSoonWindow = 7 * 24 * time.Hour

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

// RequireAdmin verifies the caller has the admin role, rendering a 403 page otherwise.
//
// Arguments:
//   - c: Active Gin request context.
//   - r: Template renderer for rendering the forbidden view.
//   - message: Error message shown on the forbidden page.
//
// Example usage:
//
//	if !view.RequireAdmin(c, h.view, "Admin only") { return }
func RequireAdmin(c *gin.Context, r *Renderer, message string) bool {
	if authmw.GetUserRoleFromContext(c).IsAdmin() {
		return true
	}
	r.RenderViewWithStatus(c, http.StatusForbidden, "forbidden.html", map[string]interface{}{
		"ErrorMessage": message,
	})
	return false
}

// EffectiveServerUserID returns 0 for staff and admin users so they can inspect and manage
// any server, and returns the authenticated user ID for regular users.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	ownerID := view.EffectiveServerUserID(c)
func EffectiveServerUserID(c *gin.Context) uint {
	if authmw.GetUserRoleFromContext(c).IsStaffOrAdmin() {
		return 0
	}
	userID, _ := authmw.GetUserIDFromContext(c)
	return userID
}

// ParseIDParam parses a positive uint path parameter from the Gin context.
//
// Arguments:
//   - c: Active Gin request context.
//   - name: Path parameter name (e.g. "id").
//
// Example usage:
//
//	id, ok := view.ParseIDParam(c, "id")
func ParseIDParam(c *gin.Context, name string) (uint, bool) {
	val, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || val == 0 {
		return 0, false
	}
	return uint(val), true
}

// ServerRedirectURL builds a server subpage URL with an optional query flash message.
//
// Arguments:
//   - serverID: Target server ID.
//   - subpath: Optional subpath (e.g. "/settings").
//   - flashKey: Optional query key ("success" or "error").
//   - message: Optional flash message text.
//
// Example usage:
//
//	c.Redirect(http.StatusFound, view.ServerRedirectURL(id, "/settings", "success", "Saved"))
func ServerRedirectURL(serverID uint, subpath, flashKey, message string) string {
	base := fmt.Sprintf("/dashboard/servers/%d%s", serverID, subpath)
	if flashKey == "" || message == "" {
		return base
	}
	return fmt.Sprintf("%s?%s=%s", base, flashKey, url.QueryEscape(message))
}

// ServerExpiryFlags computes whether a server is expired and whether it is still eligible for extension.
//
// Arguments:
//   - server: Target server model.
//   - now: Reference timestamp.
//
// Example usage:
//
//	isExpired, canExtend := view.ServerExpiryFlags(server, time.Now())
func ServerExpiryFlags(server *domain.Server, now time.Time) (bool, bool) {
	isExpired := server.IsExpired(now)
	canExtend := server.ExpiresAt != nil && server.IsWithinRetention(now, DefaultServerRetentionDays)
	return isExpired, canExtend
}

// ParseSearchAndPage extracts search query, page number, and page size from Gin query params.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	q, page, pageSize := view.ParseSearchAndPage(c)
func ParseSearchAndPage(c *gin.Context) (string, int, int) {
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

// ApplySearchFilter appends a case-insensitive LIKE OR clause across SQL conditions.
//
// Arguments:
//   - db: GORM query builder.
//   - query: User search term.
//   - conditions: SQL predicate expressions containing '?' placeholders.
//
// Example usage:
//
//	q := view.ApplySearchFilter(db, search, "LOWER(name) LIKE ?")
func ApplySearchFilter(db *gorm.DB, query string, conditions ...string) *gorm.DB {
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

// PaginateQuery counts, slices, and populates pagination metadata for an admin list query.
//
// Arguments:
//   - query: Filtered GORM query.
//   - search: Active search query string.
//   - page: Requested 1-based page number.
//   - pageSize: Items per page.
//   - baseURL: Base route path for pagination links.
//   - orderBy: SQL ORDER BY clause.
//   - dest: Pointer to result slice.
//
// Example usage:
//
//	p, err := view.PaginateQuery(q, search, page, pageSize, "/dashboard/admin/servers", "id desc", &rows)
func PaginateQuery(query *gorm.DB, search string, page, pageSize int, baseURL, orderBy string, dest interface{}) (Pagination, error) {
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

// AdminPageURL constructs a paginated admin list URL preserving search and page_size params.
//
// Arguments:
//   - baseURL: Route path.
//   - query: Search string.
//   - page: Target page number.
//   - pageSize: Items per page.
//
// Example usage:
//
//	url := view.AdminPageURL("/dashboard/admin/servers", "mc", 2, 20)
func AdminPageURL(baseURL, query string, page, pageSize int) string {
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

// LoadUserEmailMap resolves user IDs into a map of user ID -> email.
//
// Arguments:
//   - ctx: Operation context.
//   - db: GORM database handle.
//   - userIDs: Slice of user IDs (duplicates and zeros ignored).
//
// Example usage:
//
//	emails := view.LoadUserEmailMap(ctx, db, ids)
func LoadUserEmailMap(ctx context.Context, db *gorm.DB, userIDs []uint) map[uint]string {
	result := make(map[uint]string)
	unique := uniquePositiveIDs(userIDs)
	if len(unique) == 0 {
		return result
	}
	var users []domain.User
	_ = db.WithContext(ctx).Select("id, email").Where("id IN ?", unique).Find(&users).Error
	for _, u := range users {
		result[u.ID] = u.Email
	}
	return result
}

// LoadServerNameMap resolves server IDs into a map of server ID -> server name.
//
// Arguments:
//   - ctx: Operation context.
//   - db: GORM database handle.
//   - serverIDs: Slice of server IDs (duplicates and zeros ignored).
//
// Example usage:
//
//	names := view.LoadServerNameMap(ctx, db, ids)
func LoadServerNameMap(ctx context.Context, db *gorm.DB, serverIDs []uint) map[uint]string {
	result := make(map[uint]string)
	unique := uniquePositiveIDs(serverIDs)
	if len(unique) == 0 {
		return result
	}
	var servers []domain.Server
	_ = db.WithContext(ctx).Select("id, name").Where("id IN ?", unique).Find(&servers).Error
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
