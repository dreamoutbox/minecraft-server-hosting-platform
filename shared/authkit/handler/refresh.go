package handler

import (
	"net/http"

	"github.com/dreamoutbox/authkit/authkitjwt"
	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// RefreshRequest holds the refresh token for renewal.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Refresh validates a refresh token and generates a new token pair.
//
// Arguments:
//   - c: Active Gin request context containing RefreshRequest JSON.
//
// Example usage:
//
//	router.POST("/refresh", authHandler.Refresh)
func (h *Handler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid refresh input: " + err.Error()})
		return
	}

	claims, err := authkitjwt.ParseAndValidateToken(req.RefreshToken, h.jwtSecret, authkitjwt.TokenTypeRefresh)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	var user shared.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user no longer exists"})
		return
	}

	tokens, err := h.generateTokens(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating refreshed tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"tokens": tokens,
	})
}
