package mcservers

import (
	"time"

	authmw "github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard/view"
)

// ExpiringServerAlert represents a dashboard warning entry for a server that is expiring soon or recently expired.
type ExpiringServerAlert struct {
	ServerID   uint
	ServerName string
	ExpiresAt  time.Time
	IsExpired  bool
}

// ListServers displays all game servers owned by the authenticated user.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	GET /dashboard
func (h *Handler) ListServers(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	servers, err := h.hostingSvc.ListServers(c.Request.Context(), userID)
	if err != nil {
		h.view.RenderView(c, "servers_list.html", map[string]interface{}{"FlashError": err.Error()})
		return
	}

	now := time.Now()
	var expiringAlerts []ExpiringServerAlert
	for _, srv := range servers {
		if srv.ExpiresAt == nil {
			continue
		}
		isExpired := srv.IsExpired(now)
		if srv.IsExpiringSoon(now, view.ServerExpiringSoonWindow) ||
			(isExpired && srv.IsWithinRetention(now, view.DefaultServerRetentionDays)) {
			expiringAlerts = append(expiringAlerts, ExpiringServerAlert{
				ServerID:   srv.ID,
				ServerName: srv.Name,
				ExpiresAt:  *srv.ExpiresAt,
				IsExpired:  isExpired,
			})
		}
	}

	h.view.RenderView(c, "servers_list.html", map[string]interface{}{
		"Servers":         servers,
		"ExpiringServers": expiringAlerts,
	})
}
