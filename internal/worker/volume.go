package worker

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	unmountMaxAttempts = 5
	unmountRetryDelay  = 200 * time.Millisecond
)

// VolumeManager abstracts loop-mounted block storage lifecycle and tar streaming.
type VolumeManager interface {
	CreateVolume(ctx context.Context, volumeID string, diskLimitGB int) (mountPath string, imagePath string, err error)
	DeleteVolume(ctx context.Context, volumeID string) error
	EnsureMounted(ctx context.Context, volumeID string) error
	UnmountAll(ctx context.Context) error
	ExportVolume(ctx context.Context, volumeID string, w io.Writer) error
	ImportVolume(ctx context.Context, volumeID string, r io.Reader) error
	GetVolumePath(volumeID string) (mountPath string, exists bool)
}

// LocalVolumeManager manages sparse image files and directory mounts on the node host.
type LocalVolumeManager struct {
	baseDir        string
	useLoopMount   bool
	mu             sync.RWMutex
	volumeMountMap map[string]string
	volumeImageMap map[string]string
}

// NewLocalVolumeManager initializes a volume manager rooted at the specified base directory.
//
// Arguments:
//   - baseDir: Filesystem root where sparse images and mount directories reside.
//   - useLoopMount: Whether to execute raw loop device mount commands (requires SYS_ADMIN).
//
// Example usage:
//
//	vm := worker.NewLocalVolumeManager("./data/volumes", false)
func NewLocalVolumeManager(baseDir string, useLoopMount bool) *LocalVolumeManager {
	return &LocalVolumeManager{
		baseDir:        baseDir,
		useLoopMount:   useLoopMount,
		volumeMountMap: make(map[string]string),
		volumeImageMap: make(map[string]string),
	}
}

// CreateVolume provisions a sparse image sized to the plan's disk limit and prepares its mount point.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Unique identifier for the volume (e.g. server ID).
//   - diskLimitGB: Disk allocation size bound in gigabytes.
//
// Example usage:
//
//	mount, img, err := vm.CreateVolume(ctx, "server-42", 10)
func (m *LocalVolumeManager) CreateVolume(ctx context.Context, volumeID string, diskLimitGB int) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	imagesDir := filepath.Join(m.baseDir, "images")
	mountsDir, imagePath := m.resolvePathsLocked(volumeID)

	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed creating images directory: %w", err)
	}
	if err := os.MkdirAll(mountsDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed creating mount directory: %w", err)
	}

	if m.useLoopMount {
		if err := unmountDir(ctx, mountsDir); err != nil {
			return "", "", fmt.Errorf("failed unmounting stale mount at %s: %w", mountsDir, err)
		}
		detachLoopDevices(ctx, imagePath)
	}

	// Remove any pre-existing image so we always allocate a fresh inode instead of
	// truncating a file still held by a stale kernel loop device.
	if err := os.Remove(imagePath); err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("failed removing stale sparse image %s: %w", imagePath, err)
	}

	f, err := os.Create(imagePath)
	if err != nil {
		return "", "", fmt.Errorf("failed creating sparse file: %w", err)
	}
	sizeBytes := int64(diskLimitGB) * 1024 * 1024 * 1024
	if err := f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(imagePath)
		return "", "", fmt.Errorf("failed truncating sparse file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(imagePath)
		return "", "", fmt.Errorf("failed closing sparse image: %w", err)
	}

	if m.useLoopMount {
		mkfsCmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", imagePath)
		if output, err := mkfsCmd.CombinedOutput(); err != nil {
			_ = os.Remove(imagePath)
			return "", "", fmt.Errorf("mkfs.ext4 failed (%s): %w", string(output), err)
		}

		if err := mountLoopImage(ctx, imagePath, mountsDir); err != nil {
			_ = os.Remove(imagePath)
			return "", "", err
		}
	}

	m.volumeMountMap[volumeID] = mountsDir
	m.volumeImageMap[volumeID] = imagePath

	return mountsDir, imagePath, nil
}

// EnsureMounted mounts an existing ext4 volume image onto its mount directory if not already mounted.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Unique volume identifier.
//
// Example usage:
//
//	err := vm.EnsureMounted(ctx, "42")
func (m *LocalVolumeManager) EnsureMounted(ctx context.Context, volumeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.useLoopMount || volumeID == "" {
		return nil
	}

	mountDir, imagePath := m.resolvePathsLocked(volumeID)
	if _, err := os.Stat(imagePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed inspecting image %s: %w", imagePath, err)
	}

	mounted, err := isMountPoint(mountDir)
	if err != nil {
		return fmt.Errorf("failed checking mount state for %s: %w", mountDir, err)
	}
	if mounted {
		m.volumeMountMap[volumeID] = mountDir
		m.volumeImageMap[volumeID] = imagePath
		return nil
	}

	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed creating mount directory %s: %w", mountDir, err)
	}

	if err := mountLoopImage(ctx, imagePath, mountDir); err != nil {
		return err
	}

	m.volumeMountMap[volumeID] = mountDir
	m.volumeImageMap[volumeID] = imagePath
	return nil
}

