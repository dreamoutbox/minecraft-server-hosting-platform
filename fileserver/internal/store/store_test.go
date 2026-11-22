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

	u1, created1, err := st.EnsureUser(ctx, 10, "Player@Example.COM", nil, nil)
	if err != nil {
		t.Fatalf("EnsureUser 1 failed: %v", err)
	}
	if !created1 || u1.ID != 10 || u1.Email != "player@example.com" || u1.PasswordHash == "" {
		t.Fatalf("unexpected first EnsureUser result: created=%v user=%+v", created1, u1)
	}
	if u1.UID != nil || u1.GID != nil {
		t.Fatalf("expected nil UID/GID by default, got uid=%v gid=%v", u1.UID, u1.GID)
	}

	u2, created2, err := st.EnsureUser(ctx, 10, "player@example.com", nil, nil)
	if err != nil {
		t.Fatalf("EnsureUser 2 failed: %v", err)
	}
	if created2 || u2.ID != 10 || u2.PasswordHash != u1.PasswordHash {
		t.Fatalf("expected idempotent EnsureUser without recreating password")
	}
}

func TestEnsureUserUIDGIDLifecycle(t *testing.T) {
	st := newTestStore(t, "/var/lib/mc-platform/mounts")
	ctx := context.Background()

	intPtr := func(v int) *int { return &v }

	// Reject negative UID or GID
	if _, _, err := st.EnsureUser(ctx, 11, "neg@example.com", intPtr(-1), intPtr(1000)); !errors.Is(err, ErrInvalidUIDGID) {
		t.Fatalf("expected ErrInvalidUIDGID for negative uid, got %v", err)
	}
	if _, _, err := st.EnsureUser(ctx, 11, "neg@example.com", intPtr(1000), intPtr(-2)); !errors.Is(err, ErrInvalidUIDGID) {
		t.Fatalf("expected ErrInvalidUIDGID for negative gid, got %v", err)
	}

	// Create with UID/GID
	u1, created, err := st.EnsureUser(ctx, 12, "mc@example.com", intPtr(1000), intPtr(1000))
	if err != nil {
		t.Fatalf("EnsureUser with uid/gid failed: %v", err)
	}
	if !created || u1.UID == nil || *u1.UID != 1000 || u1.GID == nil || *u1.GID != 1000 {
		t.Fatalf("expected created user with 1000:1000, got created=%v user=%+v", created, u1)
	}

	// Nil uid/gid on subsequent call preserves existing values
	u2, created, err := st.EnsureUser(ctx, 12, "mc@example.com", nil, nil)
	if err != nil {
		t.Fatalf("EnsureUser with nil uid/gid failed: %v", err)
	}
	if created || u2.UID == nil || *u2.UID != 1000 || u2.GID == nil || *u2.GID != 1000 {
		t.Fatalf("expected preserved 1000:1000, got created=%v user=%+v", created, u2)
	}

	// Non-nil changed values update existing user (including 0)
	u3, created, err := st.EnsureUser(ctx, 12, "mc@example.com", intPtr(2000), intPtr(0))
	if err != nil {
		t.Fatalf("EnsureUser update uid/gid failed: %v", err)
	}
	if created || u3.UID == nil || *u3.UID != 2000 || u3.GID == nil || *u3.GID != 0 {
		t.Fatalf("expected updated 2000:0, got created=%v user=%+v", created, u3)
	}

	// Reload via GetUser to confirm persistence in SQLite
	loaded, err := st.GetUser(ctx, 12)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if loaded.UID == nil || *loaded.UID != 2000 || loaded.GID == nil || *loaded.GID != 0 {
		t.Fatalf("expected persisted 2000:0, got %+v", loaded)
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

func TestTransferUsageUpsertAndRollover(t *testing.T) {
	st := newTestStore(t, "/var/lib/mc-platform/mounts")
	ctx := context.Background()

	day1 := time.Now().UTC().Format(UsageDayFormat)
	day2 := time.Now().UTC().AddDate(0, 0, 1).Format(UsageDayFormat)

	// Missing row returns zero usage
	u0, err := st.GetUsage(ctx, 1, day1)
	if err != nil {
		t.Fatalf("GetUsage initial failed: %v", err)
	}
	if u0.UploadBytes != 0 || u0.DownloadBytes != 0 {
		t.Fatalf("expected zero usage, got %+v", u0)
	}

	// Accumulate uploads and downloads on day1
	if err := st.AddUsage(ctx, 1, day1, TransferDirectionUpload, 100); err != nil {
		t.Fatalf("AddUsage upload 1: %v", err)
	}
	if err := st.AddUsage(ctx, 1, day1, TransferDirectionUpload, 250); err != nil {
		t.Fatalf("AddUsage upload 2: %v", err)
	}
	if err := st.AddUsage(ctx, 1, day1, TransferDirectionDownload, 500); err != nil {
		t.Fatalf("AddUsage download 1: %v", err)
	}

	u1, err := st.GetUsage(ctx, 1, day1)
	if err != nil {
		t.Fatalf("GetUsage day1 failed: %v", err)
	}
	if u1.UploadBytes != 350 || u1.DownloadBytes != 500 {
		t.Fatalf("expected upload=350 download=500, got %+v", u1)
	}

	// Day rollover isolates day2 and other users
	uNext, err := st.GetUsage(ctx, 1, day2)
	if err != nil {
		t.Fatalf("GetUsage day2 failed: %v", err)
	}
	if uNext.UploadBytes != 0 || uNext.DownloadBytes != 0 {
		t.Fatalf("expected zero usage on day2, got %+v", uNext)
	}

	uOther, err := st.GetUsage(ctx, 2, day1)
	if err != nil {
		t.Fatalf("GetUsage user 2 failed: %v", err)
	}
	if uOther.UploadBytes != 0 || uOther.DownloadBytes != 0 {
		t.Fatalf("expected zero usage for user 2, got %+v", uOther)
	}

	// Old rows beyond retention are pruned on write
	oldDay := time.Now().UTC().AddDate(0, 0, -(usageRetentionDays + 5)).Format(UsageDayFormat)
	if err := st.AddUsage(ctx, 1, oldDay, TransferDirectionUpload, 42); err != nil {
		t.Fatalf("AddUsage oldDay: %v", err)
	}
	uOld, err := st.GetUsage(ctx, 1, oldDay)
	if err != nil {
		t.Fatalf("GetUsage oldDay: %v", err)
	}
	if uOld.UploadBytes != 0 {
		t.Fatalf("expected expired usage row to be pruned, got %+v", uOld)
	}
}
