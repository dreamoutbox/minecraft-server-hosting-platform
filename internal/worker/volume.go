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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	unmountMaxAttempts   = 5
	unmountRetryDelay    = 200 * time.Millisecond
	loopMountMaxAttempts = 5
	loopMountRetryDelay  = 50 * time.Millisecond
	loopDevicePathPrefix = "/dev/loop"
)

// VolumeManager abstracts loop-mounted block storage lifecycle and tar streaming.
type VolumeManager interface {
	CreateVolume(ctx context.Context, volumeID string, diskLimitGB int) (mountPath string, imagePath string, err error)
	DeleteVolume(ctx context.Context, volumeID string) error
	EnsureMounted(ctx context.Context, volumeID string) error
	UnmountAll(ctx context.Context) error
	ExportVolume(ctx context.Context, volumeID string, w io.Writer) error
	ImportVolume(ctx context.Context, volumeID string, r io.Reader) error
	UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (int64, error)
	RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error
	UploadImageBackup(ctx context.Context, volumeID string, presignedURLs []string, maxPartBytes int64) (*UploadImageBackupResponse, error)
	RestoreImageBackup(ctx context.Context, volumeID string, presignedURLs []string) (int64, error)
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

	if err := filepath.Walk(mountDir, func(path string, info os.FileInfo, err error) error {
		relPath, relErr := filepath.Rel(mountDir, path)
		if relErr == nil && isLostAndFoundRelPath(filepath.ToSlash(relPath)) {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if relErr != nil {
			return relErr
		}
		if relPath == "." {
			return nil
		}

		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, _ = os.Readlink(path)
		}

		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		return err
	}

	return tw.Close()
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

	// Clean out existing contents prior to import (preserve ext4 lost+found)
	entries, err := os.ReadDir(mountDir)
	if err != nil {
		return fmt.Errorf("failed reading mount directory %s: %w", mountDir, err)
	}
	for _, e := range entries {
		if e.Name() == "lost+found" {
			continue
		}
		target := filepath.Join(mountDir, e.Name())
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("failed removing existing entry %s: %w", target, err)
		}
	}

	trReader, closeArchive, err := unwrapOptionalGzip(r)
	if err != nil {
		return err
	}
	defer func() { _ = closeArchive() }()

	tr := tar.NewReader(trReader)
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
		if cleanRel == "." || isLostAndFoundRelPath(filepath.ToSlash(cleanRel)) {
			continue
		}

		targetPath := filepath.Join(mountDir, cleanRel)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed creating directory %s: %w", targetPath, err)
			}
			applyTarOwnership(targetPath, header.Uid, header.Gid)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed creating parent dir for %s: %w", targetPath, err)
			}
			mode := os.FileMode(header.Mode) & os.ModePerm
			if mode == 0 {
				mode = 0644
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, mode)
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
			applyTarOwnership(targetPath, header.Uid, header.Gid)
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed creating parent dir for symlink %s: %w", targetPath, err)
			}
			_ = os.Remove(targetPath)
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("failed creating symlink %s: %w", targetPath, err)
			}
			applyTarOwnership(targetPath, header.Uid, header.Gid)
		}
	}

	return nil
}

func isLostAndFoundRelPath(slashRelPath string) bool {
	return slashRelPath == "lost+found" || strings.HasPrefix(slashRelPath, "lost+found/")
}

func applyTarOwnership(path string, uid, gid int) {
	if os.Geteuid() == 0 {
		_ = os.Lchown(path, uid, gid)
	}
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

// mountLoopImage allocates a free loop device, ensures its /dev/loopN node exists, and mounts imagePath onto mountDir.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - imagePath: Path to the ext4 sparse image file.
//   - mountDir: Target directory where the image will be mounted.
//
// Example usage:
//
//	err := mountLoopImage(ctx, "/var/lib/mc-platform/images/1.img", "/var/lib/mc-platform/mounts/1")
func mountLoopImage(ctx context.Context, imagePath, mountDir string) error {
	var lastDev, lastOut string
	var lastErr error

	for attempt := 0; attempt < loopMountMaxAttempts; attempt++ {
		idx, err := allocateFreeLoopIndex()
		if err != nil {
			return fmt.Errorf("failed allocating loop device: %w", err)
		}
		loopDev, err := ensureLoopDeviceNode(idx)
		if err != nil {
			return err
		}

		// Explicit loop=/dev/loopN binds the allocated node directly with autoclear enabled.
		mountCmd := exec.CommandContext(ctx, "mount", "-o", "loop="+loopDev, imagePath, mountDir)
		output, err := mountCmd.CombinedOutput()
		if err == nil {
			return nil
		}

		lastDev = loopDev
		lastOut = strings.TrimSpace(string(output))
		lastErr = err

		// Retry in case another host process claimed the loop index between allocation and mount.
		if attempt < loopMountMaxAttempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(loopMountRetryDelay):
			}
		}
	}

	return fmt.Errorf("mount -o loop=%s failed (%s): %w", lastDev, lastOut, lastErr)
}

