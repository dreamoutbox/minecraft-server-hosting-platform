package main

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/auth"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/config"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/middleware"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/storage"
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
	r.Use(middleware.RequestIDMiddleware())

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

	authHandler := auth.NewHandler(db, cfg.JWTSecret)
	authGroup := r.Group("/api/v1/auth")
	authGroup.Use(middleware.RateLimitMiddleware(10, 20))
	authHandler.RegisterRoutes(authGroup)

	scheduler := hosting.NewLeastLoadedScheduler(db)
	portAllocator := hosting.NewPortAllocator(db, cfg.PortRangeMin, cfg.PortRangeMax)
	hostingService := hosting.NewService(db, scheduler, portAllocator, nil)
	hostingHandler := hosting.NewHandler(hostingService)
	hostingHandler.RegisterRoutes(r, cfg.JWTSecret)

	storageProvider, err := storage.NewStorageProviderFromEnv(context.Background())
	if err != nil {
		log.Printf("[WARN] using in-memory storage fallback: %v", err)
		storageProvider = storage.NewMemoryStorageProvider()
	}

	backupService := backup.NewService(db, storageProvider, nil)
	backupHandler := backup.NewHandler(backupService)
	backupHandler.RegisterRoutes(r, cfg.JWTSecret)

	migrationService := hosting.NewMigrationService(hostingService, backupService, storageProvider, nil)
	hostingHandler.SetMigrationService(migrationService)

	var paymentProvider billing.PaymentProvider
	if cfg.BillingProvider == "stripe" && cfg.StripeSecretKey != "" {
		paymentProvider = billing.NewStripePaymentProvider(cfg.StripeSecretKey)
	} else {
		paymentProvider = billing.NewMockPaymentProvider()
	}
	billingService := billing.NewService(db, paymentProvider, hostingService, cfg.StripeWebhookSecret)
	billingHandler := billing.NewHandler(billingService)
	billingHandler.RegisterRoutes(r, cfg.JWTSecret)

	dashboardService := dashboard.NewService(
		db,
		hostingService,
		backupService,
		billingService,
		migrationService,
		storageProvider,
		cfg.JWTSecret,
	)
	dashboardService.RegisterRoutes(r)

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
