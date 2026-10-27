package domain

import (
	"time"
)

// HostStatus represents host availability states in the cluster.
type HostStatus string

const (
	HostStatusActive   HostStatus = "active"
	HostStatusDraining HostStatus = "draining"
	HostStatusDisabled HostStatus = "disabled"
)

// ServerStatus represents the lifecycle phase of a provisioned game server.
type ServerStatus string

const (
	ServerStatusPending      ServerStatus = "pending"
	ServerStatusProvisioning ServerStatus = "provisioning"
	ServerStatusRunning      ServerStatus = "running"
	ServerStatusStopped      ServerStatus = "stopped"
	ServerStatusMigrating    ServerStatus = "migrating"
	ServerStatusSuspended    ServerStatus = "suspended"
	ServerStatusError        ServerStatus = "error"
)

// SubscriptionStatus represents customer subscription billing states.
type SubscriptionStatus string

const (
	SubscriptionStatusActive    SubscriptionStatus = "active"
	SubscriptionStatusPastDue   SubscriptionStatus = "past_due"
	SubscriptionStatusCancelled SubscriptionStatus = "cancelled"
	SubscriptionStatusSuspended SubscriptionStatus = "suspended"
)

// PaymentStatus represents status outcomes of transaction charges.
type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "pending"
	PaymentStatusSucceeded PaymentStatus = "succeeded"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusRefunded  PaymentStatus = "refunded"
)

// BackupStatus represents world backup creation and upload states.
type BackupStatus string

const (
	BackupStatusPending   BackupStatus = "pending"
	BackupStatusCompleted BackupStatus = "completed"
	BackupStatusFailed    BackupStatus = "failed"
)

// UserRole represents platform permission levels.
type UserRole string

const (
	UserRoleUser  UserRole = "user"
	UserRoleAdmin UserRole = "admin"
)

// User represents an authenticated account on the hosting platform.
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"not null" json:"-"`
	Role         UserRole  `gorm:"type:varchar(32);not null;default:'user'" json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Plan defines hardware resource limits and monthly cost for a Minecraft server tier.
// Note: Intentionally decoupled from Stripe plan or price identifiers.
type Plan struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Name       string    `gorm:"uniqueIndex;not null" json:"name"`
	CPULimit   int       `gorm:"not null" json:"cpu_limit"`   // number of CPU cores allocated
	MemLimitMB int       `gorm:"not null" json:"mem_limit_mb"` // memory allocation in megabytes
	DiskLimitGB int      `gorm:"not null" json:"disk_limit_gb"` // disk quota in gigabytes
	PriceCents int64     `gorm:"not null" json:"price_cents"` // price in cents per billing cycle
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Host represents a dedicated server node executing the worker daemon.
type Host struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Hostname      string     `gorm:"not null" json:"hostname"`
	WorkerBaseURL string     `gorm:"not null" json:"worker_base_url"`
	APIKey        string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	APIKeyHash    string     `gorm:"not null" json:"-"`
	MaxServers    int        `gorm:"not null" json:"max_servers"`
	Status        HostStatus `gorm:"type:varchar(32);not null;default:'active'" json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Server represents a provisioned Minecraft game server container.
// The port field is globally unique cluster-wide to preserve routing across migration.
type Server struct {
	ID          uint         `gorm:"primaryKey" json:"id"`
	UserID      uint         `gorm:"index;not null" json:"user_id"`
	PlanID      uint         `gorm:"not null" json:"plan_id"`
	HostID      uint         `gorm:"index;not null" json:"host_id"`
	Name        string       `gorm:"not null" json:"name"`
	Status      ServerStatus `gorm:"type:varchar(32);not null;default:'pending'" json:"status"`
	Port        int          `gorm:"uniqueIndex;not null" json:"port"`
	ContainerID string       `gorm:"type:varchar(128)" json:"container_id"`
	MountPath   string       `gorm:"type:varchar(255)" json:"mount_path"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`

	Plan Plan `gorm:"foreignKey:PlanID" json:"plan,omitempty"`
	Host Host `gorm:"foreignKey:HostID" json:"host,omitempty"`
}

// Subscription tracks recurring billing state managed by platform cron.
type Subscription struct {
	ID              uint               `gorm:"primaryKey" json:"id"`
	UserID          uint               `gorm:"index;not null" json:"user_id"`
	PlanID          uint               `gorm:"not null" json:"plan_id"`
	ServerID        *uint              `gorm:"index" json:"server_id,omitempty"`
	Status          SubscriptionStatus `gorm:"type:varchar(32);not null;default:'active'" json:"status"`
	NextBillingDate time.Time          `gorm:"not null" json:"next_billing_date"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`

	Plan Plan `gorm:"foreignKey:PlanID" json:"plan,omitempty"`
}

// PaymentRecord maintains historical ledger of subscription charges.
type PaymentRecord struct {
	ID             uint          `gorm:"primaryKey" json:"id"`
	UserID         uint          `gorm:"index;not null" json:"user_id"`
	SubscriptionID *uint         `gorm:"index" json:"subscription_id,omitempty"`
	AmountCents    int64         `gorm:"not null" json:"amount_cents"`
	Status         PaymentStatus `gorm:"type:varchar(32);not null;default:'pending'" json:"status"`
	ProviderTxID   string        `gorm:"type:varchar(128)" json:"provider_tx_id"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// BackupRecord tracks a discrete world archive stored in object storage.
type BackupRecord struct {
	ID         uint         `gorm:"primaryKey" json:"id"`
	ServerID   uint         `gorm:"index;not null" json:"server_id"`
	StorageKey string       `gorm:"not null" json:"storage_key"`
	SizeBytes  int64        `gorm:"not null" json:"size_bytes"`
	Status     BackupStatus `gorm:"type:varchar(32);not null;default:'pending'" json:"status"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}
