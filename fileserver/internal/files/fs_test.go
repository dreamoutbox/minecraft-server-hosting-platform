package files

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestCreateEditAndZipOperations(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "mounts", "11")
	outsideDir := filepath.Join(base, "secret")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatalf("mkdir rootDir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("mkdir outsideDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("nope"), 0644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// Extension checks
	for _, name := range []string{"eula.txt", "ops.json", "bukkit.yml", "server.properties", "UPPER.JSON"} {
		if !IsEditableFile(name) {
			t.Errorf("expected %s to be editable", name)
		}
	}
	for _, name := range []string{"server.jar", "region.mca", "README"} {
		if IsEditableFile(name) {
			t.Errorf("expected %s not to be editable", name)
		}
	}

	// CreateFile and duplicate guard
	if err := CreateFile(rootDir, ".", "notes.txt"); err != nil {
		t.Fatalf("CreateFile failed: %v", err)
	}
	if err := CreateFile(rootDir, ".", "notes.txt"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists on duplicate file, got %v", err)
	}
	if err := SaveEditableFile(rootDir, "notes.txt", "hello world\n", 1024); err != nil {
		t.Fatalf("SaveEditableFile failed: %v", err)
	}
	content, err := ReadEditableFile(rootDir, "notes.txt", 1024)
	if err != nil || content != "hello world\n" {
		t.Fatalf("ReadEditableFile got (%q, %v)", content, err)
	}

	// Non-editable and size limits
	if err := CreateFile(rootDir, ".", "server.jar"); err != nil {
		t.Fatalf("CreateFile jar failed: %v", err)
	}
	if _, err := ReadEditableFile(rootDir, "server.jar", 1024); !errors.Is(err, ErrNotEditable) {
		t.Errorf("expected ErrNotEditable reading jar, got %v", err)
	}
	if err := SaveEditableFile(rootDir, "server.jar", "bad", 1024); !errors.Is(err, ErrNotEditable) {
		t.Errorf("expected ErrNotEditable saving jar, got %v", err)
	}
	if err := SaveEditableFile(rootDir, "notes.txt", strings.Repeat("a", 50), 10); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("expected ErrFileTooLarge saving oversized text, got %v", err)
	}

	// Binary check on .txt
	if err := os.WriteFile(filepath.Join(rootDir, "bin.txt"), []byte{0x00, 0x01, 0x02}, 0644); err != nil {
		t.Fatalf("write bin.txt: %v", err)
	}
	if _, err := ReadEditableFile(rootDir, "bin.txt", 1024); !errors.Is(err, ErrBinaryFile) {
		t.Errorf("expected ErrBinaryFile for NUL byte file, got %v", err)
	}

	// Zip folder with nested file, empty subfolder, and external symlink (must be skipped)
	if err := CreateDir(rootDir, ".", "world"); err != nil {
		t.Fatalf("CreateDir world: %v", err)
	}
	if err := CreateDir(rootDir, "world", "empty-sub"); err != nil {
		t.Fatalf("CreateDir empty-sub: %v", err)
	}
	if err := SaveUploadedFile(rootDir, "world", "level.txt", bytes.NewBufferString("seed=123\n")); err != nil {
		t.Fatalf("SaveUploadedFile level.txt: %v", err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(rootDir, "world", "leak.txt")); err != nil {
		t.Fatalf("symlink failed: %v", err)
	}

	zipName, writeZip, cleanup, err := OpenDirForZip(rootDir, "world")
	if err != nil {
		t.Fatalf("OpenDirForZip failed: %v", err)
	}
	var buf bytes.Buffer
	if err := writeZip(&buf); err != nil {
		cleanup()
		t.Fatalf("writeZip failed: %v", err)
	}
	cleanup()
	if zipName != "world.zip" {
		t.Errorf("expected world.zip, got %s", zipName)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader failed: %v", err)
	}
	found := make(map[string]string)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		found[f.Name] = string(b)
	}
	if found["level.txt"] != "seed=123\n" {
		t.Errorf("expected level.txt content in zip, got %+v", found)
	}
	if _, ok := found["empty-sub/"]; !ok {
		t.Errorf("expected empty-sub/ dir entry in zip, got %+v", found)
	}
	if _, leaked := found["leak.txt"]; leaked {
		t.Errorf("symlinked external file leak.txt must not appear in zip")
	}

	// OpenDirForZip rejects files
	if _, _, _, err := OpenDirForZip(rootDir, "notes.txt"); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("expected ErrNotDirectory for file zip request, got %v", err)
	}
}
