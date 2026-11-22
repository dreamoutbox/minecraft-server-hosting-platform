package worker

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	backupArchiveContentType = "application/gzip"
	// DefaultMaxImagePartBytes caps each compressed .img tar.gz part at 1 GiB.
	DefaultMaxImagePartBytes int64 = 1024 * 1024 * 1024
	gzipMagicByte0                 = 0x1f
	gzipMagicByte1                 = 0x8b
)

// UploadVolumeBackup archives volumeID's mounted files as tar.gz to a temp file and PUTs it to presignedURL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Identifier of the server volume to archive.
//   - presignedURL: Pre-signed S3 HTTP PUT URL (worker holds no S3 credentials).
//
// Example usage:
//
//	sizeBytes, err := vm.UploadVolumeBackup(ctx, "42", putURL)
func (m *LocalVolumeManager) UploadVolumeBackup(ctx context.Context, volumeID string, presignedURL string) (int64, error) {
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("mc-vol-%s-*.tar.gz", volumeID))
	if err != nil {
		return 0, fmt.Errorf("failed creating temp backup archive: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	gzWriter := gzip.NewWriter(tmpFile)
	if err := m.ExportVolume(ctx, volumeID, gzWriter); err != nil {
		_ = gzWriter.Close()
		_ = tmpFile.Close()
		return 0, err
	}
	if err := gzWriter.Close(); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("failed finalizing gzip archive: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("failed closing temp backup archive: %w", err)
	}

	sizeBytes, err := putFileToPresignedURL(ctx, tmpPath, presignedURL, backupArchiveContentType)
	if err != nil {
		return 0, err
	}
	return sizeBytes, nil
}

// RestoreVolumeBackup downloads a backup archive via HTTP GET from presignedURL and unpacks it into volumeID.
// Both gzip-compressed (.tar.gz) and legacy uncompressed (.tar) streams are supported automatically.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Target server volume identifier.
//   - presignedURL: Pre-signed S3 HTTP GET URL.
//
// Example usage:
//
//	err := vm.RestoreVolumeBackup(ctx, "42", getURL)
func (m *LocalVolumeManager) RestoreVolumeBackup(ctx context.Context, volumeID string, presignedURL string) error {
	body, err := getFromPresignedURL(ctx, presignedURL)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	return m.ImportVolume(ctx, volumeID, body)
}

// unwrapOptionalGzip inspects the first two bytes for the gzip header and returns a decompressed reader when present.
//
// Arguments:
//   - r: Incoming archive stream (plain tar or gzipped tar).
//
// Example usage:
//
//	reader, closeFn, err := unwrapOptionalGzip(body)
func unwrapOptionalGzip(r io.Reader) (io.Reader, func() error, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err == nil && len(magic) == 2 && magic[0] == gzipMagicByte0 && magic[1] == gzipMagicByte1 {
		gzReader, gzErr := gzip.NewReader(br)
		if gzErr != nil {
			return nil, nil, fmt.Errorf("failed opening gzip backup stream: %w", gzErr)
		}
		return gzReader, gzReader.Close, nil
	}
	return br, func() error { return nil }, nil
}

// putFileToPresignedURL streams a local file via HTTP PUT with exact Content-Length to a presigned S3 URL.
//
// Arguments:
//   - ctx: Operation cancellation context controlling upload timeout.
//   - filePath: Path to the local file to upload.
//   - presignedURL: Pre-signed HTTP PUT target URL.
//   - contentType: MIME Content-Type header matching the presigned signature.
//
// Example usage:
//
//	n, err := putFileToPresignedURL(ctx, "/tmp/part-001.tar.gz", putURL, "application/gzip")
func putFileToPresignedURL(ctx context.Context, filePath string, presignedURL string, contentType string) (int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed opening file %s for upload: %w", filePath, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed stating file %s: %w", filePath, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, f)
	if err != nil {
		return 0, fmt.Errorf("failed building presigned PUT request: %w", err)
	}
	req.ContentLength = info.Size()
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Rely on caller context deadline rather than a fixed client timeout for large archives.
	transferClient := &http.Client{}
	resp, err := transferClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("presigned PUT upload failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("presigned PUT rejected with status %d: %s", resp.StatusCode, string(errBody))
	}
	return info.Size(), nil
}

