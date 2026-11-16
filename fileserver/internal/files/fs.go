package files

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EditableExtension represents a file extension editable in the web modal editor.
type EditableExtension string

const (
	ExtTXT        EditableExtension = ".txt"
	ExtJSON       EditableExtension = ".json"
	ExtYML        EditableExtension = ".yml"
	ExtProperties EditableExtension = ".properties"
)

var allowedEditableExtensions = map[EditableExtension]bool{
	ExtTXT:        true,
	ExtJSON:       true,
	ExtYML:        true,
	ExtProperties: true,
}

var (
	// ErrInvalidName indicates a filename or folder name contains invalid path separators or segments.
	ErrInvalidName = errors.New("invalid file or folder name")
	// ErrCannotModifyRoot prevents deleting, downloading, or renaming the mount root itself.
	ErrCannotModifyRoot = errors.New("cannot perform this operation on the root directory")
	// ErrIsDirectory indicates a file operation was requested on a directory.
	ErrIsDirectory = errors.New("requested path is a directory")
	// ErrNotDirectory indicates a directory operation was requested on a non-directory.
	ErrNotDirectory = errors.New("requested path is not a directory")
	// ErrNotEditable indicates the file extension is not supported by the web editor.
	ErrNotEditable = errors.New("only .txt, .json, .yml, and .properties files can be edited")
	// ErrFileTooLarge indicates the file exceeds the maximum editable size.
	ErrFileTooLarge = errors.New("file is too large to edit on the web")
	// ErrAlreadyExists indicates a target file already exists.
	ErrAlreadyExists = errors.New("file already exists")
	// ErrBinaryFile indicates a file appears to contain binary NUL bytes.
	ErrBinaryFile = errors.New("binary files cannot be edited as text")
)

// Entry describes a single file or subdirectory inside a permitted mount root.
type Entry struct {
	Name     string
	AbsPath  string
	IsDir    bool
	Editable bool
	Size     int64
	ModTime  time.Time
}

