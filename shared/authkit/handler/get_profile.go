package handler

import (
	"net/http"

	"github.com/dreamoutbox/authkit/middleware"
	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// GetProfile returns the authenticated user's profile details.
//
// Arguments:
//   - c: Active Gin request context populated by AuthMiddleware.
//
// Example usage:
//
//	protected.GET("/profile", authHandler.GetProfile)
func (h *Handler) GetProfile(c *gin.Context) {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var user shared.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": sanitizeUser(&user)})
}
