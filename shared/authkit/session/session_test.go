package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dreamoutbox/authkit"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSessionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed opening sqlite test db: %v", err)
	}
	if err := authkit.AutoMigrate(db); err != nil {
		t.Fatalf("authkit.AutoMigrate failed: %v", err)
	}
	return db
}

func TestStoreLifecycle(t *testing.T) {
	db := setupSessionTestDB(t)
	if !db.Migrator().HasTable(&Session{}) {
		t.Fatal("expected sessions table to be created by authkit.AutoMigrate")
	}

	store := NewStore(db, time.Hour)
	ctx := context.Background()

	// Invalid inputs rejected.
	if _, _, err := store.Create(ctx, 0, time.Hour); !errors.Is(err, ErrInvalidUserID) {
		t.Fatalf("expected ErrInvalidUserID, got %v", err)
	}
	if _, _, err := store.Create(ctx, 1, -time.Minute); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("expected ErrInvalidTTL, got %v", err)
	}

	// Create and validate session.
	rawToken, sess, err := store.Create(ctx, 42, 0)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if rawToken == "" || sess.TokenHash == "" || sess.TokenHash == rawToken {
		t.Fatalf("expected opaque raw token and distinct stored hash")
	}

	loaded, err := store.GetValid(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetValid failed: %v", err)
	}
	if loaded.UserID != 42 {
		t.Fatalf("expected userID 42, got %d", loaded.UserID)
	}

	// Delete revokes session immediately.
	if err := store.Delete(ctx, rawToken); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.GetValid(ctx, rawToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected ErrInvalidSession after delete, got %v", err)
	}

	// Expired session is rejected and lazily cleaned up.
	expToken, expSess, err := store.Create(ctx, 42, time.Hour)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	db.Model(&Session{}).Where("token_hash = ?", expSess.TokenHash).Update("expires_at", time.Now().UTC().Add(-time.Minute))
	if _, err := store.GetValid(ctx, expToken); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}

	// PruneExpired removes only expired rows.
	activeTok, _, err := store.Create(ctx, 42, time.Hour)
	if err != nil {
		t.Fatalf("Create active failed: %v", err)
	}
	staleTok, staleSess, err := store.Create(ctx, 42, time.Hour)
	if err != nil {
		t.Fatalf("Create stale failed: %v", err)
	}
	db.Model(&Session{}).Where("token_hash = ?", staleSess.TokenHash).Update("expires_at", time.Now().UTC().Add(-time.Minute))
	removed, err := store.PruneExpired(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("expected PruneExpired to remove 1 row, got (%d, %v)", removed, err)
	}
	if _, err := store.GetValid(ctx, activeTok); err != nil {
		t.Fatalf("expected active session to remain valid: %v", err)
	}
	if _, err := store.GetValid(ctx, staleTok); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected pruned session to be invalid, got %v", err)
	}

	// DeleteByUserID revokes all sessions for user.
	if err := store.DeleteByUserID(ctx, 42); err != nil {
		t.Fatalf("DeleteByUserID failed: %v", err)
	}
	if _, err := store.GetValid(ctx, activeTok); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected ErrInvalidSession after DeleteByUserID, got %v", err)
	}
}

func TestCookieFlagsConsistentOnSetAndClear(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := DefaultCookieConfig("sid", 2*time.Hour, true)

	r := gin.New()
	r.GET("/set", func(c *gin.Context) {
		SetCookie(c, cfg, "raw-token-value")
		c.Status(http.StatusOK)
	})
	r.GET("/clear", func(c *gin.Context) {
		ClearCookie(c, cfg)
		c.Status(http.StatusOK)
	})

	wSet := httptest.NewRecorder()
	reqSet, _ := http.NewRequest(http.MethodGet, "/set", nil)
	r.ServeHTTP(wSet, reqSet)
	setCookies := wSet.Result().Cookies()
	if len(setCookies) != 1 {
		t.Fatalf("expected 1 cookie on set, got %d", len(setCookies))
	}
	sc := setCookies[0]
	if sc.Name != "sid" || sc.Value != "raw-token-value" || !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode || sc.MaxAge != 7200 {
		t.Fatalf("unexpected set cookie attributes: %+v", sc)
	}

	wClear := httptest.NewRecorder()
	reqClear, _ := http.NewRequest(http.MethodGet, "/clear", nil)
	r.ServeHTTP(wClear, reqClear)
	clearCookies := wClear.Result().Cookies()
	if len(clearCookies) != 1 {
		t.Fatalf("expected 1 cookie on clear, got %d", len(clearCookies))
	}
	cc := clearCookies[0]
	if cc.Name != "sid" || !cc.HttpOnly || !cc.Secure || cc.SameSite != http.SameSiteLaxMode || cc.MaxAge >= 0 {
		t.Fatalf("unexpected clear cookie attributes: %+v", cc)
	}
}

