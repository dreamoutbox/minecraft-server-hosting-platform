package nodemanagerclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

// PutPresignedBytes uploads an in-memory byte slice via HTTP PUT to a presigned S3 URL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - presignedURL: Pre-signed HTTP PUT target URL.
//   - data: Payload bytes to upload.
//   - contentType: Optional MIME Content-Type header matching the presigned signature.
//
// Example usage:
//
//	n, err := nodemanagerclient.PutPresignedBytes(ctx, putURL, payload, "application/gzip")
func PutPresignedBytes(ctx context.Context, presignedURL string, data []byte, contentType string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("failed building presigned PUT request: %w", err)
	}
	req.ContentLength = int64(len(data))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("presigned PUT upload failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("presigned PUT rejected with status %d: %s", resp.StatusCode, string(errBody))
	}
	return int64(len(data)), nil
}

// GetPresignedBytes downloads an object into memory via HTTP GET from a presigned S3 URL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - presignedURL: Pre-signed HTTP GET source URL.
//
// Example usage:
//
//	data, err := nodemanagerclient.GetPresignedBytes(ctx, getURL)
func GetPresignedBytes(ctx context.Context, presignedURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, presignedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed building presigned GET request: %w", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("presigned GET download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("presigned GET rejected with status %d: %s", resp.StatusCode, string(errBody))
	}
	return io.ReadAll(resp.Body)
}
