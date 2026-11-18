package main

import (
	"log"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/config"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
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

	if err := domain.SeedPlans(db); err != nil {
		log.Fatalf("failed seeding plans: %v", err)
	}

	workerName := cfg.DefaultWorkerName
	if workerName == "" {
		workerName = "worker1"
	}
	workerURL := cfg.DefaultWorkerURL
	if workerURL == "" {
		workerURL = "http://localhost:9090"
	}
	workerKey := cfg.DefaultWorkerKey
	if workerKey == "" {
		workerKey = "dev-worker-key"
	}
	fileServerURL := cfg.DefaultFileServerURL
	if fileServerURL == "" {
		fileServerURL = "http://localhost:9100"
	}
	publicDomain := cfg.DefaultWorkerPublicDomain
	if publicDomain == "" {
		publicDomain = "localhost"
	}
	publicIP := cfg.DefaultWorkerPublicIP
	if publicIP == "" {
		publicIP = "127.0.0.1"
	}

	if err := domain.SeedDefaultHost(db, workerName, workerURL, fileServerURL, publicDomain, publicIP, workerKey, 20); err != nil {
		log.Fatalf("failed seeding default host: %v", err)
	}

	defaultDevUsers := []domain.SeedUserSpec{
		{
			Email:    "admin@example.com",
			Password: "password",
			Role:     domain.UserRoleAdmin,
		},
		{
			Email:    "staff@example.com",
			Password: "password",
			Role:     domain.UserRoleStaff,
		},
		{
			Email:    "user1@example.com",
			Password: "password",
			Role:     domain.UserRoleUser,
		},
	}

	if err := domain.SeedUsers(db, defaultDevUsers); err != nil {
		log.Fatalf("failed seeding users: %v", err)
	}

	log.Println("dev database user seeding complete")
}
