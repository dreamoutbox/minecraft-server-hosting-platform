package worker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// VolumeManager abstracts loop-mounted block storage lifecycle and tar streaming.
type VolumeManager interface {
	CreateVolume(ctx context.Context, volumeID string, diskLimitGB int) (mountPath string, imagePath string, err error)
	DeleteVolume(ctx context.Context, volumeID string) error
	ExportVolume(ctx context.Context, volumeID string, w io.Writer) error
	ImportVolume(ctx context.Context, volumeID string, r io.Reader) error
	GetVolumePath(volumeID string) (mountPath string, exists bool)
}

// LocalVolumeManager manages sparse image files and directory mounts on the node host.
type LocalVolumeManager struct {
	baseDir         string
	useLoopMount    bool
	mu              sync.RWMutex
	volumeMountMap  map[string]string
	volumeImageMap  map[string]string
}

// NewLocalVolumeManager initializes a volume manager rooted at the specified base directory.
//
// Arguments:
//   - baseDir: Filesystem root where sparse images and mount directories reside.
//   - useLoopMount: Whether to execute raw loop device mount commands (requires SYS_ADMIN).
//
// Example usage:
//   vm := worker.NewLocalVolumeManager("./data/volumes", false)
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
//   mount, img, err := vm.CreateVolume(ctx, "server-42", 10)
func (m *LocalVolumeManager) CreateVolume(ctx context.Context, volumeID string, diskLimitGB int) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	imagesDir := filepath.Join(m.baseDir, "images")
	mountsDir := filepath.Join(m.baseDir, "mounts", volumeID)

	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed creating images directory: %w", err)
	}
	if err := os.MkdirAll(mountsDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed creating mount directory: %w", err)
	}

	imagePath := filepath.Join(imagesDir, fmt.Sprintf("%s.img", volumeID))
	f, err := os.Create(imagePath)
	if err != nil {
		return "", "", fmt.Errorf("failed creating sparse file: %w", err)
	}
	sizeBytes := int64(diskLimitGB) * 1024 * 1024 * 1024
	if err := f.Truncate(sizeBytes); err != nil {
		_ = f.Close()
		return "", "", fmt.Errorf("failed truncating sparse file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", "", fmt.Errorf("failed closing sparse image: %w", err)
	}

	if m.useLoopMount {
		mkfsCmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", imagePath)
		if output, err := mkfsCmd.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("mkfs.ext4 failed (%s): %w", string(output), err)
		}

		mountCmd := exec.CommandContext(ctx, "mount", "-o", "loop", imagePath, mountsDir)
		if output, err := mountCmd.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("mount -o loop failed (%s): %w", string(output), err)
		}
	}

	m.volumeMountMap[volumeID] = mountsDir
	m.volumeImageMap[volumeID] = imagePath

	return mountsDir, imagePath, nil
}

// DeleteVolume unmounts active loop mounts and cleans up disk files for the volume.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Unique volume identifier.
//
// Example usage:
//   err := vm.DeleteVolume(ctx, "server-42")
func (m *LocalVolumeManager) DeleteVolume(ctx context.Context, volumeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mountDir, mountOk := m.volumeMountMap[volumeID]
	if !mountOk {
		mountDir = filepath.Join(m.baseDir, "mounts", volumeID)
	}

	imagePath, imgOk := m.volumeImageMap[volumeID]
	if !imgOk {
		imagePath = filepath.Join(m.baseDir, "images", fmt.Sprintf("%s.img", volumeID))
	}

	if m.useLoopMount {
		umountCmd := exec.CommandContext(ctx, "umount", mountDir)
		_ = umountCmd.Run() // ignore if already unmounted
	}

	_ = os.RemoveAll(mountDir)
	_ = os.Remove(imagePath)

	delete(m.volumeMountMap, volumeID)
	delete(m.volumeImageMap, volumeID)

	return nil
}

// ExportVolume streams a tar archive of all files contained within the volume's mount point.
//
// Arguments:
//   - ctx: Operation context for cancellation during long transfers.
//   - volumeID: Identifier of the volume to read.
//   - w: Output writer receiving the streaming tar archive.
//
// Example usage:
//   err := vm.ExportVolume(ctx, "server-42", responseWriter)
func (m *LocalVolumeManager) ExportVolume(ctx context.Context, volumeID string, w io.Writer) error {
	m.mu.RLock()
	mountDir, ok := m.volumeMountMap[volumeID]
	if !ok {
		mountDir = filepath.Join(m.baseDir, "mounts", volumeID)
	}
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
//   err := vm.ImportVolume(ctx, "server-42", requestBody)
func (m *LocalVolumeManager) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	m.mu.Lock()
	mountDir, ok := m.volumeMountMap[volumeID]
	if !ok {
		mountDir = filepath.Join(m.baseDir, "mounts", volumeID)
	}
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
//   path, ok := vm.GetVolumePath("server-42")
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
