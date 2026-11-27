package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadFromEnvDefaults(t *testing.T) {
	for _, key := range []string{
		"PORT",
		"PORT_RANGE_MIN",
		"PORT_RANGE_MAX",
		"SERVER_EXPIRED_DATA_RETENTION_DAYS",
		"CRON_MC_SERVER_DATA_BACKUP",
		"BACKUP_MAX_COUNT",
		"BACKUP_PRESIGN_TTL",
		"JWT_ACCESS_TTL",
	} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("failed unsetting %s: %v", key, err)
		}
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
	if cfg.RetentionDays != 7 {
		t.Errorf("expected default RetentionDays 7, got: %d", cfg.RetentionDays)
	}
	if cfg.CronMCServerDataBackup != "0 5 * * *" {
		t.Errorf("expected default CronMCServerDataBackup '0 5 * * *', got: %q", cfg.CronMCServerDataBackup)
	}
	if cfg.BackupMaxCount != 8 {
		t.Errorf("expected default BackupMaxCount 8, got: %d", cfg.BackupMaxCount)
	}
	if cfg.BackupPresignTTL != time.Hour {
		t.Errorf("expected default BackupPresignTTL 1h, got: %s", cfg.BackupPresignTTL)
	}
	if cfg.JWTAccessTTL != 15*time.Minute {
		t.Errorf("expected default JWTAccessTTL 15m, got: %s", cfg.JWTAccessTTL)
	}
	if cfg.DefaultWorkerKey != "dev-worker-key" {
		t.Errorf("expected default DefaultWorkerKey dev-worker-key, got: %s", cfg.DefaultWorkerKey)
	}
	if cfg.DefaultWorkerName != "worker1" {
		t.Errorf("expected default DefaultWorkerName worker1, got: %s", cfg.DefaultWorkerName)
	}
}

func TestLoadFromEnvBackupOverrides(t *testing.T) {
	t.Setenv("SERVER_EXPIRED_DATA_RETENTION_DAYS", "14")
	t.Setenv("CRON_MC_SERVER_DATA_BACKUP", "30 3 * * *")
	t.Setenv("BACKUP_MAX_COUNT", "12")
	t.Setenv("BACKUP_PRESIGN_TTL", "15m")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if cfg.RetentionDays != 14 {
		t.Errorf("expected RetentionDays 14, got %d", cfg.RetentionDays)
	}
	if cfg.CronMCServerDataBackup != "30 3 * * *" {
		t.Errorf("expected CronMCServerDataBackup '30 3 * * *', got %q", cfg.CronMCServerDataBackup)
	}
	if cfg.BackupMaxCount != 12 {
		t.Errorf("expected BackupMaxCount 12, got %d", cfg.BackupMaxCount)
	}
	if cfg.BackupPresignTTL != 15*time.Minute {
		t.Errorf("expected BackupPresignTTL 15m, got %s", cfg.BackupPresignTTL)
	}
}

func TestLoadFromEnvInvalidBackupConfig(t *testing.T) {
	t.Run("invalid cron", func(t *testing.T) {
		t.Setenv("CRON_MC_SERVER_DATA_BACKUP", "not-a-cron")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for invalid CRON_MC_SERVER_DATA_BACKUP")
		}
	})

	t.Run("non-positive backup max count", func(t *testing.T) {
		t.Setenv("BACKUP_MAX_COUNT", "0")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for BACKUP_MAX_COUNT=0")
		}
	})

	t.Run("non-numeric backup max count", func(t *testing.T) {
		t.Setenv("BACKUP_MAX_COUNT", "abc")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for BACKUP_MAX_COUNT=abc")
		}
	})

	t.Run("invalid presign ttl", func(t *testing.T) {
		t.Setenv("BACKUP_PRESIGN_TTL", "invalid")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for invalid BACKUP_PRESIGN_TTL")
		}
	})

	t.Run("non-positive presign ttl", func(t *testing.T) {
		t.Setenv("BACKUP_PRESIGN_TTL", "-5m")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for negative BACKUP_PRESIGN_TTL")
		}
	})
}

func TestLoadFromEnvInvalidRange(t *testing.T) {
	t.Setenv("PORT_RANGE_MIN", "25600")
	t.Setenv("PORT_RANGE_MAX", "25500")

	_, err := LoadFromEnv()
	if err == nil {
		t.Fatal("expected error when min >= max, got nil")
	}
}

func TestLoadFromEnvJWTAccessTTL(t *testing.T) {
	t.Run("valid override", func(t *testing.T) {
		t.Setenv("JWT_ACCESS_TTL", "30m")
		cfg, err := LoadFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.JWTAccessTTL != 30*time.Minute {
			t.Fatalf("expected 30m, got %s", cfg.JWTAccessTTL)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		t.Setenv("JWT_ACCESS_TTL", "bad")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for invalid JWT_ACCESS_TTL")
		}
	})

	t.Run("non-positive duration", func(t *testing.T) {
		t.Setenv("JWT_ACCESS_TTL", "0s")
		if _, err := LoadFromEnv(); err == nil {
			t.Fatal("expected error for zero JWT_ACCESS_TTL")
		}
	})
}
