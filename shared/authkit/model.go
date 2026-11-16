package authkit

import (
	"time"
)

// UserRole represents platform permission levels.
type UserRole string

const (
	UserRoleUser  UserRole = "user"
	UserRoleAdmin UserRole = "admin"
)

// User represents an authenticated account stored in a GORM-managed database.
type User struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Email           string    `gorm:"uniqueIndex;not null" json:"email"`
	PasswordHash    string    `gorm:"not null" json:"-"`
	Role            UserRole  `gorm:"type:varchar(32);not null;default:'user'" json:"role"`
	CustomerID      string    `gorm:"type:varchar(128)" json:"customer_id,omitempty"`
	PaymentMethodID string    `gorm:"type:varchar(128)" json:"payment_method_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