// getFromPresignedURL executes an HTTP GET against a presigned S3 URL and returns the open response body.
//
// Arguments:
//   - ctx: Operation cancellation context controlling download timeout.
//   - presignedURL: Pre-signed HTTP GET source URL.
//
// Example usage:
//
//	rc, err := getFromPresignedURL(ctx, getURL)
func getFromPresignedURL(ctx context.Context, presignedURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, presignedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed building presigned GET request: %w", err)
	}

	transferClient := &http.Client{}
	resp, err := transferClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("presigned GET download failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("presigned GET rejected with status %d: %s", resp.StatusCode, string(errBody))
	}
	return resp.Body, nil
}

// UploadImageBackup streams volumeID's ext4 .img file through tar+gzip, splitting output into
// <= maxPartBytes chunks (default 1 GiB) and uploading each chunk via its presigned PUT URL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Server/volume identifier matching <baseDir>/images/<volumeID>.img.
//   - presignedURLs: Pre-minted ordered S3 PUT URLs for parts 1..N.
//   - maxPartBytes: Maximum bytes per compressed part (defaults to 1 GiB if <= 0).
//
// Example usage:
//
//	res, err := vm.UploadImageBackup(ctx, "42", putURLs, 0)
func (m *LocalVolumeManager) UploadImageBackup(ctx context.Context, volumeID string, presignedURLs []string, maxPartBytes int64) (*UploadImageBackupResponse, error) {
	if len(presignedURLs) == 0 {
		return nil, errors.New("at least one presigned PUT URL is required")
	}
	if maxPartBytes <= 0 {
		maxPartBytes = DefaultMaxImagePartBytes
	}

	m.mu.RLock()
	_, imagePath := m.resolvePathsLocked(volumeID)
	m.mu.RUnlock()

	imgFile, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed opening volume image %s: %w", imagePath, err)
	}
	defer func() { _ = imgFile.Close() }()

	imgInfo, err := imgFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed stating volume image %s: %w", imagePath, err)
	}

	uploader := newChunkedPartUploader(ctx, volumeID, presignedURLs, maxPartBytes)
	defer uploader.Cleanup()

	gzWriter := gzip.NewWriter(uploader)
	tarWriter := tar.NewWriter(gzWriter)

	hdr := &tar.Header{
		Name:    filepath.Base(imagePath),
		Mode:    0644,
		Size:    imgInfo.Size(),
		ModTime: imgInfo.ModTime(),
	}
	if err := tarWriter.WriteHeader(hdr); err != nil {
		return nil, fmt.Errorf("failed writing image tar header: %w", err)
	}
	if _, err := copyWithContext(ctx, tarWriter, imgFile); err != nil {
		return nil, fmt.Errorf("failed archiving image %s: %w", imagePath, err)
	}
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed closing image tar stream: %w", err)
	}
	if err := gzWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed closing image gzip stream: %w", err)
	}
	if err := uploader.FlushFinal(); err != nil {
		return nil, err
	}

	return &UploadImageBackupResponse{
		PartsUploaded: len(uploader.partSizes),
		PartSizes:     uploader.partSizes,
		TotalBytes:    uploader.totalBytes,
	}, nil
}

