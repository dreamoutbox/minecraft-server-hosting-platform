package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSeaweedFSIntegration(t *testing.T) {
	// Check if SeaweedFS is reachable on localhost:8333
	resp, err := http.Get("http://localhost:8333/")
	if err != nil {
		t.Skipf("skipping SeaweedFS test: emulator unreachable: %v", err)
		return
	}
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := S3Config{
		Endpoint:     "http://localhost:8333",
		Region:       "us-east-1",
		Bucket:       fmt.Sprintf("test-bucket-%d", time.Now().UnixNano()),
		AccessKey:    "dev",
		SecretKey:    "dev",
		UsePathStyle: true,
	}

	provider, err := NewS3StorageProvider(ctx, cfg)
	if err != nil {
		t.Fatalf("failed initializing S3 provider: %v", err)
	}

	testKey := "backups/server-42/world.tar"
	testData := []byte("fake-world-tarball-binary-content-12345")

	// 1. Put
	if err := provider.Put(ctx, testKey, testData); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// 2. Get
	retrieved, err := provider.Get(ctx, testKey)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(retrieved, testData) {
		t.Errorf("retrieved bytes do not match uploaded bytes: got %s, want %s", string(retrieved), string(testData))
	}

	// 3. List
	keys, err := provider.List(ctx, "backups/server-42/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == testKey {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected key %s in list output, got: %+v", testKey, keys)
	}

	// 4. Delete
	if err := provider.Delete(ctx, testKey); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 5. Verify deleted
	_, err = provider.Get(ctx, testKey)
	if err == nil {
		t.Error("expected error getting deleted key, got nil")
	}

	// 6. PresignPut + PresignGet end-to-end against SeaweedFS
	presignKey := "backups/server-42/presigned.tar.gz"
	presignData := []byte("presigned-seaweedfs-payload")
	putURL, err := provider.PresignPut(ctx, presignKey, time.Minute, "application/gzip")
	if err != nil {
		t.Fatalf("PresignPut failed: %v", err)
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(presignData))
	if err != nil {
		t.Fatalf("failed building PUT request: %v", err)
	}
	putReq.Header.Set("Content-Type", "application/gzip")
	putReq.ContentLength = int64(len(presignData))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("presigned PUT failed: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		t.Fatalf("presigned PUT returned status %d", putResp.StatusCode)
	}

	getURL, err := provider.PresignGet(ctx, presignKey, time.Minute)
	if err != nil {
		t.Fatalf("PresignGet failed: %v", err)
	}
	getResp, err := http.Get(getURL)
	if err != nil {
		t.Fatalf("presigned GET failed: %v", err)
	}
	gotBytes, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if err != nil {
		t.Fatalf("failed reading presigned GET body: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET returned status %d", getResp.StatusCode)
	}
	if !bytes.Equal(gotBytes, presignData) {
		t.Errorf("presigned GET body mismatch: got %q, want %q", string(gotBytes), string(presignData))
	}

	// Verify SigV4 presigned URL parameters and distinct PUT vs GET signatures
	parsedPut, err := url.Parse(putURL)
	if err != nil {
		t.Fatalf("failed parsing putURL: %v", err)
	}
	parsedGet, err := url.Parse(getURL)
	if err != nil {
		t.Fatalf("failed parsing getURL: %v", err)
	}
	for name, q := range map[string]url.Values{"put": parsedPut.Query(), "get": parsedGet.Query()} {
		if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
			t.Errorf("%s URL missing AWS4-HMAC-SHA256 algorithm: %s", name, q.Get("X-Amz-Algorithm"))
		}
		if q.Get("X-Amz-Expires") != "60" {
			t.Errorf("%s URL expected X-Amz-Expires=60, got %s", name, q.Get("X-Amz-Expires"))
		}
		if q.Get("X-Amz-Signature") == "" {
			t.Errorf("%s URL missing X-Amz-Signature", name)
		}
	}
	if !strings.Contains(parsedPut.Query().Get("X-Amz-SignedHeaders"), "content-type") {
		t.Errorf("PUT URL should bind content-type in X-Amz-SignedHeaders, got %s", parsedPut.Query().Get("X-Amz-SignedHeaders"))
	}
	if parsedPut.Query().Get("X-Amz-Signature") == parsedGet.Query().Get("X-Amz-Signature") {
		t.Error("expected distinct SigV4 signatures for PUT vs GET presigned URLs")
	}

	_ = provider.Delete(ctx, presignKey)
}

func TestMemoryStorageProvider(t *testing.T) {
	ctx := context.Background()
	provider := NewMemoryStorageProvider()

	key := "test/memory.txt"
	data := []byte("memory-content")

	if err := provider.Put(ctx, key, data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	res, err := provider.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(res) != string(data) {
		t.Errorf("got %s, want %s", string(res), string(data))
	}

	keys, err := provider.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Errorf("unexpected list output: %+v", keys)
	}

	if err := provider.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = provider.Get(ctx, key)
	if err == nil {
		t.Error("expected error getting deleted memory key, got nil")
	}
}

func TestMemoryStoragePresignedURLs(t *testing.T) {
	ctx := context.Background()
	provider := NewMemoryStorageProvider()
	defer func() { _ = provider.Close() }()

	key := "backups/server-1/world.tar.gz"
	payload := []byte("compressed-world-bytes")

	putURL, err := provider.PresignPut(ctx, key, time.Minute, "application/gzip")
	if err != nil {
		t.Fatalf("PresignPut failed: %v", err)
	}

	// Reject GET/DELETE on a PUT-only URL
	for _, disallowed := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequestWithContext(ctx, disallowed, putURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s request failed: %v", disallowed, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 for %s on PUT url, got %d", disallowed, resp.StatusCode)
		}
	}

	// Upload via PUT with matching Content-Type
	putReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(payload))
	putReq.Header.Set("Content-Type", "application/gzip")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d", putResp.StatusCode)
	}

	// Download via PresignGet
	getURL, err := provider.PresignGet(ctx, key, time.Minute)
	if err != nil {
		t.Fatalf("PresignGet failed: %v", err)
	}
	getResp, err := http.Get(getURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	got, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || !bytes.Equal(got, payload) {
		t.Fatalf("unexpected GET response: status=%d body=%q", getResp.StatusCode, string(got))
	}

	// Reject PUT on GET-only URL
	badPut, _ := http.NewRequestWithContext(ctx, http.MethodPut, getURL, bytes.NewReader([]byte("overwrite")))
	badPutResp, err := http.DefaultClient.Do(badPut)
	if err != nil {
		t.Fatalf("PUT on GET url failed: %v", err)
	}
	_ = badPutResp.Body.Close()
	if badPutResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for PUT on GET url, got %d", badPutResp.StatusCode)
	}

	// Expired presigned URL should be rejected
	shortURL, err := provider.PresignGet(ctx, key, 15*time.Millisecond)
	if err != nil {
		t.Fatalf("short PresignGet failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	expResp, err := http.Get(shortURL)
	if err != nil {
		t.Fatalf("expired GET failed: %v", err)
	}
	_ = expResp.Body.Close()
	if expResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for expired presigned URL, got %d", expResp.StatusCode)
	}

	// Validation checks
	if _, err := provider.PresignPut(ctx, "", time.Minute, ""); err == nil {
		t.Error("expected error for empty key")
	}
	if _, err := provider.PresignGet(ctx, key, 0); err == nil {
		t.Error("expected error for zero TTL")
	}
}
