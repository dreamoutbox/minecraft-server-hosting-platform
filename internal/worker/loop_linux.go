//go:build linux

package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	loopControlPath = "/dev/loop-control"
	loopDeviceDir   = "/dev"
	loopDeviceMajor = 7
	loopDeviceMode  = 0660
)

// allocateFreeLoopIndex requests the next available loop device index from the kernel via /dev/loop-control.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	idx, err := allocateFreeLoopIndex()
func allocateFreeLoopIndex() (int, error) {
	ctrl, err := os.OpenFile(loopControlPath, os.O_RDWR, 0)
	if err != nil {
		return -1, fmt.Errorf("failed opening %s: %w", loopControlPath, err)
	}
	defer func() { _ = ctrl.Close() }()

	idx, err := unix.IoctlRetInt(int(ctrl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return -1, fmt.Errorf("LOOP_CTL_GET_FREE on %s failed: %w", loopControlPath, err)
	}
	return idx, nil
}

// ensureLoopDeviceNode ensures /dev/loop<index> exists as a block device node (major 7, minor index).
//
// Arguments:
//   - index: Non-negative loop device index returned by /dev/loop-control or mountinfo.
//
// Example usage:
//
//	devPath, err := ensureLoopDeviceNode(8)
func ensureLoopDeviceNode(index int) (string, error) {
	return ensureLoopDeviceNodeAt(loopDeviceDir, index, unix.Mknod)
}

// ensureLoopDeviceNodeAt creates a loop block device node inside devDir if it does not already exist.
//
// Arguments:
//   - devDir: Directory where the loopN node should reside (typically "/dev").
//   - index: Non-negative loop device minor number.
//   - mknodFn: Syscall implementation used to create the block device node (injectable for tests).
//
// Example usage:
//
//	devPath, err := ensureLoopDeviceNodeAt("/dev", 12, unix.Mknod)
func ensureLoopDeviceNodeAt(devDir string, index int, mknodFn func(path string, mode uint32, dev int) error) (string, error) {
	if index < 0 {
		return "", fmt.Errorf("invalid loop device index %d", index)
	}

	devPath := filepath.Join(devDir, fmt.Sprintf("loop%d", index))
	info, err := os.Stat(devPath)
	if err == nil {
		isBlockDevice := info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0
		if !isBlockDevice {
			return "", fmt.Errorf("%s exists but is not a block device", devPath)
		}
		return devPath, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed inspecting %s: %w", devPath, err)
	}

	devNum := int(unix.Mkdev(loopDeviceMajor, uint32(index)))
	if err := mknodFn(devPath, unix.S_IFBLK|loopDeviceMode, devNum); err != nil && !errors.Is(err, unix.EEXIST) {
		return "", fmt.Errorf("failed creating loop device node %s (major %d minor %d): %w", devPath, loopDeviceMajor, index, err)
	}
	return devPath, nil
}

// ensureLoopDeviceNodeForPath parses a /dev/loopN path and ensures its block device node exists.
//
// Arguments:
//   - devPath: Candidate device path (e.g. "/dev/loop12"). Non-loop paths are ignored.
//
// Example usage:
//
//	_ = ensureLoopDeviceNodeForPath("/dev/loop12")
func ensureLoopDeviceNodeForPath(devPath string) error {
	idx, ok := parseLoopDeviceIndex(devPath)
	if !ok {
		return nil
	}
	_, err := ensureLoopDeviceNode(idx)
	return err
}