// RestoreImageBackup downloads ordered tar.gz part(s) from presigned GET URLs, merges and
// decompresses them into a temp image file, then unmounts and atomically replaces <volumeID>.img.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Server/volume identifier matching <baseDir>/images/<volumeID>.img.
//   - presignedURLs: Ordered presigned S3 GET URLs for parts 1..N.
//
// Example usage:
//
//	sizeBytes, err := vm.RestoreImageBackup(ctx, "42", getURLs)
func (m *LocalVolumeManager) RestoreImageBackup(ctx context.Context, volumeID string, presignedURLs []string) (int64, error) {
	if len(presignedURLs) == 0 {
		return 0, errors.New("at least one presigned GET URL is required")
	}

	m.mu.RLock()
	mountDir, imagePath := m.resolvePathsLocked(volumeID)
	m.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(imagePath), 0755); err != nil {
		return 0, fmt.Errorf("failed creating images directory: %w", err)
	}

	tmpImgPath := imagePath + ".restore.tmp"
	_ = os.Remove(tmpImgPath)
	defer func() { _ = os.Remove(tmpImgPath) }()

	// Stream and extract to temp file first so download/archive errors never clobber the live image.
	restoredSize, err := extractPresignedImageArchive(ctx, presignedURLs, tmpImgPath)
	if err != nil {
		return 0, err
	}

	m.mu.Lock()
	if m.useLoopMount {
		if err := unmountDir(ctx, mountDir); err != nil {
			m.mu.Unlock()
			return 0, fmt.Errorf("failed unmounting volume %s before image replace: %w", volumeID, err)
		}
		detachLoopDevices(ctx, imagePath)
	}

	if err := os.Rename(tmpImgPath, imagePath); err != nil {
		m.mu.Unlock()
		return 0, fmt.Errorf("failed replacing volume image %s: %w", imagePath, err)
	}

	if m.useLoopMount {
		// Best-effort ext4 journal replay/repair for images captured while mounted rw.
		_ = exec.CommandContext(ctx, "e2fsck", "-p", imagePath).Run()
		if err := os.MkdirAll(mountDir, 0755); err != nil {
			m.mu.Unlock()
			return 0, fmt.Errorf("failed creating mount directory %s: %w", mountDir, err)
		}
		if err := mountLoopImage(ctx, imagePath, mountDir); err != nil {
			m.mu.Unlock()
			return 0, fmt.Errorf("failed remounting restored image %s: %w", imagePath, err)
		}
	}
	m.volumeMountMap[volumeID] = mountDir
	m.volumeImageMap[volumeID] = imagePath
	m.mu.Unlock()

	return restoredSize, nil
}

// chunkedPartUploader buffers incoming compressed stream bytes into <= maxPartBytes temp files,
// uploading and removing each completed part immediately to keep worker temp disk bounded.
type chunkedPartUploader struct {
	ctx           context.Context
	volumeID      string
	presignedURLs []string
	maxPartBytes  int64

	curFile    *os.File
	curPath    string
	curWritten int64
	partIndex  int
	partSizes  []int64
	totalBytes int64
}

func newChunkedPartUploader(ctx context.Context, volumeID string, presignedURLs []string, maxPartBytes int64) *chunkedPartUploader {
	return &chunkedPartUploader{
		ctx:           ctx,
		volumeID:      volumeID,
		presignedURLs: presignedURLs,
		maxPartBytes:  maxPartBytes,
	}
}

