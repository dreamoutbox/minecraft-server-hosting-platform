package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/fileserver/internal/config"
	"github.com/dreamoutbox/fileserver/internal/store"
)

func TestHealthEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	db, err := store.OpenSQLite(filepath.Join(tmp, "health.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	r := SetupRouter(cfg, store.New(db, tmp))
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
