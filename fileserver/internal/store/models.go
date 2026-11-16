package store

import "time"

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