func (u *chunkedPartUploader) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if err := u.ctx.Err(); err != nil {
			return total, err
		}
		if u.curFile == nil {
			if err := u.openNextPart(); err != nil {
				return total, err
			}
		}

		remaining := u.maxPartBytes - u.curWritten
		chunk := p
		if int64(len(chunk)) > remaining {
			chunk = p[:remaining]
		}

		n, err := u.curFile.Write(chunk)
		total += n
		u.curWritten += int64(n)
		if err != nil {
			return total, fmt.Errorf("failed writing backup part %d: %w", u.partIndex+1, err)
		}
		p = p[n:]

		if u.curWritten >= u.maxPartBytes {
			if err := u.uploadCurrentPart(); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func (u *chunkedPartUploader) FlushFinal() error {
	if u.curFile == nil {
		return nil
	}
	return u.uploadCurrentPart()
}

func (u *chunkedPartUploader) Cleanup() {
	if u.curFile != nil {
		_ = u.curFile.Close()
		u.curFile = nil
	}
	if u.curPath != "" {
		_ = os.Remove(u.curPath)
		u.curPath = ""
	}
}

func (u *chunkedPartUploader) openNextPart() error {
	if u.partIndex >= len(u.presignedURLs) {
		return fmt.Errorf("archive exceeded available presigned URLs (%d)", len(u.presignedURLs))
	}
	f, err := os.CreateTemp("", fmt.Sprintf("mc-img-%s-part-%03d-*.tar.gz", u.volumeID, u.partIndex+1))
	if err != nil {
		return fmt.Errorf("failed creating temp image backup part: %w", err)
	}
	u.curFile = f
	u.curPath = f.Name()
	u.curWritten = 0
	return nil
}

func (u *chunkedPartUploader) uploadCurrentPart() error {
	path := u.curPath
	f := u.curFile
	u.curFile = nil
	u.curPath = ""
	u.curWritten = 0

	defer func() { _ = os.Remove(path) }()

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed closing temp image backup part %d: %w", u.partIndex+1, err)
	}

	sizeBytes, err := putFileToPresignedURL(u.ctx, path, u.presignedURLs[u.partIndex], backupArchiveContentType)
	if err != nil {
		return fmt.Errorf("failed uploading image backup part %d: %w", u.partIndex+1, err)
	}

	u.partSizes = append(u.partSizes, sizeBytes)
	u.totalBytes += sizeBytes
	u.partIndex++
	return nil
}

// sequentialPartsReader concatenates multiple presigned GET part responses into a single stream.
type sequentialPartsReader struct {
	ctx     context.Context
	urls    []string
	index   int
	current io.ReadCloser
}

func (r *sequentialPartsReader) Read(p []byte) (int, error) {
	for {
		if r.current == nil {
			if r.index >= len(r.urls) {
				return 0, io.EOF
			}
			body, err := getFromPresignedURL(r.ctx, r.urls[r.index])
			if err != nil {
				return 0, fmt.Errorf("failed fetching image backup part %d: %w", r.index+1, err)
			}
			r.current = body
			r.index++
		}

		n, err := r.current.Read(p)
		if err == io.EOF {
			_ = r.current.Close()
			r.current = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *sequentialPartsReader) Close() error {
	if r.current != nil {
		err := r.current.Close()
		r.current = nil
		return err
	}
	return nil
}

func extractPresignedImageArchive(ctx context.Context, presignedURLs []string, destPath string) (int64, error) {
	partsReader := &sequentialPartsReader{ctx: ctx, urls: presignedURLs}
	defer func() { _ = partsReader.Close() }()

	gzReader, err := gzip.NewReader(partsReader)
	if err != nil {
		return 0, fmt.Errorf("failed opening merged gzip stream: %w", err)
	}
	defer func() { _ = gzReader.Close() }()

	tarReader := tar.NewReader(gzReader)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			return 0, errors.New("image backup archive did not contain a regular image file")
		}
		if err != nil {
			return 0, fmt.Errorf("failed reading image tar header: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
		if err != nil {
			return 0, fmt.Errorf("failed creating temp restore image %s: %w", destPath, err)
		}
		written, copyErr := copyWithContext(ctx, outFile, tarReader)
		if copyErr != nil {
			_ = outFile.Close()
			return 0, fmt.Errorf("failed extracting restored image: %w", copyErr)
		}
		if err := outFile.Sync(); err != nil {
			_ = outFile.Close()
			return 0, fmt.Errorf("failed syncing restored image: %w", err)
		}
		if err := outFile.Close(); err != nil {
			return 0, fmt.Errorf("failed closing restored image: %w", err)
		}
		return written, nil
	}
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 256*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, rErr := src.Read(buf)
		if nr > 0 {
			nw, wErr := dst.Write(buf[:nr])
			written += int64(nw)
			if wErr != nil {
				return written, wErr
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if rErr == io.EOF {
			return written, nil
		}
		if rErr != nil {
			return written, rErr
		}
	}
}
