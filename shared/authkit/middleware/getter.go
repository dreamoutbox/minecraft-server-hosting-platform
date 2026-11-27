package middleware

import (
	"errors"

	"github.com/dreamoutbox/authkit/shared"
	"github.com/gin-gonic/gin"
)

// GetUserIDFromContext retrieves the authenticated user ID set by auth middleware.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	userID, err := middleware.GetUserIDFromContext(c)
//	if err != nil {
//	    c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
//	    return
//	}
func GetUserIDFromContext(c *gin.Context) (uint, error) {
	val, exists := c.Get(shared.ContextUserIDKey)
	if !exists {
		return 0, errors.New("user ID missing from context")
	}
	id, ok := val.(uint)
	if !ok {
		return 0, errors.New("user ID in context is not a valid uint")
	}
	return id, nil
}

// GetUserRoleFromContext retrieves the authenticated user role from Gin context.
// Defaults to shared.UserRoleUser when missing or unrecognized.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	role := middleware.GetUserRoleFromContext(c)
func GetUserRoleFromContext(c *gin.Context) shared.UserRole {
	val, exists := c.Get(shared.ContextRoleKey)
	if !exists {
		return shared.UserRoleUser
	}
	switch r := val.(type) {
	case shared.UserRole:
		if r != "" {
			return r
		}
	case string:
		if r != "" {
			return shared.UserRole(r)
		}
	}
	return shared.UserRoleUser
}
