package store

import (
	"time"

	"github.com/dreamoutbox/authkit"
)

// User extends authkit.User with optional Unix UID/GID ownership applied to files
// uploaded or created by this user through the fileserver.
type User struct {
	authkit.User
	UID *int `gorm:"column:uid" json:"uid,omitempty"`
	GID *int `gorm:"column:gid" json:"gid,omitempty"`
}

// TableName pins store.User to the shared "users" table name in SQLite.
func (User) TableName() string {
	return "users"
}

// DirPermission grants a specific user permission to manage files in a server's mount directory.
type DirPermission struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"not null;uniqueIndex:idx_user_server" json:"user_id"`
	ServerID  uint      `gorm:"not null;uniqueIndex:idx_user_server;index" json:"server_id"`
	DirPath   string    `gorm:"type:varchar(255);not null" json:"dir_path"`
	CreatedAt time.Time `json:"created_at"`
}

// UsedLoginToken records consumed single-use login token JTIs to prevent replay attacks.
type UsedLoginToken struct {
	JTI       string    `gorm:"primaryKey;type:varchar(64)"`
	ExpiresAt time.Time `gorm:"index;not null"`
}

// TransferDirection identifies whether transferred bytes count against upload or download quota.
type TransferDirection string

const (
	// TransferDirectionUpload counts bytes uploaded by a user.
	TransferDirectionUpload TransferDirection = "upload"
	// TransferDirectionDownload counts bytes downloaded by a user.
	TransferDirectionDownload TransferDirection = "download"
)

// TransferUsage tracks per-user daily upload and download byte totals keyed by UTC day (YYYY-MM-DD).
type TransferUsage struct {
	ID            uint   `gorm:"primaryKey"`
	UserID        uint   `gorm:"not null;uniqueIndex:idx_usage_user_day"`
	Day           string `gorm:"type:varchar(10);not null;uniqueIndex:idx_usage_user_day;index"`
	UploadBytes   int64  `gorm:"not null;default:0"`
	DownloadBytes int64  `gorm:"not null;default:0"`
}
