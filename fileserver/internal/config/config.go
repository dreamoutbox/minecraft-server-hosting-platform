package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultPort          = "9100"
	defaultSQLitePath    = "/var/lib/mc-platform/fileserver/fileserver.db"
	defaultMountsDir     = "/var/lib/mc-platform/mounts"
	defaultAPIKey        = "dev-worker-key"
	defaultJWTSecret     = "dev-fileserver-jwt-secret-change-in-prod"
	defaultMaxUploadMB   = 2048
	defaultDailyQuotaMB  = 5120
	defaultLoginTokenTTL = 60 * time.Second
	defaultSessionTTL    = 24 * time.Hour
)

// Config holds runtime settings for the node fileserver daemon.
type Config struct {
	Port            string
	SQLitePath      string
	MountsDir       string
	APIKey          string
	JWTSecret       string
	MaxUploadBytes  int64
	DailyQuotaBytes int64
	CookieSecure    bool
	LoginTokenTTL   time.Duration
	SessionTTL      time.Duration
}

// LoadFromEnv reads fileserver configuration from environment variables with safe dev defaults.
//
// Arguments:
//   - None (reads directly from process environment).
//
// Example usage:
//
//	cfg, err := config.LoadFromEnv()
func LoadFromEnv() (*Config, error) {
	maxUploadMB, err := parsePositiveInt("FILESERVER_MAX_UPLOAD_MB", defaultMaxUploadMB)
	if err != nil {
		return nil, fmt.Errorf("invalid FILESERVER_MAX_UPLOAD_MB: %w", err)
	}
	dailyQuotaMB, err := parseNonNegativeInt("FILESERVER_DAILY_QUOTA_MB", defaultDailyQuotaMB)
	if err != nil {
		return nil, fmt.Errorf("invalid FILESERVER_DAILY_QUOTA_MB: %w", err)
	}
	sessionTTL, err := parsePositiveDuration("FILESERVER_SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return nil, fmt.Errorf("invalid FILESERVER_SESSION_TTL: %w", err)
	}

	return &Config{
		Port:            getEnv("FILESERVER_PORT", defaultPort),
		SQLitePath:      getEnv("FILESERVER_SQLITE_PATH", defaultSQLitePath),
		MountsDir:       getEnv("FILESERVER_MOUNTS_DIR", defaultMountsDir),
		APIKey:          getEnv("FILESERVER_API_KEY", defaultAPIKey),
		JWTSecret:       getEnv("FILESERVER_JWT_SECRET", defaultJWTSecret),
		MaxUploadBytes:  int64(maxUploadMB) * 1024 * 1024,
		DailyQuotaBytes: int64(dailyQuotaMB) * 1024 * 1024,
		CookieSecure:    os.Getenv("FILESERVER_COOKIE_SECURE") == "true",
		LoginTokenTTL:   defaultLoginTokenTTL,
		SessionTTL:      sessionTTL,
	}, nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func parsePositiveInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("must be a positive integer, got %q", raw)
	}
	return parsed, nil
}

func parseNonNegativeInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("must be a non-negative integer, got %q", raw)
	}
	return parsed, nil
}

func parsePositiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("must be a positive duration, got %q", raw)
	}
	return parsed, nil
}
