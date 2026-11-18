package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"

	"github.com/dreamoutbox/authkit"
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
		&ServerOperationLog{},
		&ServerJob{},
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
//   - fileServerBaseURL: Public HTTP root URL of the node fileserver daemon.
//   - publicDomain: Public DNS domain players use to connect to servers on this node.
//   - publicIP: Public IPv4/IPv6 address of this node.
//   - apiKey: Worker authentication key.
//   - maxServers: Capacity limit for the host.
//
// Example usage:
//
//	err := domain.SeedDefaultHost(db, "compose-worker", "http://worker:9090", "http://localhost:9100", "localhost", "127.0.0.1", "dev-worker-key", 20)
func SeedDefaultHost(db *gorm.DB, hostname, workerBaseURL, fileServerBaseURL, publicDomain, publicIP, apiKey string, maxServers int) error {
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
				Hostname:          hostname,
				PublicDomain:      publicDomain,
				PublicIP:          publicIP,
				WorkerBaseURL:     workerBaseURL,
				FileServerBaseURL: fileServerBaseURL,
				APIKey:            apiKey,
				APIKeyHash:        hashHex,
				MaxServers:        maxServers,
				Status:            HostStatusActive,
			}
			return db.Create(&host).Error
		}
		return err
	}

	host.Status = HostStatusActive
	host.APIKey = apiKey
	host.APIKeyHash = hashHex
	if fileServerBaseURL != "" {
		host.FileServerBaseURL = fileServerBaseURL
	}
	if publicDomain != "" {
		host.PublicDomain = publicDomain
	}
	if publicIP != "" {
		host.PublicIP = publicIP
	}
	return db.Save(&host).Error
}

// SeedUserSpec defines a user account to seed into the database.
type SeedUserSpec struct {
	Email    string
	Password string
	Role     UserRole
}

// SeedUsers inserts users into the database if they do not already exist.
//
// Arguments:
//   - db: Active GORM database instance.
//   - specs: Slice of user specifications to seed.
//
// Example usage:
//
//	if err := domain.SeedUsers(db, defaultDevUsers); err != nil {
//	    log.Fatalf("failed seeding users: %v", err)
//	}
func SeedUsers(db *gorm.DB, specs []SeedUserSpec) error {
	for _, spec := range specs {
		var existing User
		err := db.Where("email = ?", spec.Email).First(&existing).Error
		if err == nil {
			log.Printf("user %s already exists, skipping", spec.Email)
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		hashedPassword, err := authkit.HashPassword(spec.Password)
		if err != nil {
			return err
		}

		user := User{
			Email:        spec.Email,
			PasswordHash: hashedPassword,
			Role:         spec.Role,
		}
		if err := db.Create(&user).Error; err != nil {
			return err
		}
		log.Printf("seeded user %s (role=%s)", spec.Email, spec.Role)
	}
	return nil
}
