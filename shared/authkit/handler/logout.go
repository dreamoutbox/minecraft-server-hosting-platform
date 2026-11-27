package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Logout acknowledges user sign-out.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	router.POST("/logout", authHandler.Logout)
func (h *Handler) Logout(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "logged out successfully"})
}
