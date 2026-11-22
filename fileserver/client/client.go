package client

import (
	"context"
)

// Client defines management operations exposed by a node's fileserver daemon.
type Client interface {
	RegisterUser(ctx context.Context, userID uint, email string, password string, uid *int, gid *int) (*RegisterUserResponse, error)
	Allow(ctx context.Context, userID uint, serverID uint) error
	Disallow(ctx context.Context, userID uint, serverID uint) error
	RevokeServer(ctx context.Context, serverID uint) error
	CreateLoginToken(ctx context.Context, userID uint) (*LoginTokenResponse, error)
}
