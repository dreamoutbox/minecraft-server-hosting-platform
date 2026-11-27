package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dreamoutbox/authkit/middleware"
	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UpdateProfileRequest holds profile fields eligible for modification.
type UpdateProfileRequest struct {
	Email       string `json:"email" binding:"omitempty,email"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password" binding:"omitempty,min=8"`
}

// UpdateProfile allows authenticated users to update their profile email or password.
//
// Arguments:
//   - c: Active Gin request context populated by AuthMiddleware.
//
// Example usage:
//
//	protected.PUT("/profile", authHandler.UpdateProfile)
func (h *Handler) UpdateProfile(c *gin.Context) {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile update: " + err.Error()})
		return
	}

	var user shared.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if req.NewPassword != "" {
		if !shared.CheckPassword(user.PasswordHash, req.OldPassword) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "current password is incorrect"})
			return
		}
		newHashed, err := shared.HashPassword(req.NewPassword)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed hashing new password"})
			return
		}
		user.PasswordHash = newHashed
	}

	if req.Email != "" {
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		var existing shared.User
		if err := h.db.Where("email = ? AND id != ?", req.Email, user.ID).First(&existing).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "email is already in use"})
			return
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error verifying email"})
			return
		}
		user.Email = req.Email
	}

	if err := h.db.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed saving updated profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "profile updated successfully",
		"user":    sanitizeUser(&user),
	})
}
