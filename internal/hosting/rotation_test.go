package hosting

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/domain"
)

func TestRotateHostAPIKey(t *testing.T) {
	r, service, _, _, token := setupHostingTestEnv(t)
	if r == nil {
		return
	}

	ctx := context.Background()

	// 1. Register host
	host, originalKey, err := service.RegisterHost(ctx, "rotate-node.internal", "http://127.0.0.1:9090", 10)
	if err != nil {
		t.Fatalf("failed registering host: %v", err)
	}

	// 2. Rotate host key via service
	newKey, err := service.RotateHostAPIKey(ctx, host.ID)
	if err != nil {
		t.Fatalf("failed rotating key: %v", err)
	}

	if newKey == originalKey {
		t.Fatal("expected rotated key to differ from original key")
	}

	// 3. Verify updated in database
	var reloaded domain.Host
	if err := service.db.First(&reloaded, host.ID).Error; err != nil {
		t.Fatalf("failed reloading host: %v", err)
	}
	if reloaded.APIKey != newKey {
		t.Errorf("host APIKey not updated in DB: got %s, want %s", reloaded.APIKey, newKey)
	}

	// 4. Test HTTP endpoint: POST /api/v1/hosts/:id/rotate-key
	wRotate := httptest.NewRecorder()
	reqRotate, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/hosts/%d/rotate-key", host.ID), nil)
	reqRotate.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(wRotate, reqRotate)
	if wRotate.Code != http.StatusOK {
		t.Errorf("expected rotate-key endpoint 200, got %d", wRotate.Code)
	}
}
