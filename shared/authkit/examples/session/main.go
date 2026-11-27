package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/dreamoutbox/authkit"
	"github.com/dreamoutbox/authkit/handler"
	"github.com/dreamoutbox/authkit/middleware"
	"github.com/dreamoutbox/authkit/session"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	defaultAddr       = ":8080"
	defaultSessionTTL = 24 * time.Hour
)

func main() {
	db, err := gorm.Open(sqlite.Open("file:session_example?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed opening sqlite database: %v", err)
	}
	if err := authkit.SetupDB(db); err != nil {
		log.Fatalf("failed setting up authkit tables: %v", err)
	}

	cookieSecure := os.Getenv("COOKIE_SECURE") == "true"
	cookieCfg := session.DefaultCookieConfig(session.DefaultCookieName, defaultSessionTTL, cookieSecure)
	authsessions := session.NewManager(db, cookieCfg)

	r := gin.Default()

	r.POST("/register", func(c *gin.Context) {
		var req handler.RegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, err := authkit.RegisterUser(db, req.Email, req.Password); err != nil {
			switch {
			case errors.Is(err, authkit.ErrInvalidEmail), errors.Is(err, authkit.ErrPasswordTooShort):
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			case errors.Is(err, authkit.ErrEmailAlreadyExists):
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
			}
			return
		}
		c.JSON(http.StatusCreated, gin.H{"message": "registered; please log in"})
	})

	r.POST("/login", func(c *gin.Context) {
		var req handler.LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		user, err := authkit.Authenticate(db, req.Email, req.Password)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		if err := authsessions.SetSession(c, user.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed creating session"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "logged in"})
	})

	r.POST("/logout", func(c *gin.Context) {
		_ = authsessions.Logout(c)
		c.JSON(http.StatusOK, gin.H{"message": "logged out"})
	})

	// Pass "/login" instead of "" to redirect browser clients on missing/expired sessions.
	protected := r.Group("/dashboard")
	protected.Use(authsessions.Middleware(""))
	{
		protected.GET("/me", func(c *gin.Context) {
			userID, _ := middleware.GetUserIDFromContext(c)
			role := middleware.GetUserRoleFromContext(c)
			c.JSON(http.StatusOK, gin.H{
				"user_id": userID,
				"role":    role,
			})
		})
	}

	log.Printf("Session auth example listening on %s", defaultAddr)
	if err := r.Run(defaultAddr); err != nil {
		log.Fatal(err)
	}
}