// DeleteVolume unmounts active loop mounts and deletes mount directory contents and sparse image file.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Unique volume identifier.
//
// Example usage:
//
//	err := vm.DeleteVolume(ctx, "server-42")
func (m *LocalVolumeManager) DeleteVolume(ctx context.Context, volumeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mountDir, imagePath := m.resolvePathsLocked(volumeID)

	if m.useLoopMount {
		if err := unmountDir(ctx, mountDir); err != nil {
			return fmt.Errorf("failed unmounting volume %s at %s: %w", volumeID, mountDir, err)
		}
		detachLoopDevices(ctx, imagePath)
	}

	if err := os.RemoveAll(mountDir); err != nil {
		return fmt.Errorf("failed removing mount directory %s: %w", mountDir, err)
	}
	if err := os.Remove(imagePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed removing volume image %s: %w", imagePath, err)
	}

	delete(m.volumeMountMap, volumeID)
	delete(m.volumeImageMap, volumeID)

	return nil
}

// UnmountAll unmounts all active ext4 loop volumes under the worker's mounts directory while preserving images.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	err := vm.UnmountAll(ctx)
func (m *LocalVolumeManager) UnmountAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.useLoopMount {
		return nil
	}

	candidates := make(map[string]struct{})
	for _, mountDir := range m.volumeMountMap {
		candidates[filepath.Clean(mountDir)] = struct{}{}
	}

	mountsRoot := filepath.Clean(filepath.Join(m.baseDir, "mounts"))
	if entries, err := os.ReadDir(mountsRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				candidates[filepath.Join(mountsRoot, entry.Name())] = struct{}{}
			}
		}
	}

	if activeMounts, err := readMountPoints(); err == nil {
		prefix := mountsRoot + string(filepath.Separator)
		for _, mp := range activeMounts {
			cleanMP := filepath.Clean(mp)
			if strings.HasPrefix(cleanMP, prefix) {
				candidates[cleanMP] = struct{}{}
			}
		}
	}

	var errs []error
	unmountedDirs := make(map[string]bool)
	for mountDir := range candidates {
		if err := unmountDir(ctx, mountDir); err != nil {
			errs = append(errs, err)
			continue
		}
		unmountedDirs[mountDir] = true
	}

	for volID, mountDir := range m.volumeMountMap {
		if unmountedDirs[filepath.Clean(mountDir)] {
			delete(m.volumeMountMap, volID)
		}
	}

	return errors.Join(errs...)
}

// ExportVolume streams a tar archive of all files contained within the volume's mount point.
//
// Arguments:
//   - ctx: Operation context for cancellation during long transfers.
//   - volumeID: Identifier of the volume to read.
//   - w: Output writer receiving the streaming tar archive.
//
// Example usage:
//
//	err := vm.ExportVolume(ctx, "server-42", responseWriter)
func (m *LocalVolumeManager) ExportVolume(ctx context.Context, volumeID string, w io.Writer) error {
	if err := m.EnsureMounted(ctx, volumeID); err != nil {
		return err
	}

	m.mu.RLock()
	mountDir, _ := m.resolvePathsLocked(volumeID)
	m.mu.RUnlock()

	if _, err := os.Stat(mountDir); os.IsNotExist(err) {
		return fmt.Errorf("volume mount directory %s does not exist", mountDir)
	}

	tw := tar.NewWriter(w)
	defer func() { _ = tw.Close() }()

	return filepath.Walk(mountDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		relPath, err := filepath.Rel(mountDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.Mode().IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()

		_, err = io.Copy(tw, file)
		return err
	})
}

