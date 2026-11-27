package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/config"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/domain"
)

func TestHealthCheckRoute(t *testing.T) {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}

	db, err := domain.InitDB(cfg.DatabaseDSN)
	if err != nil {
		t.Skipf("skipping test: database not reachable: %v", err)
		return
	}

	router := SetupRouter(db, cfg)

	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing response json: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", resp["status"])
	}
}
