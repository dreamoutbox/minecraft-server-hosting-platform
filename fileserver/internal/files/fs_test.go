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

	// Text vs binary content detection & known binary extension rejection
	if !IsTextContent([]byte("HOST=localhost\nPORT=25575\n"), false) {
		t.Error("expected .env-style UTF-8 text to be recognized as text")
	}
	if !IsTextContent([]byte{}, false) {
		t.Error("expected empty file content to be recognized as text")
	}
	// Simulate 4096-byte sniff boundary cutting a 3-byte UTF-8 rune ("中") in half
	prefixSplitUTF8 := append(bytes.Repeat([]byte("a"), 4095), 0xe4)
	if !IsTextContent(prefixSplitUTF8, true) {
		t.Error("expected truncated multi-byte UTF-8 boundary to be recognized as text")
	}
	if IsTextContent(prefixSplitUTF8, false) {
		t.Error("expected untruncated incomplete UTF-8 to be rejected")
	}
	if IsTextContent([]byte{0xff, 0xfe, 0x00, 0x01}, false) {
		t.Error("expected binary bytes to be rejected")
	}

	// Create .yaml, .env, binary, and oversized files to verify ListDir Editable detection
	for _, tc := range []struct {
		name    string
		content []byte
		want    bool
	}{
		{"config.yaml", []byte("key: value\n"), true},
		{".paper.env", []byte("MEMORY=2G\n"), true},
		{".env", []byte("PORT=8080\n"), true},
		{"README", []byte("plain text without extension\n"), true},
		{"eula.txt", []byte("eula=true\n"), true},
		{"ops.json", []byte("[]\n"), true},
		{"bukkit.yml", []byte("settings:\n"), true},
		{"server.properties", []byte("motd=test\n"), true},
		{"split-utf8.yaml", append(bytes.Repeat([]byte("a"), 4095), []byte("中文\n")...), true},
		{"level.dat", []byte{0x1f, 0x8b, 0x08, 0x00, 0x00}, false},
		{"icon.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, false},
		{"huge.log", bytes.Repeat([]byte("x"), int(MaxEditableFileBytes)+1), false},
	} {
		if err := os.WriteFile(filepath.Join(rootDir, tc.name), tc.content, 0644); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}

	listed, err := ListDir(rootDir, ".")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	editableByName := make(map[string]bool, len(listed))
	for _, e := range listed {
		editableByName[e.Name] = e.Editable
	}
	for name, want := range map[string]bool{
		"config.yaml":       true,
		".paper.env":        true,
		".env":              true,
		"README":            true,
		"eula.txt":          true,
		"ops.json":          true,
		"bukkit.yml":        true,
		"server.properties": true,
		"split-utf8.yaml":   true,
		"level.dat":         false,
		"icon.png":          false,
		"huge.log":          false,
	} {
		if got := editableByName[name]; got != want {
			t.Errorf("ListDir %s Editable = %v, want %v", name, got, want)
		}
	}

	// CreateFile, ReadEditableFile, SaveEditableFile on .env and .yaml
	for _, name := range []string{"notes.txt", ".rcon-cli.yaml", ".custom.env"} {
		if err := CreateFile(rootDir, ".", name); err != nil {
			t.Fatalf("CreateFile(%s) failed: %v", name, err)
		}
		if err := SaveEditableFile(rootDir, name, "hello=world\n", 1024); err != nil {
			t.Fatalf("SaveEditableFile(%s) failed: %v", name, err)
		}
		gotContent, err := ReadEditableFile(rootDir, name, 1024)
		if err != nil || gotContent != "hello=world\n" {
			t.Fatalf("ReadEditableFile(%s) got (%q, %v)", name, gotContent, err)
		}
	}
	if err := CreateFile(rootDir, ".", "notes.txt"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists on duplicate file, got %v", err)
	}

	// Binary extensions and oversized files are rejected
	if err := CreateFile(rootDir, ".", "server.jar"); err != nil {
		t.Fatalf("CreateFile jar failed: %v", err)
	}
	if _, err := ReadEditableFile(rootDir, "server.jar", 1024); !errors.Is(err, ErrBinaryFile) {
		t.Errorf("expected ErrBinaryFile reading jar, got %v", err)
	}
	if err := SaveEditableFile(rootDir, "server.jar", "bad", 1024); !errors.Is(err, ErrBinaryFile) {
		t.Errorf("expected ErrBinaryFile saving jar, got %v", err)
	}
	if _, err := ReadEditableFile(rootDir, "huge.log", MaxEditableFileBytes); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("expected ErrFileTooLarge reading huge.log, got %v", err)
	}
	if err := SaveEditableFile(rootDir, "notes.txt", strings.Repeat("a", 50), 10); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("expected ErrFileTooLarge saving oversized text, got %v", err)
	}
	if err := SaveEditableFile(rootDir, "notes.txt", "abc\x00def", 1024); !errors.Is(err, ErrBinaryFile) {
		t.Errorf("expected ErrBinaryFile saving NUL byte content, got %v", err)
	}

	// Binary check on .txt (both early and past the 4KB sniff boundary)
	if err := os.WriteFile(filepath.Join(rootDir, "bin.txt"), []byte{0x00, 0x01, 0x02}, 0644); err != nil {
		t.Fatalf("write bin.txt: %v", err)
	}
	if _, err := ReadEditableFile(rootDir, "bin.txt", 1024); !errors.Is(err, ErrBinaryFile) {
		t.Errorf("expected ErrBinaryFile for NUL byte file, got %v", err)
	}
	lateBin := append(bytes.Repeat([]byte("a"), 5000), 0x00, 0x01)
	if err := os.WriteFile(filepath.Join(rootDir, "late-bin.txt"), lateBin, 0644); err != nil {
		t.Fatalf("write late-bin.txt: %v", err)
	}
	if _, err := ReadEditableFile(rootDir, "late-bin.txt", MaxEditableFileBytes); !errors.Is(err, ErrBinaryFile) {
		t.Errorf("expected ErrBinaryFile for NUL byte after 4KB, got %v", err)
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

type failingReader struct {
	sent bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, "partial-data")
		return n, nil
	}
	return 0, errors.New("simulated no space left on device")
}