// detachLoopDevices detaches any active kernel loop devices backed by imagePath.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - imagePath: Path to the sparse image file whose loop bindings should be detached.
//
// Example usage:
//
//	detachLoopDevices(ctx, "/var/lib/mc-platform/images/1.img")
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
		if dev == "" {
			continue
		}
		// Ensure node exists in container /dev so detaching still works after container restarts.
		_ = ensureLoopDeviceNodeForPath(dev)
		_ = exec.CommandContext(ctx, "losetup", "-d", dev).Run()
	}
}

func unmountDir(ctx context.Context, mountDir string) error {
	source, mounted, err := findMountSource(mountDir)
	if err != nil {
		return fmt.Errorf("failed checking mount state for %s: %w", mountDir, err)
	}
	if !mounted {
		return nil
	}

	// Ensure the backing /dev/loopN node exists before umount -d attempts to detach it.
	_ = ensureLoopDeviceNodeForPath(source)

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

type mountInfoEntry struct {
	mountPoint string
	source     string
}

func isMountPoint(target string) (bool, error) {
	_, mounted, err := findMountSource(target)
	return mounted, err
}

func findMountSource(target string) (string, bool, error) {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		absTarget = filepath.Clean(target)
	}
	resolvedTarget := absTarget
	if eval, err := filepath.EvalSymlinks(absTarget); err == nil {
		resolvedTarget = eval
	}

	entries, err := readMountInfo()
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		cleanMP := filepath.Clean(entry.mountPoint)
		if cleanMP == absTarget || cleanMP == resolvedTarget {
			return entry.source, true, nil
		}
	}
	return "", false, nil
}

func readMountPoints() ([]string, error) {
	entries, err := readMountInfo()
	if err != nil {
		return nil, err
	}
	mounts := make([]string, 0, len(entries))
	for _, entry := range entries {
		mounts = append(mounts, entry.mountPoint)
	}
	return mounts, nil
}

func readMountInfo() ([]mountInfoEntry, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var entries []mountInfoEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if entry, ok := parseMountInfoLine(scanner.Text()); ok {
			entries = append(entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// parseMountInfoLine extracts the mount point and mount source from a single /proc/self/mountinfo line.
//
// Arguments:
//   - line: Raw line from /proc/self/mountinfo.
//
// Example usage:
//
//	entry, ok := parseMountInfoLine("412 105 7:9 / /var/lib/mc-platform/mounts/9 rw - ext4 /dev/loop9 rw")
func parseMountInfoLine(line string) (mountInfoEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return mountInfoEntry{}, false
	}

	entry := mountInfoEntry{
		mountPoint: unescapeMountInfoPath(fields[4]),
	}
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" && i+2 < len(fields) {
			entry.source = unescapeMountInfoPath(fields[i+2])
			break
		}
	}
	return entry, true
}

// parseLoopDeviceIndex extracts the numeric loop minor index from a /dev/loopN path.
//
// Arguments:
//   - devPath: Device path string such as "/dev/loop12".
//
// Example usage:
//
//	idx, ok := parseLoopDeviceIndex("/dev/loop12")
func parseLoopDeviceIndex(devPath string) (int, bool) {
	if !strings.HasPrefix(devPath, loopDevicePathPrefix) {
		return 0, false
	}
	suffix := strings.TrimPrefix(devPath, loopDevicePathPrefix)
	if suffix == "" {
		return 0, false
	}
	idx, err := strconv.Atoi(suffix)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
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