// ImportVolume unpacks a tar stream into the volume's mount point, sanitizing paths to prevent traversal.
//
// Arguments:
//   - ctx: Operation context for cancellation.
//   - volumeID: Target volume identifier.
//   - r: Reader providing the tar stream archive.
//
// Example usage:
//
//	err := vm.ImportVolume(ctx, "server-42", requestBody)
func (m *LocalVolumeManager) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	if err := m.EnsureMounted(ctx, volumeID); err != nil {
		return err
	}

	m.mu.Lock()
	mountDir, _ := m.resolvePathsLocked(volumeID)
	m.mu.Unlock()

	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed creating mount target directory: %w", err)
	}

	// Clean out existing contents prior to import
	entries, err := os.ReadDir(mountDir)
	if err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(mountDir, e.Name()))
		}
	}

	tr := tar.NewReader(r)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading tar archive: %w", err)
		}

		// Prevent path traversal outside mount directory
		cleanRel := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			return fmt.Errorf("illegal relative path in tar archive: %s", header.Name)
		}

		targetPath := filepath.Join(mountDir, cleanRel)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed creating directory %s: %w", targetPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed creating parent dir for %s: %w", targetPath, err)
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed creating file %s: %w", targetPath, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				_ = outFile.Close()
				return fmt.Errorf("failed extracting contents for %s: %w", targetPath, err)
			}
			if err := outFile.Close(); err != nil {
				return fmt.Errorf("failed closing file %s: %w", targetPath, err)
			}
		}
	}

	return nil
}

// GetVolumePath retrieves the current filesystem mount path for a known volume.
//
// Arguments:
//   - volumeID: Identifier of the volume.
//
// Example usage:
//
//	path, ok := vm.GetVolumePath("server-42")
func (m *LocalVolumeManager) GetVolumePath(volumeID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mountDir, ok := m.volumeMountMap[volumeID]
	if !ok {
		mountDir = filepath.Join(m.baseDir, "mounts", volumeID)
		if _, err := os.Stat(mountDir); err == nil {
			return mountDir, true
		}
		return "", false
	}
	return mountDir, true
}

func (m *LocalVolumeManager) resolvePathsLocked(volumeID string) (string, string) {
	mountDir, mountOk := m.volumeMountMap[volumeID]
	if !mountOk {
		mountDir = filepath.Join(m.baseDir, "mounts", volumeID)
	}

	imagePath, imgOk := m.volumeImageMap[volumeID]
	if !imgOk {
		imagePath = filepath.Join(m.baseDir, "images", fmt.Sprintf("%s.img", volumeID))
	}

	return mountDir, imagePath
}

func mountLoopImage(ctx context.Context, imagePath, mountDir string) error {
	mountCmd := exec.CommandContext(ctx, "mount", "-o", "loop", imagePath, mountDir)
	if output, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount -o loop failed (%s): %w", string(output), err)
	}
	return nil
}

// detachLoopDevices detaches any active kernel loop devices backed by imagePath.
func detachLoopDevices(ctx context.Context, imagePath string) {
	if _, err := os.Stat(imagePath); err != nil {
		return
	}
	out, err := exec.CommandContext(ctx, "losetup", "-j", imagePath, "-O", "NAME", "--noheadings").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dev := strings.TrimSpace(line)
		if dev != "" {
			_ = exec.CommandContext(ctx, "losetup", "-d", dev).Run()
		}
	}
}

func unmountDir(ctx context.Context, mountDir string) error {
	mounted, err := isMountPoint(mountDir)
	if err != nil {
		return fmt.Errorf("failed checking mount state for %s: %w", mountDir, err)
	}
	if !mounted {
		return nil
	}

	var lastOut string
	var lastErr error
	for attempt := 0; attempt < unmountMaxAttempts; attempt++ {
		// -d explicitly detaches the associated loop device so loop slots are freed immediately.
		umountCmd := exec.CommandContext(ctx, "umount", "-d", mountDir)
		out, err := umountCmd.CombinedOutput()
		if err == nil {
			stillMounted, checkErr := isMountPoint(mountDir)
			if checkErr == nil && !stillMounted {
				return nil
			}
		}
		lastOut = strings.TrimSpace(string(out))
		lastErr = err

		stillMounted, checkErr := isMountPoint(mountDir)
		if checkErr == nil && !stillMounted {
			return nil
		}

		if attempt < unmountMaxAttempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(unmountRetryDelay):
			}
		}
	}

	return fmt.Errorf("umount -d %s failed (%s): %w", mountDir, lastOut, lastErr)
}

func isMountPoint(target string) (bool, error) {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		absTarget = filepath.Clean(target)
	}
	resolvedTarget := absTarget
	if eval, err := filepath.EvalSymlinks(absTarget); err == nil {
		resolvedTarget = eval
	}

	mounts, err := readMountPoints()
	if err != nil {
		return false, err
	}
	for _, mp := range mounts {
		cleanMP := filepath.Clean(mp)
		if cleanMP == absTarget || cleanMP == resolvedTarget {
			return true, nil
		}
	}
	return false, nil
}

func readMountPoints() ([]string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var mounts []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 5 {
			mounts = append(mounts, unescapeMountInfoPath(fields[4]))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

func unescapeMountInfoPath(p string) string {
	r := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return r.Replace(p)
}
