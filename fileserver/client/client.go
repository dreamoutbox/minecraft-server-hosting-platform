package client

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

// Client defines management operations exposed by a node's fileserver daemon.
type Client interface {
	EnsureUser(ctx context.Context, userID uint, email string, uid *int, gid *int) (*EnsureUserResponse, error)
	Allow(ctx context.Context, userID uint, serverID uint) error
	Disallow(ctx context.Context, userID uint, serverID uint) error
	RevokeServer(ctx context.Context, serverID uint) error
	CreateLoginToken(ctx context.Context, userID uint) (*LoginTokenResponse, error)
}

// HTTPClient calls a node's fileserver management API using Bearer API key authentication.
type HTTPClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New constructs an HTTPClient targeting a node fileserver instance.
//
// Arguments:
//   - baseURL: Root HTTP URL of the node fileserver (e.g. http://localhost:9100).
//   - apiKey: Shared node API key for management endpoints.
//
// Example usage:
//
//	c := client.New("http://node-1:9100", host.APIKey)
func New(baseURL string, apiKey string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// doJSON sends an authenticated JSON HTTP request and optionally decodes the JSON response.
func (c *HTTPClient) doJSON(ctx context.Context, method, path string, reqBody interface{}, respOut interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("failed marshaling fileserver request: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	targetURL := fmt.Sprintf("%s%s", c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed creating fileserver request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fileserver request to %s failed: %w", targetURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fileserver %s %s failed with status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if respOut != nil {
		if err := json.NewDecoder(resp.Body).Decode(respOut); err != nil {
			return fmt.Errorf("failed decoding fileserver response: %w", err)
		}
	}
	return nil
}

// EnsureUser idempotently ensures a user record exists on the fileserver.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Platform user primary key.
//   - email: User email address.
//   - uid: Optional Unix user ID (>= 0) applied to files uploaded/created by this user.
//   - gid: Optional Unix group ID (>= 0) applied to files uploaded/created by this user.
//
// Example usage:
//
//	uid, gid := 1000, 1000
//	res, err := c.EnsureUser(ctx, user.ID, user.Email, &uid, &gid)
func (c *HTTPClient) EnsureUser(ctx context.Context, userID uint, email string, uid *int, gid *int) (*EnsureUserResponse, error) {
	var out EnsureUserResponse
	err := c.doJSON(ctx, http.MethodPut, "/api/v1/users", EnsureUserRequest{
		ID:    userID,
		Email: email,
		UID:   uid,
		GID:   gid,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Allow grants a user permission to manage a server's mount directory.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//   - serverID: Target server ID.
//
// Example usage:
//
//	err := c.Allow(ctx, user.ID, server.ID)
func (c *HTTPClient) Allow(ctx context.Context, userID uint, serverID uint) error {
	return c.doJSON(ctx, http.MethodPut, "/api/v1/permissions", PermissionRequest{
		UserID:   userID,
		ServerID: serverID,
	}, nil)
}

// Disallow revokes a user's permission to manage a server's mount directory.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//   - serverID: Target server ID.
//
// Example usage:
//
//	err := c.Disallow(ctx, user.ID, server.ID)
func (c *HTTPClient) Disallow(ctx context.Context, userID uint, serverID uint) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/permissions", PermissionRequest{
		UserID:   userID,
		ServerID: serverID,
	}, nil)
}

// RevokeServer removes all directory permissions associated with a deleted server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - serverID: Target server ID.
//
// Example usage:
//
//	err := c.RevokeServer(ctx, server.ID)
func (c *HTTPClient) RevokeServer(ctx context.Context, serverID uint) error {
	return c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/servers/%d/permissions", serverID), nil, nil)
}

// CreateLoginToken requests a short-lived single-use login token for the specified user.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - userID: Target user ID.
//
// Example usage:
//
//	tok, err := c.CreateLoginToken(ctx, user.ID)
func (c *HTTPClient) CreateLoginToken(ctx context.Context, userID uint) (*LoginTokenResponse, error) {
	var out LoginTokenResponse
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/login-token", userID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
