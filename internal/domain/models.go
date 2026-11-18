package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/dreamoutbox/authkit"
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
type UserRole = authkit.UserRole

const (
	UserRoleUser  = authkit.UserRoleUser
	UserRoleStaff = authkit.UserRoleStaff
	UserRoleAdmin = authkit.UserRoleAdmin
)

// ServerType represents supported Minecraft server jar distributions.
type ServerType string

const (
	ServerTypeVanilla ServerType = "VANILLA"
	ServerTypePaper   ServerType = "PAPER"
	ServerTypeFabric  ServerType = "FABRIC"
	ServerTypeForge   ServerType = "FORGE"
)

// DefaultServerVersion selects the latest upstream release when no specific version is pinned.
const DefaultServerVersion = "LATEST"

var validServerTypes = map[ServerType]bool{
	ServerTypeVanilla: true,
	ServerTypePaper:   true,
	ServerTypeFabric:  true,
	ServerTypeForge:   true,
}

// AllServerTypes returns the supported Minecraft server jar options in display order.
//
// Arguments:
//   - None.
//
// Example usage:
//
//	types := domain.AllServerTypes()
func AllServerTypes() []ServerType {
	return []ServerType{
		ServerTypeVanilla,
		ServerTypePaper,
		ServerTypeFabric,
		ServerTypeForge,
	}
}

// ParseServerType normalizes and validates a Minecraft server jar distribution value.
//
// Arguments:
//   - raw: Unvalidated distribution string (defaults to VANILLA when empty).
//
// Example usage:
//
//	st, err := domain.ParseServerType("paper")
func ParseServerType(raw string) (ServerType, error) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return ServerTypeVanilla, nil
	}
	candidate := ServerType(strings.ToUpper(cleaned))
	if !validServerTypes[candidate] {
		return "", errors.New("unsupported server type; allowed: VANILLA, PAPER, FABRIC, FORGE")
	}
	return candidate, nil
}

// ValidateServerVersion normalizes and validates a Minecraft version string.
//
// Arguments:
//   - raw: Unvalidated version string (defaults to LATEST when empty).
//
// Example usage:
//
//	ver, err := domain.ValidateServerVersion("1.21.4")
func ValidateServerVersion(raw string) (string, error) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return DefaultServerVersion, nil
	}
	if strings.EqualFold(cleaned, DefaultServerVersion) {
		return DefaultServerVersion, nil
	}
	if len(cleaned) > 32 {
		return "", errors.New("server version must be 32 characters or fewer")
	}
	for _, r := range cleaned {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' && r != '_' {
			return "", errors.New("server version may only contain letters, digits, '.', '-', or '_'")
		}
	}
	return cleaned, nil
}

// ServerOperationType enumerates server actions recorded in server_operation_logs.
type ServerOperationType string

const (
	ServerOpCreate  ServerOperationType = "create"
	ServerOpStop    ServerOperationType = "stop"
	ServerOpDelete  ServerOperationType = "delete"
	ServerOpBackup  ServerOperationType = "backup"
	ServerOpRestore ServerOperationType = "restore"
)

// ServerLogStatus represents the final outcome of an audited server operation.
type ServerLogStatus string

const (
	ServerLogStatusSuccess ServerLogStatus = "success"
	ServerLogStatusError   ServerLogStatus = "error"
)

// JobType identifies background queue tasks processed asynchronously by cmd/apiserver.
type JobType string

const (
	JobTypeCreateServer JobType = "create_server"
	JobTypeDeleteServer JobType = "delete_server"
	JobTypeBackup       JobType = "backup"
	JobTypeRestore      JobType = "restore"
)

// JobStatus tracks execution state for queued jobs in PostgreSQL.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

// User represents an authenticated account on the hosting platform.
type User = authkit.User

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
	ID                uint       `gorm:"primaryKey" json:"id"`
	Hostname          string     `gorm:"not null" json:"hostname"`
	WorkerBaseURL     string     `gorm:"not null" json:"worker_base_url"`
	FileServerBaseURL string     `gorm:"type:varchar(255);not null;default:''" json:"fileserver_base_url"`
	APIKey            string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	APIKeyHash        string     `gorm:"not null" json:"-"`
	MaxServers        int        `gorm:"not null" json:"max_servers"`
	Status            HostStatus `gorm:"type:varchar(32);not null;default:'active'" json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Server represents a provisioned Minecraft game server container.
// The port field is globally unique cluster-wide to preserve routing across migration.
type Server struct {
	ID          uint         `gorm:"primaryKey" json:"id"`
	UserID      uint         `gorm:"index;not null" json:"user_id"`
	PlanID      uint         `gorm:"not null" json:"plan_id"`
	HostID      uint         `gorm:"index;not null" json:"host_id"`
	Name          string       `gorm:"not null" json:"name"`
	Status        ServerStatus `gorm:"type:varchar(32);not null;default:'pending'" json:"status"`
	Port          int          `gorm:"uniqueIndex;not null" json:"port"`
	ContainerID   string       `gorm:"type:varchar(128)" json:"container_id"`
	MountPath     string       `gorm:"type:varchar(255)" json:"mount_path"`
	ServerType    ServerType   `gorm:"type:varchar(32);not null;default:'VANILLA'" json:"server_type"`
	ServerVersion string       `gorm:"type:varchar(32);not null;default:'LATEST'" json:"server_version"`
	CreatedAt     time.Time    `json:"created_at"`
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
	FailureCount    int                `gorm:"default:0;not null" json:"failure_count"`
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

// ServerOperationLog persists audited create/stop/delete/backup/restore outcomes in PostgreSQL.
type ServerOperationLog struct {
	ID        uint                `gorm:"primaryKey" json:"id"`
	ServerID  uint                `gorm:"index;not null" json:"server_id"`
	UserID    uint                `gorm:"index;not null" json:"user_id"`
	Operation ServerOperationType `gorm:"type:varchar(32);not null;index" json:"operation"`
	Status    ServerLogStatus     `gorm:"type:varchar(16);not null;index" json:"status"`
	Message   string              `gorm:"type:text;not null" json:"message"`
	CreatedAt time.Time           `gorm:"index" json:"created_at"`
}

// ServerJob represents a queued asynchronous server lifecycle or backup task with retry support.
type ServerJob struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Type        JobType    `gorm:"type:varchar(32);not null;index" json:"type"`
	Status      JobStatus  `gorm:"type:varchar(32);not null;default:'pending';index" json:"status"`
	UserID      uint       `gorm:"index;not null" json:"user_id"`
	ServerID    uint       `gorm:"index;not null" json:"server_id"`
	BackupID    *uint      `gorm:"index" json:"backup_id,omitempty"`
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	MaxAttempts int        `gorm:"not null;default:3" json:"max_attempts"`
	LastError   string     `gorm:"type:text" json:"last_error,omitempty"`
	NextRunAt   time.Time  `gorm:"index;not null" json:"next_run_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
