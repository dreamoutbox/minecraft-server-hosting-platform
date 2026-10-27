package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config encapsulates runtime configuration settings for the platform orchestrator.
type Config struct {
	ServerPort          string
	DatabaseDSN         string
	PortRangeMin        int
	PortRangeMax        int
	JWTSecret           string
	BillingProvider     string
	StripeSecretKey     string
	StripeWebhookSecret string
}

// LoadFromEnv loads environment configuration and validates critical constraints.
// It inspects environment variables, applies sensible defaults for local development,
// and ensures global port range bounds are valid.
//
// Arguments:
//   - None (reads directly from process environment).
//
// Example usage:
//   cfg, err := config.LoadFromEnv()
//   if err != nil {
//       log.Fatalf("failed loading config: %v", err)
//   }
func LoadFromEnv() (*Config, error) {
	serverPort := getEnv("PORT", "8080")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "postgres")
	dbPassword := getEnv("DB_PASSWORD", "postgrespassword")
	dbName := getEnv("DB_NAME", "minecraft_hosting")
	dbSSL := getEnv("DB_SSLMODE", "disable")
	jwtSecret := getEnv("JWT_SECRET", "dev-platform-jwt-secret-key-change-in-prod")
	billingProvider := getEnv("BILLING_PROVIDER", "mock")
	stripeSecretKey := getEnv("STRIPE_SECRET_KEY", "")
	stripeWebhookSecret := getEnv("STRIPE_WEBHOOK_SECRET", "whsec_dev_mock_key")

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
		ServerPort:          serverPort,
		DatabaseDSN:         dsn,
		PortRangeMin:        minPort,
		PortRangeMax:        maxPort,
		JWTSecret:           jwtSecret,
		BillingProvider:     billingProvider,
		StripeSecretKey:     stripeSecretKey,
		StripeWebhookSecret: stripeWebhookSecret,
	}, nil
}

// getEnv retrieves an environment variable or returns the fallback value.
//
// Arguments:
//   - key: Name of the environment variable.
//   - fallback: Value to return if key is unset or empty.
//
// Example usage:
//   port := getEnv("PORT", "8080")
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
//   minPort, err := parseEnvInt("PORT_RANGE_MIN", 25565)
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
