package config

import (
	"testing"
	"time"
)

func TestLoadFromEnvDefaults(t *testing.T) {
	t.Setenv("FILESERVER_PORT", "")
	t.Setenv("FILESERVER_MAX_UPLOAD_MB", "")

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
	if cfg.MaxUploadBytes != 100*1024*1024 {
		t.Errorf("expected 100MB upload limit, got %d", cfg.MaxUploadBytes)
	}
}

func TestLoadFromEnvInvalidUploadLimit(t *testing.T) {
	t.Setenv("FILESERVER_MAX_UPLOAD_MB", "0")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for zero upload limit")
	}
}