// IsEditableFile reports whether filename has an extension supported by the web editor.
//
// Arguments:
//   - name: File name or path whose extension is checked case-insensitively.
//
// Example usage:
//
//	if files.IsEditableFile("server.properties") { ... }
func IsEditableFile(name string) bool {
	ext := EditableExtension(strings.ToLower(filepath.Ext(strings.TrimSpace(name))))
	return allowedEditableExtensions[ext]
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
			Name:     de.Name(),
			AbsPath:  absPath,
			IsDir:    de.IsDir(),
			Editable: !de.IsDir() && IsEditableFile(de.Name()),
			Size:     size,
			ModTime:  modTime,
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

// CreateFile creates a new empty file inside dirRel within rootDir without overwriting existing files.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - dirRel: Target directory relative to rootDir.
//   - fileName: Name of the new file to create.
//
// Example usage:
//
//	err := CreateFile(perm.DirPath, ".", "whitelist.json")
func CreateFile(rootDir, dirRel, fileName string) error {
	name, err := validateSingleName(fileName)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	dst, err := root.OpenFile(joinRel(dirRel, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("failed creating file: %w", err)
	}
	return dst.Close()
}

func validateEditableTarget(entryRel string) error {
	if entryRel == "" || entryRel == "." {
		return ErrCannotModifyRoot
	}
	if !IsEditableFile(entryRel) {
		return ErrNotEditable
	}
	return nil
}

// ReadEditableFile reads a supported text config file within rootDir up to maxBytes.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - entryRel: File path relative to rootDir.
//   - maxBytes: Maximum file size permitted in bytes.
//
// Example usage:
//
//	content, err := ReadEditableFile(perm.DirPath, "server.properties", 2<<20)
func ReadEditableFile(rootDir, entryRel string, maxBytes int64) (string, error) {
	if err := validateEditableTarget(entryRel); err != nil {
		return "", err
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return "", fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(entryRel)
	if err != nil {
		return "", fmt.Errorf("failed opening file: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("failed stating file: %w", err)
	}
	if info.IsDir() {
		return "", ErrIsDirectory
	}
	if info.Size() > maxBytes {
		return "", ErrFileTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed reading file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return "", ErrFileTooLarge
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", ErrBinaryFile
	}
	return string(data), nil
}

// SaveEditableFile overwrites an existing supported text config file inside rootDir.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - entryRel: File path relative to rootDir.
//   - content: New UTF-8 file text content to write.
//   - maxBytes: Maximum allowed content byte length.
//
// Example usage:
//
//	err := SaveEditableFile(perm.DirPath, "server.properties", updatedText, 2<<20)
func SaveEditableFile(rootDir, entryRel, content string, maxBytes int64) error {
	if err := validateEditableTarget(entryRel); err != nil {
		return err
	}
	if int64(len(content)) > maxBytes {
		return ErrFileTooLarge
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("failed opening mount root: %w", err)
	}
	defer func() { _ = root.Close() }()

	info, err := root.Stat(entryRel)
	if err != nil {
		return fmt.Errorf("failed stating file: %w", err)
	}
	if info.IsDir() {
		return ErrIsDirectory
	}

	dst, err := root.OpenFile(entryRel, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed saving file: %w", err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.WriteString(dst, content); err != nil {
		return fmt.Errorf("failed writing file: %w", err)
	}
	return nil
}

// OpenDirForZip validates a directory inside rootDir and prepares a streaming zip writer closure.
// Callers must invoke cleanup when finished.
//
// Arguments:
//   - rootDir: Absolute root directory granted to the user.
//   - entryRel: Directory path relative to rootDir ("." for root itself).
//
// Example usage:
//
//	zipName, writeZip, cleanup, err := OpenDirForZip(perm.DirPath, "world")
func OpenDirForZip(rootDir, entryRel string) (string, func(io.Writer) error, func(), error) {
	if entryRel == "" {
		entryRel = "."
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed opening mount root: %w", err)
	}

	dirFile, err := root.Open(entryRel)
	if err != nil {
		_ = root.Close()
		return "", nil, nil, fmt.Errorf("failed opening directory: %w", err)
	}

	info, err := dirFile.Stat()
	_ = dirFile.Close()
	if err != nil {
		_ = root.Close()
		return "", nil, nil, fmt.Errorf("failed stating directory: %w", err)
	}
	if !info.IsDir() {
		_ = root.Close()
		return "", nil, nil, ErrNotDirectory
	}

	baseName := filepath.Base(rootDir)
	if entryRel != "." {
		baseName = filepath.Base(entryRel)
	}
	if baseName == "" || baseName == "." || baseName == string(filepath.Separator) {
		baseName = "archive"
	}

	writeZip := func(w io.Writer) error {
		zw := zip.NewWriter(w)
		if err := addDirToZip(root, entryRel, "", zw); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	}

	cleanup := func() {
		_ = root.Close()
	}
	return baseName + ".zip", writeZip, cleanup, nil
}

func addDirToZip(root *os.Root, diskRel, zipPrefix string, zw *zip.Writer) error {
	dirFile, err := root.Open(diskRel)
	if err != nil {
		return fmt.Errorf("failed opening directory %s: %w", diskRel, err)
	}
	entries, err := dirFile.ReadDir(-1)
	_ = dirFile.Close()
	if err != nil {
		return fmt.Errorf("failed reading directory %s: %w", diskRel, err)
	}

	for _, de := range entries {
		// Skip symlinks so folder archives remain confined to real files within rootDir.
		if de.Type()&os.ModeSymlink != 0 {
			continue
		}

		childDiskRel := joinRel(diskRel, de.Name())
		childZipPath := de.Name()
		if zipPrefix != "" {
			childZipPath = zipPrefix + "/" + de.Name()
		}

		info, err := de.Info()
		if err != nil {
			return fmt.Errorf("failed stating %s: %w", childDiskRel, err)
		}

		if de.IsDir() {
			hdr, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			hdr.Name = childZipPath + "/"
			hdr.Method = zip.Store
			if _, err := zw.CreateHeader(hdr); err != nil {
				return err
			}
			if err := addDirToZip(root, childDiskRel, childZipPath, zw); err != nil {
				return err
			}
			continue
		}

		if err := addFileToZip(root, childDiskRel, childZipPath, info, zw); err != nil {
			return err
		}
	}
	return nil
}

func addFileToZip(root *os.Root, diskRel, zipPath string, info os.FileInfo, zw *zip.Writer) error {
	f, err := root.Open(diskRel)
	if err != nil {
		return fmt.Errorf("failed opening file %s: %w", diskRel, err)
	}
	defer func() { _ = f.Close() }()

	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = zipPath
	hdr.Method = zip.Deflate

	entryWriter, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	if _, err := io.Copy(entryWriter, f); err != nil {
		return fmt.Errorf("failed writing zip entry %s: %w", zipPath, err)
	}
	return nil
}
