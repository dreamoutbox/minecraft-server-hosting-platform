package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	defaultServerExpiredRetentionDays = 7
	defaultCronMCServerDataBackup     = "0 5 * * *"
	defaultBackupMaxCount             = 8
	defaultBackupPresignTTL           = time.Hour
	defaultJWTAccessTTL               = 15 * time.Minute
)

// Config encapsulates runtime configuration settings for the platform orchestrator.
type Config struct {
	ServerPort                   string
	DatabaseDSN                  string
	PortRangeMin                 int
	PortRangeMax                 int
	JWTSecret                    string
	JWTAccessTTL                 time.Duration
	AppBaseURL                   string
	RetentionDays                int
	CronMCServerDataBackup       string
	BackupMaxCount               int
	BackupPresignTTL             time.Duration
	BillingProvider              string
	StripeSecretKey              string
	StripeWebhookSecret          string
	DefaultWorkerURL             string
	DefaultWorkerKey             string
	DefaultWorkerName            string
	DefaultWorkerPublicDomain    string
	DefaultWorkerPublicIP        string
	DefaultWorkerMaxServers      int
	DefaultFileServerURL         string
	DefaultFileServerInternalURL string
}

// LoadFromEnv loads environment configuration and validates critical constraints.
// It inspects environment variables, applies sensible defaults for local development,
// and ensures global port range bounds are valid.
//
// Arguments:
//   - None (reads directly from process environment).
//
// Example usage:
//
//	cfg, err := config.LoadFromEnv()
//	if err != nil {
//	    log.Fatalf("failed loading config: %v", err)
//	}
func LoadFromEnv() (*Config, error) {
	serverPort := getEnv("PORT", "8080")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "postgres")
	dbPassword := getEnv("DB_PASSWORD", "postgrespassword")
	dbName := getEnv("DB_NAME", "minecraft_hosting")
	dbSSL := getEnv("DB_SSLMODE", "disable")
	jwtSecret := getEnv("JWT_SECRET", "dev-platform-jwt-secret-key-change-in-prod")
	jwtAccessTTL, err := parseEnvDuration("JWT_ACCESS_TTL", defaultJWTAccessTTL)
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_ACCESS_TTL: %w", err)
	}
	if jwtAccessTTL <= 0 {
		return nil, fmt.Errorf("JWT_ACCESS_TTL must be positive: got %s", jwtAccessTTL)
	}
	appBaseURL := getEnv("APP_BASE_URL", "http://localhost:8080")
	retentionDays, err := parseEnvInt("SERVER_EXPIRED_DATA_RETENTION_DAYS", defaultServerExpiredRetentionDays)
	if err != nil || retentionDays <= 0 {
		retentionDays = defaultServerExpiredRetentionDays
	}

	cronMCServerDataBackup := getEnv("CRON_MC_SERVER_DATA_BACKUP", defaultCronMCServerDataBackup)
	if _, err := cron.ParseStandard(cronMCServerDataBackup); err != nil {
		return nil, fmt.Errorf("invalid CRON_MC_SERVER_DATA_BACKUP (%q): %w", cronMCServerDataBackup, err)
	}

	backupMaxCount, err := parseEnvInt("BACKUP_MAX_COUNT", defaultBackupMaxCount)
	if err != nil {
		return nil, fmt.Errorf("invalid BACKUP_MAX_COUNT: %w", err)
	}
	if backupMaxCount <= 0 {
		return nil, fmt.Errorf("BACKUP_MAX_COUNT must be positive: got %d", backupMaxCount)
	}

	backupPresignTTL, err := parseEnvDuration("BACKUP_PRESIGN_TTL", defaultBackupPresignTTL)
	if err != nil {
		return nil, fmt.Errorf("invalid BACKUP_PRESIGN_TTL: %w", err)
	}
	if backupPresignTTL <= 0 {
		return nil, fmt.Errorf("BACKUP_PRESIGN_TTL must be positive: got %s", backupPresignTTL)
	}
	billingProvider := getEnv("BILLING_PROVIDER", "")
	stripeSecretKey := getEnv("STRIPE_SECRET_KEY", "")
	if billingProvider == "" {
		if stripeSecretKey != "" {
			billingProvider = "stripe"
		} else {
			billingProvider = "mock"
		}
	}
	stripeWebhookSecret := getEnv("STRIPE_WEBHOOK_SECRET", "whsec_dev_mock_key")
	defaultWorkerURL := getEnv("DEFAULT_WORKER_URL", "")
	defaultWorkerKey := getEnv("DEFAULT_WORKER_KEY", "dev-worker-key")
	defaultWorkerName := getEnv("DEFAULT_WORKER_NAME", "worker1")
	defaultWorkerPublicDomain := getEnv("DEFAULT_WORKER_PUBLIC_DOMAIN", "")
	defaultWorkerPublicIP := getEnv("DEFAULT_WORKER_PUBLIC_IP", "")
	defaultWorkerMaxServers, err := parseEnvInt("DEFAULT_WORKER_MAX_SERVERS", 0)
	if err != nil {
		return nil, fmt.Errorf("invalid DEFAULT_WORKER_MAX_SERVERS: %w", err)
	}
	if defaultWorkerMaxServers <= 0 {
		defaultWorkerMaxServers, err = parseEnvInt("WORKER_MAX_SERVERS", 20)
		if err != nil {
			return nil, fmt.Errorf("invalid WORKER_MAX_SERVERS: %w", err)
		}
		if defaultWorkerMaxServers <= 0 {
			defaultWorkerMaxServers = 20
		}
	}
	defaultFileServerURL := getEnv("DEFAULT_FILESERVER_URL", "")
	defaultFileServerInternalURL := getEnv("DEFAULT_FILESERVER_INTERNAL_URL", "")

	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		dbHost, dbUser, dbPassword, dbName, dbPort, dbSSL,
	)

	minPort, err := parseEnvInt("PORT_RANGE_MIN", 25565)
	if err != nil {
		return nil, fmt.Errorf("invalid PORT_RANGE_MIN: %w", err)
	}

	maxPort, err := parseEnvInt("PORT_RANGE_MAX", 25600)
	if err != nil {
		return nil, fmt.Errorf("invalid PORT_RANGE_MAX: %w", err)
	}

	if minPort <= 0 || maxPort <= 0 {
		return nil, fmt.Errorf("port ranges must be positive integers: got min=%d max=%d", minPort, maxPort)
	}

	if minPort >= maxPort {
		return nil, fmt.Errorf("PORT_RANGE_MIN (%d) must be less than PORT_RANGE_MAX (%d)", minPort, maxPort)
	}

	return &Config{
		ServerPort:                   serverPort,
		DatabaseDSN:                  dsn,
		PortRangeMin:                 minPort,
		PortRangeMax:                 maxPort,
		JWTSecret:                    jwtSecret,
		JWTAccessTTL:                 jwtAccessTTL,
		AppBaseURL:                   appBaseURL,
		RetentionDays:                retentionDays,
		CronMCServerDataBackup:       cronMCServerDataBackup,
		BackupMaxCount:               backupMaxCount,
		BackupPresignTTL:             backupPresignTTL,
		BillingProvider:              billingProvider,
		StripeSecretKey:              stripeSecretKey,
		StripeWebhookSecret:          stripeWebhookSecret,
		DefaultWorkerURL:             defaultWorkerURL,
		DefaultWorkerKey:             defaultWorkerKey,
		DefaultWorkerName:            defaultWorkerName,
		DefaultWorkerPublicDomain:    defaultWorkerPublicDomain,
		DefaultWorkerPublicIP:        defaultWorkerPublicIP,
		DefaultWorkerMaxServers:      defaultWorkerMaxServers,
		DefaultFileServerURL:         defaultFileServerURL,
		DefaultFileServerInternalURL: defaultFileServerInternalURL,
	}, nil
}

// getEnv retrieves an environment variable or returns the fallback value.
//
// Arguments:
//   - key: Name of the environment variable.
//   - fallback: Value to return if key is unset or empty.
//
// Example usage:
//
//	port := getEnv("PORT", "8080")
func getEnv(key string, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// parseEnvInt parses an integer from the environment or returns fallback.
//
// Arguments:
//   - key: Name of the environment variable.
//   - fallback: Default integer value to use if key is unset.
//
// Example usage:
//
//	minPort, err := parseEnvInt("PORT_RANGE_MIN", 25565)
func parseEnvInt(key string, fallback int) (int, error) {
	val := os.Getenv(key)
	if val == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

// parseEnvDuration parses a Go duration string from the environment or returns fallback.
//
// Arguments:
//   - key: Name of the environment variable.
//   - fallback: Default duration value to use if key is unset.
//
// Example usage:
//
//	ttl, err := parseEnvDuration("BACKUP_PRESIGN_TTL", time.Hour)
func parseEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	val := os.Getenv(key)
	if val == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(val)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}
