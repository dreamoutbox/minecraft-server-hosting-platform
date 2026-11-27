package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// StorageType indicates the backend storage technology variant.
type StorageType string

const (
	StorageTypeS3     StorageType = "s3"
	StorageTypeMemory StorageType = "memory"
)

// StorageProvider defines operations for reading, writing, listing, and presigning object blobs.
type StorageProvider interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string) (string, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// S3Config encapsulates configuration settings required to connect to an S3-compatible service.
type S3Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
}

// S3StorageProvider implements StorageProvider against SeaweedFS (dev) or AWS S3 (prod).
type S3StorageProvider struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
}

// NewS3StorageProvider initializes an S3-compatible object storage provider.
// In dev environments targeting SeaweedFS or local emulators, it automatically ensures
// the target bucket exists before returning.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - cfg: S3 connectivity credentials and endpoints.
//
// Example usage:
//
//	provider, err := storage.NewS3StorageProvider(ctx, storage.S3Config{
//	    Endpoint:     "http://localhost:8333",
//	    Region:       "us-east-1",
//	    Bucket:       "minecraft-backups",
//	    UsePathStyle: true,
//	})
func NewS3StorageProvider(ctx context.Context, cfg S3Config) (*S3StorageProvider, error) {
	if cfg.Bucket == "" {
		cfg.Bucket = "minecraft-backups"
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	optFns := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		optFns = append(optFns, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	} else if cfg.Endpoint != "" {
		// Provide static dummy credentials for local S3 emulators (SeaweedFS) to avoid IMDS lookup delay
		optFns = append(optFns, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("dummy", "dummy", ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("failed loading aws configuration: %w", err)
	}

	s3OptFns := func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	}

	client := s3.NewFromConfig(awsCfg, s3OptFns)

	// Ensure bucket exists for dev emulators such as SeaweedFS
	if cfg.Endpoint != "" {
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
			Bucket: aws.String(cfg.Bucket),
		})
		if err != nil {
			var notFound *s3types.NotFound
			if errors.As(err, &notFound) || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NotFound") {
				_, createErr := client.CreateBucket(ctx, &s3.CreateBucketInput{
					Bucket: aws.String(cfg.Bucket),
				})
				if createErr != nil && !strings.Contains(createErr.Error(), "BucketAlreadyOwnedByYou") {
					return nil, fmt.Errorf("failed creating bucket %s: %w", cfg.Bucket, createErr)
				}
			}
		}
	}

	return &S3StorageProvider{
		client:        client,
		presignClient: s3.NewPresignClient(client),
		bucket:        cfg.Bucket,
	}, nil
}

// Put uploads an object payload under the specified key.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Unique path identifier of the object within the bucket.
//   - data: Raw binary byte slice of object contents.
//
// Example usage:
//
//	err := provider.Put(ctx, "backups/server-1/world.tar", data)
func (s *S3StorageProvider) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("failed putting object %s: %w", key, err)
	}
	return nil
}

// Get downloads an object from the bucket.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Path key of the object to retrieve.
//
// Example usage:
//
//	data, err := provider.Get(ctx, "backups/server-1/world.tar")
func (s *S3StorageProvider) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed getting object %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading object body for %s: %w", key, err)
	}
	return data, nil
}

// Delete removes an object from the bucket.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Path key of the object to remove.
//
// Example usage:
//
//	err := provider.Delete(ctx, "backups/server-1/world.tar")
func (s *S3StorageProvider) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed deleting object %s: %w", key, err)
	}
	return nil
}

// List enumerates object keys matching the given prefix.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - prefix: Directory or key prefix filter.
//
// Example usage:
//
//	keys, err := provider.List(ctx, "backups/server-1/")
func (s *S3StorageProvider) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed listing objects for prefix %s: %w", prefix, err)
		}
		for _, item := range page.Contents {
			if item.Key != nil {
				keys = append(keys, *item.Key)
			}
		}
	}

	return keys, nil
}

// PresignPut mints a time-limited, single-object HTTP PUT URL scoped to the configured bucket and key.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Target object key within the bucket.
//   - ttl: Validity duration for starting the upload.
//   - contentType: Optional MIME Content-Type bound to the upload (empty leaves it unbound).
//
// Example usage:
//
//	url, err := provider.PresignPut(ctx, "backups/server-1/world.tar.gz", time.Hour, "application/gzip")
func (s *S3StorageProvider) PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string) (string, error) {
	if err := validatePresignArgs(key, ttl); err != nil {
		return "", err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	req, err := s.presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("failed presigning PUT for %s: %w", key, err)
	}
	return req.URL, nil
}

// PresignGet mints a time-limited, single-object HTTP GET URL scoped to the configured bucket and key.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Object key within the bucket to allow downloading.
//   - ttl: Validity duration for starting the download.
//
// Example usage:
//
//	url, err := provider.PresignGet(ctx, "backups/server-1/world.tar.gz", time.Hour)
func (s *S3StorageProvider) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validatePresignArgs(key, ttl); err != nil {
		return "", err
	}
	req, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("failed presigning GET for %s: %w", key, err)
	}
	return req.URL, nil
}

type memoryPresignedGrant struct {
	method      string
	key         string
	contentType string
	expiresAt   time.Time
}

// MemoryStorageProvider offers an in-memory implementation for testing without network dependencies.
type MemoryStorageProvider struct {
	mu         sync.RWMutex
	data       map[string][]byte
	grants     map[string]memoryPresignedGrant
	serverAddr string
	httpSrv    *http.Server
	listener   net.Listener
}

