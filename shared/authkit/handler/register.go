package handler

import (
	"errors"
	"net/http"

	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// RegisterRequest holds registration inputs.
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

// Register creates a new user account, hashes credentials, and issues an initial JWT token pair.
//
// Arguments:
//   - c: Active Gin request context containing RegisterRequest JSON.
//
// Example usage:
//
//	router.POST("/register", authHandler.Register)
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid registration input: " + err.Error()})
		return
	}

	newUser, err := shared.RegisterUser(h.db, req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, shared.ErrInvalidEmail), errors.Is(err, shared.ErrPasswordTooShort):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, shared.ErrEmailAlreadyExists):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed creating user record"})
		}
		return
	}

	tokens, err := h.generateTokens(newUser)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating tokens"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"user":   sanitizeUser(newUser),
		"tokens": tokens,
	})
}