func TestMiddlewareAuthRevocationAndLiveRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSessionTestDB(t)
	store := NewStore(db, time.Hour)
	cfg := DefaultCookieConfig("session", time.Hour, false)

	user := authkit.User{
		Email:        "sess-user@example.com",
		PasswordHash: "hash",
		Role:         authkit.UserRoleUser,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed creating user: %v", err)
	}

	r := gin.New()
	r.GET("/ui", Middleware(store, cfg, "/login"), func(c *gin.Context) {
		uid, _ := authkit.GetUserIDFromContext(c)
		role := authkit.GetUserRoleFromContext(c)
		c.String(http.StatusOK, fmt.Sprintf("%d:%s", uid, role))
	})
	r.GET("/api", Middleware(store, cfg, ""), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Missing cookie redirects on UI and returns 401 on API.
	wNoUI := httptest.NewRecorder()
	reqNoUI, _ := http.NewRequest(http.MethodGet, "/ui", nil)
	r.ServeHTTP(wNoUI, reqNoUI)
	if wNoUI.Code != http.StatusFound || wNoUI.Header().Get("Location") != "/login" {
		t.Fatalf("expected 302 -> /login, got %d (%s)", wNoUI.Code, wNoUI.Header().Get("Location"))
	}

	wNoAPI := httptest.NewRecorder()
	reqNoAPI, _ := http.NewRequest(http.MethodGet, "/api", nil)
	r.ServeHTTP(wNoAPI, reqNoAPI)
	if wNoAPI.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", wNoAPI.Code)
	}

	// Valid session succeeds with live user role.
	rawToken, _, err := store.Create(context.Background(), user.ID, time.Hour)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	wOK := httptest.NewRecorder()
	reqOK, _ := http.NewRequest(http.MethodGet, "/ui", nil)
	reqOK.AddCookie(&http.Cookie{Name: "session", Value: rawToken})
	r.ServeHTTP(wOK, reqOK)
	if wOK.Code != http.StatusOK || wOK.Body.String() != fmt.Sprintf("%d:user", user.ID) {
		t.Fatalf("expected 200 %d:user, got %d (%s)", user.ID, wOK.Code, wOK.Body.String())
	}

	// Role update in DB takes effect immediately without re-login.
	db.Model(&authkit.User{}).Where("id = ?", user.ID).Update("role", authkit.UserRoleAdmin)
	wAdmin := httptest.NewRecorder()
	reqAdmin, _ := http.NewRequest(http.MethodGet, "/ui", nil)
	reqAdmin.AddCookie(&http.Cookie{Name: "session", Value: rawToken})
	r.ServeHTTP(wAdmin, reqAdmin)
	if wAdmin.Code != http.StatusOK || wAdmin.Body.String() != fmt.Sprintf("%d:admin", user.ID) {
		t.Fatalf("expected updated role admin, got %d (%s)", wAdmin.Code, wAdmin.Body.String())
	}

	// Deleting user immediately invalidates session and clears cookie.
	db.Delete(&authkit.User{}, user.ID)
	wDeleted := httptest.NewRecorder()
	reqDeleted, _ := http.NewRequest(http.MethodGet, "/ui", nil)
	reqDeleted.AddCookie(&http.Cookie{Name: "session", Value: rawToken})
	r.ServeHTTP(wDeleted, reqDeleted)
	if wDeleted.Code != http.StatusFound || wDeleted.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect after user deletion, got %d", wDeleted.Code)
	}
}
