package config

import (
	"os"
	"testing"
)

func TestLoadFromEnvDefaults(t *testing.T) {
	if err := os.Unsetenv("PORT"); err != nil {
		t.Fatalf("failed unsetting PORT: %v", err)
	}
	if err := os.Unsetenv("PORT_RANGE_MIN"); err != nil {
		t.Fatalf("failed unsetting PORT_RANGE_MIN: %v", err)
	}
	if err := os.Unsetenv("PORT_RANGE_MAX"); err != nil {
		t.Fatalf("failed unsetting PORT_RANGE_MAX: %v", err)
	}

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if cfg.ServerPort != "8080" {
		t.Errorf("expected default ServerPort 8080, got: %s", cfg.ServerPort)
	}
	if cfg.PortRangeMin != 25565 {
		t.Errorf("expected default PortRangeMin 25565, got: %d", cfg.PortRangeMin)
	}
	if cfg.PortRangeMax != 25600 {
		t.Errorf("expected default PortRangeMax 25600, got: %d", cfg.PortRangeMax)
	}
}

func TestLoadFromEnvInvalidRange(t *testing.T) {
	t.Setenv("PORT_RANGE_MIN", "25600")
	t.Setenv("PORT_RANGE_MAX", "25500")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("expected error when min >= max, got nil")
	}
}
