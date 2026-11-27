package handler

import (
	"net/http"

	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// LoginRequest holds login credentials.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// Login authenticates a user with email and password, returning an access and refresh token pair.
//
// Arguments:
//   - c: Active Gin request context containing LoginRequest JSON.
//
// Example usage:
//
//	router.POST("/login", authHandler.Login)
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid login input: " + err.Error()})
		return
	}

	user, err := shared.Authenticate(h.db, req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	tokens, err := h.generateTokens(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user":   sanitizeUser(user),
		"tokens": tokens,
	})
}
