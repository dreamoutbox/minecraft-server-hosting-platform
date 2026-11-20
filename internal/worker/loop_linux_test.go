//go:build linux

package worker

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsureLoopDeviceNodeAt(t *testing.T) {
	tempDir := t.TempDir()

	var calledPath string
	var calledMode uint32
	var calledDev int
	fakeMknod := func(path string, mode uint32, dev int) error {
		calledPath = path
		calledMode = mode
		calledDev = dev
		return nil
	}

	gotPath, err := ensureLoopDeviceNodeAt(tempDir, 15, fakeMknod)
	if err != nil {
		t.Fatalf("ensureLoopDeviceNodeAt returned error: %v", err)
	}
	wantPath := filepath.Join(tempDir, "loop15")
	if gotPath != wantPath || calledPath != wantPath {
		t.Errorf("path = (%q, called %q), want %q", gotPath, calledPath, wantPath)
	}
	wantMode := uint32(unix.S_IFBLK | loopDeviceMode)
	if calledMode != wantMode {
		t.Errorf("mode = %#o, want %#o", calledMode, wantMode)
	}
	wantDev := int(unix.Mkdev(loopDeviceMajor, 15))
	if calledDev != wantDev {
		t.Errorf("dev = %d, want %d", calledDev, wantDev)
	}

	// EEXIST race should succeed cleanly
	eexistMknod := func(path string, mode uint32, dev int) error {
		return unix.EEXIST
	}
	if _, err := ensureLoopDeviceNodeAt(tempDir, 16, eexistMknod); err != nil {
		t.Fatalf("expected EEXIST race to succeed, got: %v", err)
	}

	// Negative index should fail
	if _, err := ensureLoopDeviceNodeAt(tempDir, -1, fakeMknod); err == nil {
		t.Error("expected negative loop index to return an error")
	}

	// Existing regular file at target path should fail rather than being treated as a block device
	nonBlockPath := filepath.Join(tempDir, "loop20")
	if err := os.WriteFile(nonBlockPath, []byte("not-a-device"), 0644); err != nil {
		t.Fatalf("failed creating dummy file: %v", err)
	}
	if _, err := ensureLoopDeviceNodeAt(tempDir, 20, fakeMknod); err == nil {
		t.Error("expected regular file at /dev/loop20 to return an error")
	}
}
