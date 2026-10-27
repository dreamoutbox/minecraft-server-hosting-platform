package storage

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
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
