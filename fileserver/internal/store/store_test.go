package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T, mountsDir string) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	return New(db, mountsDir)
}

func TestEnsureUserIdempotent(t *testing.T) {
	st := newTestStore(t, "/var/lib/mc-platform/mounts")
	ctx := context.Background()

	u1, created1, err := st.EnsureUser(ctx, 10, "Player@Example.COM")
	if err != nil {
		t.Fatalf("EnsureUser 1 failed: %v", err)
	}
	if !created1 || u1.ID != 10 || u1.Email != "player@example.com" || u1.PasswordHash == "" {
		t.Fatalf("unexpected first EnsureUser result: created=%v user=%+v", created1, u1)
	}

	u2, created2, err := st.EnsureUser(ctx, 10, "player@example.com")
	if err != nil {
		t.Fatalf("EnsureUser 2 failed: %v", err)
	}
	if created2 || u2.ID != 10 || u2.PasswordHash != u1.PasswordHash {
		t.Fatalf("expected idempotent EnsureUser without recreating password")
	}
}

func TestPermissionsAndPathAuthorization(t *testing.T) {
	mountsDir := "/var/lib/mc-platform/mounts"
	st := newTestStore(t, mountsDir)
	ctx := context.Background()

	perm, err := st.Allow(ctx, 5, 42)
	if err != nil {
		t.Fatalf("Allow failed: %v", err)
	}
	expectedRoot := filepath.Join(mountsDir, "42")
	if perm.DirPath != expectedRoot {
		t.Fatalf("expected DirPath %s, got %s", expectedRoot, perm.DirPath)
	}

	// Idempotent Allow
	if _, err := st.Allow(ctx, 5, 42); err != nil {
		t.Fatalf("idempotent Allow failed: %v", err)
	}

	// Exact root match
	matched, rel, err := st.FindPermissionForPath(ctx, 5, expectedRoot)
	if err != nil || matched.ServerID != 42 || rel != "." {
		t.Fatalf("expected root match, got matched=%+v rel=%q err=%v", matched, rel, err)
	}

	// Subdirectory match
	_, relSub, err := st.FindPermissionForPath(ctx, 5, filepath.Join(expectedRoot, "plugins", "config.yml"))
	if err != nil || relSub != filepath.Join("plugins", "config.yml") {
		t.Fatalf("expected subpath match, got rel=%q err=%v", relSub, err)
	}

	// Prefix-collision escape (e.g. /mounts/420 must NOT match /mounts/42)
	if _, _, err := st.FindPermissionForPath(ctx, 5, filepath.Join(mountsDir, "420")); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied for sibling prefix 420, got %v", err)
	}

	// Traversal escape
	if _, _, err := st.FindPermissionForPath(ctx, 5, expectedRoot+"/../99"); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied for traversal, got %v", err)
	}

	// Wrong user
	if _, _, err := st.FindPermissionForPath(ctx, 999, expectedRoot); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied for unauthorized user, got %v", err)
	}

	// Disallow one user
	if err := st.Disallow(ctx, 5, 42); err != nil {
		t.Fatalf("Disallow failed: %v", err)
	}
	if _, _, err := st.FindPermissionForPath(ctx, 5, expectedRoot); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied after Disallow, got %v", err)
	}

	// RevokeServer removes all grants for a server
	_, _ = st.Allow(ctx, 5, 77)
	_, _ = st.Allow(ctx, 6, 77)
	if err := st.RevokeServer(ctx, 77); err != nil {
		t.Fatalf("RevokeServer failed: %v", err)
	}
	for _, uid := range []uint{5, 6} {
		if _, _, err := st.FindPermissionForPath(ctx, uid, fmt.Sprintf("%s/77", mountsDir)); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("expected user %d revoked, got %v", uid, err)
		}
	}
}

func TestConsumeLoginTokenSingleUse(t *testing.T) {
	st := newTestStore(t, "/var/lib/mc-platform/mounts")
	ctx := context.Background()

	if err := st.ConsumeLoginToken(ctx, "jti-1", time.Now().Add(60*time.Second)); err != nil {
		t.Fatalf("first consume failed: %v", err)
	}
	if err := st.ConsumeLoginToken(ctx, "jti-1", time.Now().Add(60*time.Second)); !errors.Is(err, ErrTokenAlreadyUsed) {
		t.Fatalf("expected ErrTokenAlreadyUsed on replay, got %v", err)
	}
}
