package main

import (
	"log"
	"net/http"
	"os"

	"github.com/dreamoutbox/authkit"
	"github.com/dreamoutbox/authkit/handler"
	"github.com/dreamoutbox/authkit/middleware"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	defaultAddr      = ":8081"
	defaultJWTSecret = "dev-example-jwt-secret-change-in-prod"
)

func main() {
	db, err := gorm.Open(sqlite.Open("file:jwt_example?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed opening sqlite database: %v", err)
	}
	if err := authkit.SetupDB(db); err != nil {
		log.Fatalf("failed setting up authkit tables: %v", err)
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = defaultJWTSecret
	}

	r := gin.Default()

	authHandler := handler.NewHandler(db, jwtSecret)
	authHandler.RegisterRoutes(r.Group("/api/v1/auth"))

	api := r.Group("/api/v1")
	api.Use(middleware.AuthMiddleware(jwtSecret))
	{
		api.GET("/whoami", func(c *gin.Context) {
			userID, _ := middleware.GetUserIDFromContext(c)
			role := middleware.GetUserRoleFromContext(c)
			c.JSON(http.StatusOK, gin.H{
				"user_id": userID,
				"role":    role,
			})
		})
	}

	log.Printf("JWT API example listening on %s", defaultAddr)
	if err := r.Run(defaultAddr); err != nil {
		log.Fatal(err)
	}
}
