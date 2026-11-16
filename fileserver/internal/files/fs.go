package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	// ErrInvalidName indicates a filename or folder name contains invalid path separators or segments.
	ErrInvalidName = errors.New("invalid file or folder name")
	// ErrCannotModifyRoot prevents deleting, downloading, or renaming the mount root itself.
	ErrCannotModifyRoot = errors.New("cannot perform this operation on the root directory")
	// ErrIsDirectory indicates a file download was requested on a directory.
	ErrIsDirectory = errors.New("cannot download a directory")
)

// Entry describes a single file or subdirectory inside a permitted mount root.
type Entry struct {
	Name    string
	AbsPath string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// validateSingleName ensures name is a single safe path segment without traversal or separators.
func validateSingleName(name string) (string, error) {
	cleaned := strings.TrimSpace(name)
	if cleaned == "" || cleaned == "." || cleaned == ".." || strings.ContainsAny(cleaned, "/\\\x00") {
		return "", ErrInvalidName
	}
	return cleaned, nil
}

// joinRel combines a clean relative directory path with a validated child segment.
func joinRel(dirRel, name string) string {
	if dirRel == "" || dirRel == "." {
		return name
	}
	return filepath.Join(dirRel, name)
}

// ListDir opens rootDir via os.Root and returns sorted directory entries for relPath.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - relPath: Clean path relative to rootDir (use "." for rootDir itself).
//
// Example usage:
//
//	entries, err := ListDir("/var/lib/mc-platform/mounts/42", ".")
func ListDir(rootDir, relPath string) ([]Entry, error) {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	if relPath == "" {
		relPath = "."
	}

	dirFile, err := root.Open(relPath)
	if err != nil {
		return nil, fmt.Errorf("failed opening directory: %w", err)
	}
	defer func() { _ = dirFile.Close() }()

	info, err := dirFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed stating directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("requested path is not a directory")
	}

	dirEntries, err := dirFile.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("failed reading directory: %w", err)
	}

	entries := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		var size int64
		var modTime time.Time
		if entryInfo, infoErr := de.Info(); infoErr == nil {
			size = entryInfo.Size()
			modTime = entryInfo.ModTime()
		}

		absPath := filepath.Join(rootDir, joinRel(relPath, de.Name()))
		entries = append(entries, Entry{
			Name:    de.Name(),
			AbsPath: absPath,
			IsDir:   de.IsDir(),
			Size:    size,
			ModTime: modTime,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return entries, nil
}

// SaveUploadedFile writes an uploaded file stream into dirRel inside rootDir using os.Root.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - dirRel: Target directory relative to rootDir.
//   - rawFilename: Uploaded filename (base name is extracted and validated).
//   - src: Stream providing file bytes.
//
// Example usage:
//
//	err := SaveUploadedFile(perm.DirPath, rel, header.Filename, fileReader)
func SaveUploadedFile(rootDir, dirRel, rawFilename string, src io.Reader) error {
	base := filepath.Base(strings.ReplaceAll(rawFilename, "\\", "/"))
	name, err := validateSingleName(base)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	targetRel := joinRel(dirRel, name)
	dst, err := root.OpenFile(targetRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed creating file: %w", err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed writing uploaded file: %w", err)
	}
	return nil
}

// CreateDir creates a subdirectory inside dirRel within rootDir using os.Root.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - dirRel: Parent directory relative to rootDir.
//   - folderName: Name of the new subdirectory.
//
// Example usage:
//
//	err := CreateDir(perm.DirPath, ".", "plugins")
func CreateDir(rootDir, dirRel, folderName string) error {
	name, err := validateSingleName(folderName)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	if err := root.Mkdir(joinRel(dirRel, name), 0755); err != nil {
		return fmt.Errorf("failed creating directory: %w", err)
	}
	return nil
}

// RenameEntry renames a file or directory within its parent directory inside rootDir.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - entryRel: Existing entry path relative to rootDir.
//   - newName: New single-segment entry name.
//
// Example usage:
//
//	err := RenameEntry(perm.DirPath, "server.properties", "server.properties.bak")
func RenameEntry(rootDir, entryRel, newName string) error {
	if entryRel == "" || entryRel == "." {
		return ErrCannotModifyRoot
	}
	cleanNew, err := validateSingleName(newName)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	parent := filepath.Dir(entryRel)
	dstRel := joinRel(parent, cleanNew)
	if err := root.Rename(entryRel, dstRel); err != nil {
		return fmt.Errorf("failed renaming entry: %w", err)
	}
	return nil
}

// DeleteEntry removes a file or directory tree inside rootDir using os.Root.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - entryRel: Target path relative to rootDir (cannot be ".").
//
// Example usage:
//
//	err := DeleteEntry(perm.DirPath, "old-world")
func DeleteEntry(rootDir, entryRel string) error {
	if entryRel == "" || entryRel == "." {
		return ErrCannotModifyRoot
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	if err := root.RemoveAll(entryRel); err != nil {
		return fmt.Errorf("failed deleting entry: %w", err)
	}
	return nil
}

// OpenDownload opens a non-directory file inside rootDir for streaming to the client.
// Callers must invoke the returned cleanup function when finished reading.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - entryRel: File path relative to rootDir.
//
// Example usage:
//
//	f, info, closeFn, err := OpenDownload(perm.DirPath, "server.properties")
func OpenDownload(rootDir, entryRel string) (*os.File, os.FileInfo, func(), error) {
	if entryRel == "" || entryRel == "." {
		return nil, nil, nil, ErrCannotModifyRoot
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed opening mount root: %w", err)
	}

	f, err := root.Open(entryRel)
	if err != nil {
		_ = root.Close()
		return nil, nil, nil, fmt.Errorf("failed opening file: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = root.Close()
		return nil, nil, nil, fmt.Errorf("failed stating file: %w", err)
	}
	if info.IsDir() {
		_ = f.Close()
		_ = root.Close()
		return nil, nil, nil, ErrIsDirectory
	}

	cleanup := func() {
		_ = f.Close()
		_ = root.Close()
	}
	return f, info, cleanup, nil
}
