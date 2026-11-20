//go:build !linux

package worker

import "errors"

var errLoopDevicesUnsupported = errors.New("loop devices are only supported on Linux")

// allocateFreeLoopIndex returns an error on non-Linux platforms where loop-control is unavailable.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	_, err := allocateFreeLoopIndex()
func allocateFreeLoopIndex() (int, error) {
	return -1, errLoopDevicesUnsupported
}

// ensureLoopDeviceNode returns an error on non-Linux platforms where loop block nodes are unavailable.
//
// Arguments:
//   - index: Loop device minor index.
//
// Example usage:
//
//	_, err := ensureLoopDeviceNode(0)
func ensureLoopDeviceNode(index int) (string, error) {
	return "", errLoopDevicesUnsupported
}

// ensureLoopDeviceNodeForPath is a no-op on non-Linux platforms.
//
// Arguments:
//   - devPath: Candidate device path.
//
// Example usage:
//
//	_ = ensureLoopDeviceNodeForPath("/dev/loop0")
func ensureLoopDeviceNodeForPath(devPath string) error {
	return nil
}
