package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB establishes a connection to PostgreSQL using the provided DSN.
// It initializes connection pooling parameters suitable for platform traffic.
//
// Arguments:
//   - dsn: PostgreSQL connection string formatted with host, user, dbname, etc.
//
// Example usage:
//
//	db, err := domain.InitDB("host=localhost user=postgres dbname=minecraft_hosting sslmode=disable")
//	if err != nil {
//	    log.Fatalf("failed to connect database: %v", err)
//	}
func InitDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed opening postgres connection: %w", err)
	}
	return db, nil
}

// AutoMigrate applies schema changes for platform models.
// It creates tables and indexes if they do not exist.
//
// Arguments:
//   - db: Active GORM database instance.
//
// Example usage:
//
//	if err := domain.AutoMigrate(db); err != nil {
//	    log.Fatalf("database migration failed: %v", err)
//	}
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&User{},
		&Plan{},
		&Host{},
		&Server{},
		&Subscription{},
		&PaymentRecord{},
		&BackupRecord{},
	)
}

// SeedPlans seeds default hardware plan tiers into the database if missing.
// Tiers: Small (1 CPU / 1GB RAM / 10GB disk), Medium (2 CPU / 2GB RAM / 20GB disk), Large (4 CPU / 4GB RAM / 40GB disk).
//
// Arguments:
//   - db: Active GORM database instance.
//
// Example usage:
//
//	if err := domain.SeedPlans(db); err != nil {
//	    log.Fatalf("failed seeding plans: %v", err)
//	}
func SeedPlans(db *gorm.DB) error {
	defaultPlans := []Plan{
		{
			Name:        "Small",
			CPULimit:    1,
			MemLimitMB:  1024,
			DiskLimitGB: 10,
			PriceCents:  500, // $5.00/mo
		},
		{
			Name:        "Medium",
			CPULimit:    2,
			MemLimitMB:  2048,
			DiskLimitGB: 20,
			PriceCents:  1000, // $10.00/mo
		},
		{
			Name:        "Large",
			CPULimit:    4,
			MemLimitMB:  4096,
			DiskLimitGB: 40,
			PriceCents:  2000, // $20.00/mo
		},
	}

	for _, plan := range defaultPlans {
		var existing Plan
		err := db.Where("name = ?", plan.Name).First(&existing).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if createErr := db.Create(&plan).Error; createErr != nil {
					return fmt.Errorf("failed creating plan %s: %w", plan.Name, createErr)
				}
			} else {
				return fmt.Errorf("failed checking plan %s: %w", plan.Name, err)
			}
		}
	}

	return nil
}

// SeedDefaultHost ensures the primary worker node is registered and active in the cluster.
//
// Arguments:
//   - db: Active GORM database handle.
//   - hostname: Display hostname of the node.
//   - workerBaseURL: HTTP root URL of the worker daemon.
//   - apiKey: Worker authentication key.
//   - maxServers: Capacity limit for the host.
//
// Example usage:
//   err := domain.SeedDefaultHost(db, "compose-worker", "http://worker:9090", "dev-worker-key", 20)
func SeedDefaultHost(db *gorm.DB, hostname, workerBaseURL, apiKey string, maxServers int) error {
	if workerBaseURL == "" {
		return nil
	}
	hashBytes := sha256.Sum256([]byte(apiKey))
	hashHex := hex.EncodeToString(hashBytes[:])

	var host Host
	err := db.Where("worker_base_url = ?", workerBaseURL).First(&host).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			host = Host{
				Hostname:      hostname,
				WorkerBaseURL: workerBaseURL,
				APIKey:        apiKey,
				APIKeyHash:    hashHex,
				MaxServers:    maxServers,
				Status:        HostStatusActive,
			}
			return db.Create(&host).Error
		}
		return err
	}

	host.Status = HostStatusActive
	host.APIKey = apiKey
	host.APIKeyHash = hashHex
	return db.Save(&host).Error
}

