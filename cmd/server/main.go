package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/config"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

// SetupRouter initializes HTTP routes and middleware for the orchestrator API.
//
// Arguments:
//   - db: Active database handle for data layer interactions.
//   - cfg: Loaded runtime configuration containing ports and limits.
//
// Example usage:
//   router := SetupRouter(db, cfg)
//   router.Run(":8080")
func SetupRouter(db *gorm.DB, cfg *config.Config) *gin.Engine {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		sqlDB, err := db.DB()
		if err != nil || sqlDB.Ping() != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "degraded",
				"error":  "database unavailable",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":         "ok",
			"port_range_min": cfg.PortRangeMin,
			"port_range_max": cfg.PortRangeMax,
		})
	})

	return r
}

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("failed loading configuration: %v", err)
	}

	db, err := domain.InitDB(cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("failed initializing database: %v", err)
	}

	if err := domain.AutoMigrate(db); err != nil {
		log.Fatalf("failed running database migrations: %v", err)
	}

	if err := domain.SeedPlans(db); err != nil {
		log.Fatalf("failed seeding plans: %v", err)
	}

	log.Printf("Server listening on port %s (Port Range: %d-%d)", cfg.ServerPort, cfg.PortRangeMin, cfg.PortRangeMax)
	r := SetupRouter(db, cfg)
	if err := r.Run(":" + cfg.ServerPort); err != nil {
		log.Fatalf("server exited with error: %v", err)
	}
}
