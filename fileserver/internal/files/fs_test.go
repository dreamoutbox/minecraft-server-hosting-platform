package files

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFileOpsAndRootSafety(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "mounts", "10")
	outsideDir := filepath.Join(base, "secret")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatalf("mkdir rootDir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("mkdir outsideDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("top-secret"), 0644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// Create symlink pointing outside rootDir
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(rootDir, "escape-link")); err != nil {
		t.Fatalf("symlink failed: %v", err)
	}

	// 1. Create folder and upload file
	if err := CreateDir(rootDir, ".", "plugins"); err != nil {
		t.Fatalf("CreateDir failed: %v", err)
	}
	if err := SaveUploadedFile(rootDir, "plugins", "config.yml", bytes.NewBufferString("enabled: true\n")); err != nil {
		t.Fatalf("SaveUploadedFile failed: %v", err)
	}

	// 2. List directory
	entries, err := ListDir(rootDir, ".")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	if len(entries) == 0 || !entries[0].IsDir || entries[0].Name != "plugins" {
		t.Fatalf("expected plugins dir first, got %+v", entries)
	}

	// 3. Download file
	f, info, closeFn, err := OpenDownload(rootDir, filepath.Join("plugins", "config.yml"))
	if err != nil {
		t.Fatalf("OpenDownload failed: %v", err)
	}
	data, _ := io.ReadAll(f)
	closeFn()
	if string(data) != "enabled: true\n" || info.Size() == 0 {
		t.Fatalf("unexpected downloaded content: %q", string(data))
	}

	// 4. Rename file
	if err := RenameEntry(rootDir, filepath.Join("plugins", "config.yml"), "config.yml.bak"); err != nil {
		t.Fatalf("RenameEntry failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "plugins", "config.yml.bak")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	// 5. Reject symlink escape outside root
	if _, _, _, err := OpenDownload(rootDir, "escape-link"); err == nil {
		t.Fatal("expected symlink pointing outside root to be rejected by os.Root")
	}

	// 6. Reject traversal and invalid names
	if err := CreateDir(rootDir, ".", "../escape"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("expected ErrInvalidName for traversal folder name, got %v", err)
	}
	if err := DeleteEntry(rootDir, "."); !errors.Is(err, ErrCannotModifyRoot) {
		t.Errorf("expected ErrCannotModifyRoot deleting root, got %v", err)
	}
	if err := RenameEntry(rootDir, ".", "other"); !errors.Is(err, ErrCannotModifyRoot) {
		t.Errorf("expected ErrCannotModifyRoot renaming root, got %v", err)
	}

	// 7. Delete folder recursively
	if err := DeleteEntry(rootDir, "plugins"); err != nil {
		t.Fatalf("DeleteEntry failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("expected plugins directory deleted, stat err=%v", err)
	}
}
