package authkit

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ErrInvalidCredentials indicates the supplied email or password failed verification.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Handler provides HTTP controller methods for user authentication and profile management.
type Handler struct {
	db        *gorm.DB
	jwtSecret string
}

// NewHandler initializes a new authentication handler.
//
// Arguments:
//   - db: Database connection for user queries and updates.
//   - jwtSecret: Secret key used to sign and verify JWT tokens.
//
// Example usage:
//
//	handler := authkit.NewHandler(db, cfg.JWTSecret)
func NewHandler(db *gorm.DB, jwtSecret string) *Handler {
	return &Handler{
		db:        db,
		jwtSecret: jwtSecret,
	}
}

// RegisterRequest holds registration inputs.
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

// LoginRequest holds login credentials.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// RefreshRequest holds the refresh token for renewal.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// UpdateProfileRequest holds profile fields eligible for modification.
type UpdateProfileRequest struct {
	Email       string `json:"email" binding:"omitempty,email"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password" binding:"omitempty,min=8"`
}

// UserResponse sanitizes user details for client responses.
type UserResponse struct {
	ID        uint     `json:"id"`
	Email     string   `json:"email"`
	Role      UserRole `json:"role"`
	CreatedAt string   `json:"created_at"`
}

// sanitizeUser converts User into client-safe UserResponse.
func sanitizeUser(user *User) UserResponse {
	return UserResponse{
		ID:        user.ID,
		Email:     user.Email,
		Role:      user.Role,
		CreatedAt: user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// Authenticate verifies an email and plaintext password against the users table.
//
// Arguments:
//   - db: Active GORM database handle containing the users table.
//   - email: Account email address (trimmed and lowercased before lookup).
//   - password: Plaintext password candidate to verify.
//
// Example usage:
//
//	user, err := authkit.Authenticate(db, "player@example.com", "secret-pass")
func Authenticate(db *gorm.DB, email, password string) (*User, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	var user User
	if err := db.Where("email = ?", normalized).First(&user).Error; err != nil {
		return nil, ErrInvalidCredentials
	}

	if !CheckPassword(user.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}

	return &user, nil
}

// Register creates a new user account, hashes credentials, and issues an initial JWT token pair.
//
// Arguments:
//   - c: Active Gin request context containing RegisterRequest JSON.
//
// Example usage:
//
//	router.POST("/register", handler.Register)
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid registration input: " + err.Error()})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	var existing User
	if err := h.db.Where("email = ?", req.Email).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "user with this email already exists"})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error verifying email"})
		return
	}

	hashedPassword, err := HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed hashing credentials"})
		return
	}

	newUser := User{
		Email:        req.Email,
		PasswordHash: hashedPassword,
		Role:         UserRoleUser,
	}

	if err := h.db.Create(&newUser).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed creating user record"})
		return
	}

	tokens, err := GenerateTokenPair(newUser.ID, newUser.Email, newUser.Role, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating tokens"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"user":   sanitizeUser(&newUser),
		"tokens": tokens,
	})
}

// Login authenticates a user with email and password, returning an access and refresh token pair.
//
// Arguments:
//   - c: Active Gin request context containing LoginRequest JSON.
//
// Example usage:
//
//	router.POST("/login", handler.Login)
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid login input: " + err.Error()})
		return
	}

	user, err := Authenticate(h.db, req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	tokens, err := GenerateTokenPair(user.ID, user.Email, user.Role, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user":   sanitizeUser(user),
		"tokens": tokens,
	})
}

// Refresh validates a refresh token and generates a new token pair.
//
// Arguments:
//   - c: Active Gin request context containing RefreshRequest JSON.
//
// Example usage:
//
//	router.POST("/refresh", handler.Refresh)
func (h *Handler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid refresh input: " + err.Error()})
		return
	}

	claims, err := ParseAndValidateToken(req.RefreshToken, h.jwtSecret, TokenTypeRefresh)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	var user User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user no longer exists"})
		return
	}

	tokens, err := GenerateTokenPair(user.ID, user.Email, user.Role, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed generating refreshed tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"tokens": tokens,
	})
}

// Logout acknowledges user sign-out.
//
// Arguments:
//   - c: Active Gin request context.
//
// Example usage:
//
//	router.POST("/logout", handler.Logout)
func (h *Handler) Logout(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "logged out successfully"})
}

// GetProfile returns the authenticated user's profile details.
//
// Arguments:
//   - c: Active Gin request context populated by AuthMiddleware.
//
// Example usage:
//
//	protected.GET("/profile", handler.GetProfile)
func (h *Handler) GetProfile(c *gin.Context) {
	userID, err := GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var user User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": sanitizeUser(&user)})
}

// UpdateProfile allows authenticated users to update their profile email or password.
//
// Arguments:
//   - c: Active Gin request context populated by AuthMiddleware.
//
// Example usage:
//
//	protected.PUT("/profile", handler.UpdateProfile)
func (h *Handler) UpdateProfile(c *gin.Context) {
	userID, err := GetUserIDFromContext(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile update: " + err.Error()})
		return
	}

	var user User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if req.NewPassword != "" {
		if !CheckPassword(user.PasswordHash, req.OldPassword) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "current password is incorrect"})
			return
		}
		newHashed, err := HashPassword(req.NewPassword)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed hashing new password"})
			return
		}
		user.PasswordHash = newHashed
	}

	if req.Email != "" {
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		var existing User
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

// RegisterRoutes binds authentication and self-service profile endpoints onto the router.
//
// Arguments:
//   - rg: Target router group (e.g. /api/v1/auth).
//
// Example usage:
//
//	authHandler.RegisterRoutes(router.Group("/api/v1/auth"))
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/register", h.Register)
	rg.POST("/login", h.Login)
	rg.POST("/refresh", h.Refresh)
	rg.POST("/logout", h.Logout)

	protected := rg.Group("")
	protected.Use(AuthMiddleware(h.jwtSecret))
	{
		protected.GET("/profile", h.GetProfile)
		protected.PUT("/profile", h.UpdateProfile)
	}
}
