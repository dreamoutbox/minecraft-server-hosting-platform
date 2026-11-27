package main

import (
	"context"
	"log"
	"os/signal"
	"strings"
	"syscall"
	"time"

	fsclient "github.com/dreamoutbox/fileserver/client"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/backup"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/config"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/hosting"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/jobs"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/storage"
	worker "github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
)

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

	workerFactory := func(baseURL string, apiKey string) worker.Client {
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

	storageProvider, err := storage.NewStorageProviderFromEnv(context.Background())
	if err != nil {
		log.Printf("[WARN] using in-memory storage fallback: %v", err)
		storageProvider = storage.NewMemoryStorageProvider()
	}

	backupService := backup.NewService(db, storageProvider, workerFactory)
	backupService.SetMaxBackups(cfg.BackupMaxCount)
	backupService.SetPresignTTL(cfg.BackupPresignTTL)
	jobService := jobs.NewService(db, hostingService, backupService)

	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSig()

	stopQueue := jobService.Start(sigCtx)
	defer stopQueue()

	stopLifecycle := jobService.StartLifecycleCron(sigCtx, time.Minute, cfg.RetentionDays)
	defer stopLifecycle()

	stopBackupCron, err := jobService.StartImageBackupCron(sigCtx, cfg.CronMCServerDataBackup)
	if err != nil {
		log.Fatalf("failed starting image backup cron: %v", err)
	}
	defer stopBackupCron()

	log.Printf(
		"Background jobworker started (retention=%d days, backup_cron=%q, backup_max_count=%d, presign_ttl=%s)",
		cfg.RetentionDays,
		cfg.CronMCServerDataBackup,
		cfg.BackupMaxCount,
		cfg.BackupPresignTTL,
	)
	<-sigCtx.Done()
	log.Println("Background jobworker shutting down...")
}
