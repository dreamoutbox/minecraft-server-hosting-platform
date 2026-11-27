package config

import (
	"testing"
	"time"
)

func TestLoadFromEnvDefaults(t *testing.T) {
	t.Setenv("FILESERVER_PORT", "")
	t.Setenv("FILESERVER_MAX_UPLOAD_MB", "")
	t.Setenv("FILESERVER_DAILY_QUOTA_MB", "")
	t.Setenv("FILESERVER_SESSION_TTL", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if cfg.Port != "9100" {
		t.Errorf("expected default port 9100, got %s", cfg.Port)
	}
	if cfg.LoginTokenTTL != 60*time.Second {
		t.Errorf("expected 60s login token ttl, got %v", cfg.LoginTokenTTL)
	}
	if cfg.MaxUploadBytes != 2048*1024*1024 {
		t.Errorf("expected 2048MB upload limit, got %d", cfg.MaxUploadBytes)
	}
	if cfg.DailyQuotaBytes != 5120*1024*1024 {
		t.Errorf("expected 5120MB daily quota default, got %d", cfg.DailyQuotaBytes)
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Errorf("expected 24h session ttl default, got %s", cfg.SessionTTL)
	}
}

func TestLoadFromEnvDailyQuota(t *testing.T) {
	t.Setenv("FILESERVER_DAILY_QUOTA_MB", "1024")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("expected nil error for override, got: %v", err)
	}
	if cfg.DailyQuotaBytes != 1024*1024*1024 {
		t.Errorf("expected 1024MB daily quota, got %d", cfg.DailyQuotaBytes)
	}

	t.Setenv("FILESERVER_DAILY_QUOTA_MB", "0")
	cfgZero, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("expected 0 (unlimited) to be valid, got: %v", err)
	}
	if cfgZero.DailyQuotaBytes != 0 {
		t.Errorf("expected 0 bytes for unlimited quota, got %d", cfgZero.DailyQuotaBytes)
	}

	t.Setenv("FILESERVER_DAILY_QUOTA_MB", "-5")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for negative daily quota")
	}
}

func TestLoadFromEnvInvalidUploadLimit(t *testing.T) {
	t.Setenv("FILESERVER_MAX_UPLOAD_MB", "0")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for zero upload limit")
	}
}

func TestLoadFromEnvSessionTTL(t *testing.T) {
	t.Setenv("FILESERVER_SESSION_TTL", "12h")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("expected 12h session ttl, got %s", cfg.SessionTTL)
	}

	t.Setenv("FILESERVER_SESSION_TTL", "-5m")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for negative session ttl")
	}
}