// NewMemoryStorageProvider instantiates an in-memory storage provider.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	memProvider := storage.NewMemoryStorageProvider()
func NewMemoryStorageProvider() *MemoryStorageProvider {
	return &MemoryStorageProvider{
		data:   make(map[string][]byte),
		grants: make(map[string]memoryPresignedGrant),
	}
}

// Close shuts down the optional local presign HTTP listener started for in-memory presigned URLs.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	defer memProvider.Close()
func (m *MemoryStorageProvider) Close() error {
	m.mu.Lock()
	srv := m.httpSrv
	m.httpSrv = nil
	m.listener = nil
	m.serverAddr = ""
	m.mu.Unlock()
	if srv != nil {
		return srv.Close()
	}
	return nil
}

func (m *MemoryStorageProvider) Put(ctx context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := make([]byte, len(data))
	copy(buf, data)
	m.data[key] = buf
	return nil
}

func (m *MemoryStorageProvider) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, exists := m.data[key]
	if !exists {
		return nil, fmt.Errorf("object not found: %s", key)
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	return buf, nil
}

func (m *MemoryStorageProvider) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MemoryStorageProvider) List(ctx context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// PresignPut mints a local loopback PUT URL enforcing token scope, method, content type, and TTL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Target object key.
//   - ttl: Expiration window for the presigned token.
//   - contentType: Optional expected Content-Type header.
//
// Example usage:
//
//	url, err := memProvider.PresignPut(ctx, "backups/server-1/world.tar.gz", time.Minute, "application/gzip")
func (m *MemoryStorageProvider) PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string) (string, error) {
	return m.mintGrant(http.MethodPut, key, ttl, contentType)
}

// PresignGet mints a local loopback GET URL enforcing token scope, method, and TTL.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - key: Target object key.
//   - ttl: Expiration window for the presigned token.
//
// Example usage:
//
//	url, err := memProvider.PresignGet(ctx, "backups/server-1/world.tar.gz", time.Minute)
func (m *MemoryStorageProvider) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return m.mintGrant(http.MethodGet, key, ttl, "")
}

func (m *MemoryStorageProvider) mintGrant(method, key string, ttl time.Duration, contentType string) (string, error) {
	if err := validatePresignArgs(key, ttl); err != nil {
		return "", err
	}
	addr, err := m.ensurePresignServer()
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed generating presign token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	m.mu.Lock()
	m.grants[token] = memoryPresignedGrant{
		method:      method,
		key:         key,
		contentType: contentType,
		expiresAt:   time.Now().Add(ttl),
	}
	m.mu.Unlock()

	return fmt.Sprintf("http://%s/objects/%s", addr, token), nil
}

func (m *MemoryStorageProvider) ensurePresignServer() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.serverAddr != "" {
		return m.serverAddr, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("failed starting memory presign listener: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/objects/", m.handlePresignedHTTP)
	srv := &http.Server{Handler: mux}

	m.listener = ln
	m.httpSrv = srv
	m.serverAddr = ln.Addr().String()

	go func() {
		_ = srv.Serve(ln)
	}()

	return m.serverAddr, nil
}

func (m *MemoryStorageProvider) handlePresignedHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/objects/")
	if token == "" || strings.Contains(token, "/") {
		http.Error(w, "invalid presigned token", http.StatusForbidden)
		return
	}

	m.mu.RLock()
	grant, ok := m.grants[token]
	m.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown presigned token", http.StatusForbidden)
		return
	}
	if time.Now().After(grant.expiresAt) {
		http.Error(w, "presigned url expired", http.StatusForbidden)
		return
	}
	if r.Method != grant.method {
		http.Error(w, "method not allowed for presigned url", http.StatusForbidden)
		return
	}

	switch grant.method {
	case http.MethodPut:
		if grant.contentType != "" && r.Header.Get("Content-Type") != grant.contentType {
			http.Error(w, "content-type mismatch", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed reading body", http.StatusBadRequest)
			return
		}
		if err := m.Put(r.Context(), grant.key, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, err := m.Get(r.Context(), grant.key)
		if err != nil {
			http.Error(w, "object not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	default:
		http.Error(w, "unsupported method", http.StatusForbidden)
	}
}

func validatePresignArgs(key string, ttl time.Duration) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("presign object key must not be empty")
	}
	if ttl <= 0 {
		return errors.New("presign TTL must be positive")
	}
	return nil
}

// NewStorageProviderFromEnv inspects environment variables and constructs the appropriate provider.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	sp, err := storage.NewStorageProviderFromEnv(ctx)
func NewStorageProviderFromEnv(ctx context.Context) (StorageProvider, error) {
	storageType := os.Getenv("STORAGE_TYPE")
	if storageType == string(StorageTypeMemory) {
		return NewMemoryStorageProvider(), nil
	}

	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8333" // default dev SeaweedFS
	}

	region := os.Getenv("S3_REGION")
	bucket := os.Getenv("S3_BUCKET")
	accessKey := os.Getenv("S3_ACCESS_KEY")
	secretKey := os.Getenv("S3_SECRET_KEY")
	usePathStyle := os.Getenv("S3_USE_PATH_STYLE") != "false"

	return NewS3StorageProvider(ctx, S3Config{
		Endpoint:     endpoint,
		Region:       region,
		Bucket:       bucket,
		AccessKey:    accessKey,
		SecretKey:    secretKey,
		UsePathStyle: usePathStyle,
	})
}
