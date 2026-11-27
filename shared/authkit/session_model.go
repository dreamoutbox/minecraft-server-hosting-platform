package authkit

import (
	"time"
)

// Session represents a server-side authenticated browser session stored in the database.
// Only the SHA-256 hash of the opaque cookie token is persisted so a DB leak cannot be replayed.
type Session struct {
	TokenHash  string    `gorm:"primaryKey;type:varchar(64)" json:"-"`
	UserID     uint      `gorm:"index;not null" json:"user_id"`
	ExpiresAt  time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// TableName pins Session to the "sessions" table.
func (Session) TableName() string {
	return "sessions"
}
