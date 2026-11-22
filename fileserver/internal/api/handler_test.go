package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dreamoutbox/authkit"
	"github.com/dreamoutbox/fileserver/client"
	"github.com/dreamoutbox/fileserver/internal/store"
)

func setupAPITestServer(t *testing.T) (*httptest.Server, *store.Store, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "api.db")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	st := store.New(db, "/var/lib/mc-platform/mounts")

	apiKey := "test-node-api-key"
	jwtSecret := "test-fileserver-jwt-secret"

	r := gin.New()
	h := NewHandler(st, jwtSecret, 60*time.Second)
	h.RegisterRoutes(r, apiKey)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, st, apiKey, jwtSecret
}

func TestManagementAPIAuthAndLifecycle(t *testing.T) {
	srv, st, apiKey, jwtSecret := setupAPITestServer(t)
	ctx := context.Background()

	// 1. Reject missing or wrong API key
	badClient := client.New(srv.URL, "wrong-key")
	if _, err := badClient.EnsureUser(ctx, 1, "u@example.com", nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 for bad api key, got %v", err)
	}

	reqNoAuth, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/users", nil)
	respNoAuth, err := http.DefaultClient.Do(reqNoAuth)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer header, got %d", respNoAuth.StatusCode)
	}

	c := client.New(srv.URL, apiKey)

	// 2. Login token for unknown user returns 404
	if _, err := c.CreateLoginToken(ctx, 999); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 for missing user, got %v", err)
	}

	// 3. EnsureUser idempotent + UID/GID persistence and validation
	uid1000, gid1000 := 1000, 1000
	neg := -1
	if _, err := c.EnsureUser(ctx, 7, "alice@example.com", &neg, &gid1000); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 for negative uid, got %v", err)
	}
	if _, err := c.EnsureUser(ctx, 7, "alice@example.com", &uid1000, &neg); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 for negative gid, got %v", err)
	}

	u1, err := c.EnsureUser(ctx, 7, "alice@example.com", &uid1000, &gid1000)
	if err != nil {
		t.Fatalf("EnsureUser 1 failed: %v", err)
	}
	if !u1.Created || u1.ID != 7 || u1.UID == nil || *u1.UID != 1000 || u1.GID == nil || *u1.GID != 1000 {
		t.Fatalf("expected created=true id=7 uid=1000 gid=1000, got %+v", u1)
	}
	u2, err := c.EnsureUser(ctx, 7, "alice@example.com", nil, nil)
	if err != nil {
		t.Fatalf("EnsureUser 2 failed: %v", err)
	}
	if u2.Created || u2.ID != 7 || u2.UID == nil || *u2.UID != 1000 || u2.GID == nil || *u2.GID != 1000 {
		t.Fatalf("expected created=false id=7 preserved uid=1000 gid=1000, got %+v", u2)
	}

	// 4. Allow / Disallow / RevokeServer
	if err := c.Allow(ctx, 7, 12); err != nil {
		t.Fatalf("Allow failed: %v", err)
	}
	if _, _, err := st.FindPermissionForPath(ctx, 7, "/var/lib/mc-platform/mounts/12"); err != nil {
		t.Fatalf("expected permission present after Allow: %v", err)
	}

	if err := c.Disallow(ctx, 7, 12); err != nil {
		t.Fatalf("Disallow failed: %v", err)
	}
	if _, _, err := st.FindPermissionForPath(ctx, 7, "/var/lib/mc-platform/mounts/12"); err == nil {
		t.Fatal("expected permission removed after Disallow")
	}

	if err := c.Allow(ctx, 7, 15); err != nil {
		t.Fatalf("Allow 15 failed: %v", err)
	}
	if err := c.RevokeServer(ctx, 15); err != nil {
		t.Fatalf("RevokeServer failed: %v", err)
	}
	if _, _, err := st.FindPermissionForPath(ctx, 7, "/var/lib/mc-platform/mounts/15"); err == nil {
		t.Fatal("expected permission removed after RevokeServer")
	}

	// 5. Login token issuance and validation
	tokResp, err := c.CreateLoginToken(ctx, 7)
	if err != nil {
		t.Fatalf("CreateLoginToken failed: %v", err)
	}
	claims, err := authkit.ParseAndValidateToken(tokResp.Token, jwtSecret, authkit.TokenTypeLogin)
	if err != nil {
		t.Fatalf("issued login token failed validation: %v", err)
	}
	if claims.UserID != 7 || claims.Email != "alice@example.com" || claims.ID == "" {
		t.Errorf("unexpected login token claims: %+v", claims)
	}
}
