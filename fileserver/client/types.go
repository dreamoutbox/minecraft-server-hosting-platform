package client

import "time"

// RegisterUserRequest is the payload sent to idempotently create or update a fileserver user.
type RegisterUserRequest struct {
	ID       uint   `json:"id" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
	UID      *int   `json:"uid,omitempty"`
	GID      *int   `json:"gid,omitempty"`
}

// RegisterUserResponse reports the registered user identity and whether a new record was created.
type RegisterUserResponse struct {
	ID      uint   `json:"id"`
	Email   string `json:"email"`
	UID     *int   `json:"uid,omitempty"`
	GID     *int   `json:"gid,omitempty"`
	Created bool   `json:"created"`
}

// PermissionRequest identifies a user and server directory grant or revocation target.
type PermissionRequest struct {
	UserID   uint `json:"user_id" binding:"required"`
	ServerID uint `json:"server_id" binding:"required"`
}

// PermissionResponse returns the resulting directory permission state.
type PermissionResponse struct {
	UserID   uint   `json:"user_id"`
	ServerID uint   `json:"server_id"`
	DirPath  string `json:"dir_path"`
	Allowed  bool   `json:"allowed"`
}

// LoginTokenResponse returns a short-lived, single-use login JWT for browser access.
type LoginTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}
