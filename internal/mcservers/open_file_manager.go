package mcservers

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	authmw "github.com/dreamoutbox/go-authkit/middleware"
	"github.com/gin-gonic/gin"
)

// OpenFileManager provisions fileserver access for the server and redirects to the node's file manager.
//
// Arguments:
//   - c: Active Gin request context with URL parameter 'id'.
//
// Example usage:
//
//	GET /dashboard/servers/:id/files/open
func (h *Handler) OpenFileManager(c *gin.Context) {
	userID, _ := authmw.GetUserIDFromContext(c)
	allowNonOwner := authmw.GetUserRoleFromContext(c).IsStaffOrAdmin()
	serverIDVal, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || serverIDVal == 0 {
		c.Redirect(http.StatusFound, "/dashboard?error=Invalid+server+ID")
		return
	}

	targetURL, err := h.hostingSvc.OpenFileManagerURL(c.Request.Context(), userID, uint(serverIDVal), allowNonOwner)
	if err != nil {
		c.Redirect(http.StatusFound, fmt.Sprintf("/dashboard/servers/%d?error=%s", serverIDVal, url.QueryEscape(err.Error())))
		return
	}

	c.Redirect(http.StatusFound, targetURL)
}
