package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client defines the interface for communicating with a remote node's worker daemon.
type Client interface {
	GetStatus(ctx context.Context) (*HostStatusResponse, error)
	CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*CreateVolumeResponse, error)
	DeleteVolume(ctx context.Context, volumeID string) error
	ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error)
	ImportVolume(ctx context.Context, volumeID string, r io.Reader) error
	CreateContainer(ctx context.Context, req CreateContainerRequest) (*CreateContainerResponse, error)
	StopContainer(ctx context.Context, containerID string) error
	DeleteContainer(ctx context.Context, containerID string) error
	GetContainerStatus(ctx context.Context, containerID string) (*ContainerStatusResponse, error)
}

// HTTPClient implements Client via standard HTTP requests injecting the worker API key.
type HTTPClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient constructs an HTTP worker client targeting a specific host node.
//
// Arguments:
//   - baseURL: Root URL of the worker HTTP service (e.g. http://10.0.0.1:9090).
//   - apiKey: Worker authentication key for the targeted host.
//
// Example usage:
//   client := worker.NewClient("http://node-1:9090", "host-secret")
func NewClient(baseURL string, apiKey string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// doRequest handles request execution, header injection, and HTTP status verification.
func (c *HTTPClient) doRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	url := fmt.Sprintf("%s%s", c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed creating worker request: %w", err)
	}

	req.Header.Set(APIKeyHeader, c.apiKey)
	if reqID, ok := ctx.Value("request_id").(string); ok && reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request to %s failed: %w", url, err)
	}

	return resp, nil
}

// GetStatus retrieves health and capacity metrics from the node.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//   status, err := client.GetStatus(ctx)
func (c *HTTPClient) GetStatus(ctx context.Context) (*HostStatusResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/status", nil, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("worker status failed with code %d: %s", resp.StatusCode, string(b))
	}

	var status HostStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed decoding status response: %w", err)
	}
	return &status, nil
}

// CreateVolume requests sparse storage allocation on the node.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Associated game server ID.
//   - diskLimitGB: Size ceiling in gigabytes.
//
// Example usage:
//   vol, err := client.CreateVolume(ctx, 42, 20)
func (c *HTTPClient) CreateVolume(ctx context.Context, serverID uint, diskLimitGB int) (*CreateVolumeResponse, error) {
	payload, err := json.Marshal(CreateVolumeRequest{
		ServerID:    serverID,
		DiskLimitGB: diskLimitGB,
	})
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/volumes", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create volume failed with code %d: %s", resp.StatusCode, string(b))
	}

	var res CreateVolumeResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed decoding volume response: %w", err)
	}
	return &res, nil
}

// DeleteVolume cleans up storage mount and sparse files on the host.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Identifier of volume to destroy.
//
// Example usage:
//   err := client.DeleteVolume(ctx, "42")
func (c *HTTPClient) DeleteVolume(ctx context.Context, volumeID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/volumes/%s", volumeID), nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete volume failed with code %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ExportVolume streams a tar archive of volume files from the node.
// Caller is responsible for closing the returned ReadCloser.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Identifier of volume to archive.
//
// Example usage:
//   stream, err := client.ExportVolume(ctx, "42")
func (c *HTTPClient) ExportVolume(ctx context.Context, volumeID string) (io.ReadCloser, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/volumes/%s/export", volumeID), nil, "")
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("export volume failed with code %d: %s", resp.StatusCode, string(b))
	}

	return resp.Body, nil
}

// ImportVolume streams a tar archive to overwrite volume contents on the node.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - volumeID: Target volume identifier.
//   - r: Reader containing the tar stream archive.
//
// Example usage:
//   err := client.ImportVolume(ctx, "42", tarStream)
func (c *HTTPClient) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/volumes/%s/import", volumeID), r, "application/x-tar")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("import volume failed with code %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// CreateContainer launches a container on the host with configured resources and port forwarding.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Container specifications.
//
// Example usage:
//   ctr, err := client.CreateContainer(ctx, req)
func (c *HTTPClient) CreateContainer(ctx context.Context, req CreateContainerRequest) (*CreateContainerResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/containers", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create container failed with code %d: %s", resp.StatusCode, string(b))
	}

	var res CreateContainerResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed decoding container response: %w", err)
	}
	return &res, nil
}

// StopContainer requests graceful container stoppage on the host.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID.
//
// Example usage:
//   err := client.StopContainer(ctx, "ctr-123")
func (c *HTTPClient) StopContainer(ctx context.Context, containerID string) error {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/stop", containerID), nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop container failed with code %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// DeleteContainer removes a container from the host.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID.
//
// Example usage:
//   err := client.DeleteContainer(ctx, "ctr-123")
func (c *HTTPClient) DeleteContainer(ctx context.Context, containerID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/containers/%s", containerID), nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete container failed with code %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// GetContainerStatus queries the running state of a container on the host.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID.
//
// Example usage:
//   status, err := client.GetContainerStatus(ctx, "ctr-123")
func (c *HTTPClient) GetContainerStatus(ctx context.Context, containerID string) (*ContainerStatusResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/containers/%s/status", containerID), nil, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get container status failed with code %d: %s", resp.StatusCode, string(b))
	}

	var res ContainerStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed decoding container status response: %w", err)
	}
	return &res, nil
}