func TestSaveUploadedFileRemovesPartialOnError(t *testing.T) {
	rootDir := t.TempDir()
	if err := SaveUploadedFile(rootDir, ".", "huge.bin", &failingReader{}); err == nil {
		t.Fatal("expected SaveUploadedFile error on failing stream")
	}
	if _, err := os.Stat(filepath.Join(rootDir, "huge.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected partial huge.bin to be removed after write error, stat err=%v", err)
	}
}

func TestOpenPathsForZip(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()

	if err := SaveUploadedFile(rootDir, ".", "a.txt", bytes.NewBufferString("alpha")); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := CreateDir(rootDir, ".", "sub"); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := SaveUploadedFile(rootDir, "sub", "b.txt", bytes.NewBufferString("beta")); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("nope"), 0644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(rootDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Empty list rejected
	if _, _, _, err := OpenPathsForZip(rootDir, nil); !errors.Is(err, ErrNoPathsSelected) {
		t.Fatalf("expected ErrNoPathsSelected, got %v", err)
	}
	// Root "." rejected
	if _, _, _, err := OpenPathsForZip(rootDir, []string{"."}); !errors.Is(err, ErrCannotModifyRoot) {
		t.Fatalf("expected ErrCannotModifyRoot, got %v", err)
	}

	zipName, writeZip, cleanup, err := OpenPathsForZip(rootDir, []string{"a.txt", "sub", "link.txt"})
	if err != nil {
		t.Fatalf("OpenPathsForZip failed: %v", err)
	}
	var buf bytes.Buffer
	if err := writeZip(&buf); err != nil {
		cleanup()
		t.Fatalf("writeZip failed: %v", err)
	}
	cleanup()
	if zipName != "selected.zip" {
		t.Errorf("expected selected.zip, got %s", zipName)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	found := make(map[string]string)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		found[f.Name] = string(data)
	}
	if found["a.txt"] != "alpha" {
		t.Errorf("expected a.txt=alpha, got %+v", found)
	}
	if found["sub/b.txt"] != "beta" {
		t.Errorf("expected sub/b.txt=beta, got %+v", found)
	}
	if _, leaked := found["link.txt"]; leaked {
		t.Errorf("symlink link.txt must not appear in multi-zip")
	}
}
