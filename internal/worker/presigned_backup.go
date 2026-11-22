package worker

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	backupArchiveContentType = "application/gzip"
	gzipMagicByte0           = 0x1f
	gzipMagicByte1           = 0x8b
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
