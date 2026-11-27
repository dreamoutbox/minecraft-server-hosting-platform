package main

import (
	"context"
	"log"
	"net/http"
	"strings"

	authhandler "github.com/dreamoutbox/authkit/handler"
	"github.com/dreamoutbox/authkit/session"
	fsclient "github.com/dreamoutbox/fileserver/client"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/billing"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/config"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/dashboard"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/jobs"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/middleware"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/storage"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

// SetupRouter initializes HTTP routes and middleware for the orchestrator API and dashboard.
//
// Arguments:
//   - db: Active database handle for data layer interactions.
//   - cfg: Loaded runtime configuration containing ports and limits.
//
// Example usage:
//
//	router := SetupRouter(db, cfg)
//	router.Run(":8080")
func SetupRouter(db *gorm.DB, cfg *config.Config) *gin.Engine {
	r, _ := buildRouter(db, cfg)
	return r
}

func buildRouter(db *gorm.DB, cfg *config.Config) (*gin.Engine, *jobs.Service) {
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

	authHandler := authhandler.NewHandler(db, cfg.JWTSecret)
	authHandler.SetAccessTokenTTL(cfg.JWTAccessTTL)
	authGroup := r.Group("/api/v1/auth")
	authGroup.Use(middleware.RateLimitMiddleware(10, 20))
	authHandler.RegisterRoutes(authGroup)

	workerFactory := func(baseURL string, apiKey string) worker.Client {
		// When running inside containers and a default worker URL is configured,
		// map loopback addresses (127.0.0.1 / localhost) to the reachable compose worker service.
		if cfg.DefaultWorkerURL != "" && (strings.Contains(baseURL, "127.0.0.1:9090") || strings.Contains(baseURL, "localhost:9090")) {
			baseURL = cfg.DefaultWorkerURL
			if apiKey == "" {
				apiKey = cfg.DefaultWorkerKey
			}
		}
		return worker.NewClient(baseURL, apiKey)
	}

	scheduler := hosting.NewLeastLoadedScheduler(db)
	portAllocator := hosting.NewPortAllocator(db, cfg.PortRangeMin, cfg.PortRangeMax)
	hostingService := hosting.NewService(db, scheduler, portAllocator, workerFactory)
	hostingService.SetFileServerClientFactory(func(baseURL string, apiKey string) fsclient.Client {
		if cfg.DefaultFileServerInternalURL != "" && (strings.Contains(baseURL, "127.0.0.1:9100") || strings.Contains(baseURL, "localhost:9100")) {
			baseURL = cfg.DefaultFileServerInternalURL
			if apiKey == "" {
				apiKey = cfg.DefaultWorkerKey
			}
		}
		return fsclient.New(baseURL, apiKey)
	})
	hostingHandler := hosting.NewHandler(hostingService)
	hostingHandler.RegisterRoutes(r, cfg.JWTSecret)

	storageProvider, err := storage.NewStorageProviderFromEnv(context.Background())
	if err != nil {
		log.Printf("[WARN] using in-memory storage fallback: %v", err)
		storageProvider = storage.NewMemoryStorageProvider()
	}

	backupService := backup.NewService(db, storageProvider, workerFactory)
	backupService.SetMaxBackups(cfg.BackupMaxCount)
	backupService.SetPresignTTL(cfg.BackupPresignTTL)
	backupHandler := backup.NewHandler(backupService)
	backupHandler.RegisterRoutes(r, cfg.JWTSecret)

	jobService := jobs.NewService(db, hostingService, backupService)
	hostingHandler.SetJobQueue(jobService)
	backupHandler.SetJobQueue(jobService)
	jobHandler := jobs.NewHandler(jobService)
	jobHandler.RegisterRoutes(r, cfg.JWTSecret)

	migrationService := hosting.NewMigrationService(hostingService, backupService, storageProvider, workerFactory)
	hostingHandler.SetMigrationService(migrationService)

	var paymentProvider billing.PaymentProvider
	if cfg.BillingProvider == "stripe" && cfg.StripeSecretKey != "" {
		paymentProvider = billing.NewStripePaymentProvider(cfg.StripeSecretKey)
	} else {
		paymentProvider = billing.NewMockPaymentProvider()
	}
	billingService := billing.NewService(db, paymentProvider, cfg.StripeWebhookSecret)
	billingService.SetJobQueue(jobService)
	billingService.SetAppBaseURL(cfg.AppBaseURL)
	billingService.SetRetentionDays(cfg.RetentionDays)
	billingHandler := billing.NewHandler(billingService)
	billingHandler.RegisterRoutes(r, cfg.JWTSecret)

	cookieCfg := session.DefaultCookieConfig(session.DefaultCookieName, cfg.SessionTTL, cfg.CookieSecure)
	authsessions := session.NewManager(db, cookieCfg)
	dashboardService := dashboard.NewService(
		db,
		hostingService,
		backupService,
		billingService,
		migrationService,
		jobService,
		authsessions,
	)
	dashboardService.RegisterRoutes(r)

	return r, jobService
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

	if err := domain.SeedDefaultHost(
		db,
		cfg.DefaultWorkerName,
		cfg.DefaultWorkerURL,
		cfg.DefaultFileServerURL,
		cfg.DefaultWorkerPublicDomain,
		cfg.DefaultWorkerPublicIP,
		cfg.DefaultWorkerKey,
		cfg.DefaultWorkerMaxServers,
	); err != nil {
		log.Printf("[WARN] failed seeding default worker host: %v", err)
	}

	r, _ := buildRouter(db, cfg)

	log.Printf("Server listening on port %s (Port Range: %d-%d)", cfg.ServerPort, cfg.PortRangeMin, cfg.PortRangeMax)
	if err := r.Run(":" + cfg.ServerPort); err != nil {
		log.Fatalf("server exited with error: %v", err)
	}
}
