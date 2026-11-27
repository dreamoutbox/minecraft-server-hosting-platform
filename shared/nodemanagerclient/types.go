package nodemanagerclient

// APIKeyHeader is the HTTP header used to supply the worker daemon API key.
const APIKeyHeader = "X-Worker-API-Key"

// ContainerStatus represents the container runtime state reported by the worker daemon.
type ContainerStatus string

const (
	ContainerStatusRunning ContainerStatus = "running"
	ContainerStatusStopped ContainerStatus = "stopped"
	ContainerStatusUnknown ContainerStatus = "unknown"
)

// CreateContainerRequest specifies container parameters sent to the worker daemon.
type CreateContainerRequest struct {
	ServerID      uint   `json:"server_id"`
	Port          int    `json:"port"`
	CPULimit      int    `json:"cpu_limit"`
	MemLimitMB    int    `json:"mem_limit_mb"`
	DiskLimitGB   int    `json:"disk_limit_gb"`
	MountPath     string `json:"mount_path"`
	Image         string `json:"image"`
	ServerType    string `json:"server_type,omitempty"`
	ServerVersion string `json:"server_version,omitempty"`
}

// CreateContainerResponse provides provisioned container information.
type CreateContainerResponse struct {
	ContainerID string `json:"container_id"`
	Port        int    `json:"port"`
	MountPath   string `json:"mount_path"`
}

// ContainerStatusResponse reports live state of a container on the host.
type ContainerStatusResponse struct {
	ContainerID string          `json:"container_id"`
	Status      ContainerStatus `json:"status"`
}

// ContainerLogsResponse carries recent stdout/stderr console output from a container.
type ContainerLogsResponse struct {
	ContainerID string `json:"container_id"`
	Logs        string `json:"logs"`
}

// ExecCommandRequest specifies a Minecraft console command to execute inside a running container.
type ExecCommandRequest struct {
	Command string `json:"command" binding:"required"`
}

// ExecCommandResponse returns the output produced by executing a console command.
type ExecCommandResponse struct {
	ContainerID string `json:"container_id"`
	Command     string `json:"command"`
	Output      string `json:"output"`
}

// CreateVolumeRequest specifies size bounds for loop-mounted sparse storage.
type CreateVolumeRequest struct {
	ServerID    uint `json:"server_id"`
	DiskLimitGB int  `json:"disk_limit_gb"`
}

// CreateVolumeResponse reports mount coordinates of provisioned storage.
type CreateVolumeResponse struct {
	MountPath string `json:"mount_path"`
	ImagePath string `json:"image_path"`
}

// HostStatusResponse communicates local capacity metrics from the host node.
type HostStatusResponse struct {
	Hostname         string `json:"hostname"`
	ActiveContainers int    `json:"active_containers"`
	MaxServers       int    `json:"max_servers"`
	FreeDiskBytes    uint64 `json:"free_disk_bytes"`
	TotalDiskBytes   uint64 `json:"total_disk_bytes"`
}

// UploadVolumeBackupRequest instructs the worker to tar.gz a server mount and PUT it to a presigned URL.
type UploadVolumeBackupRequest struct {
	PresignedURL string `json:"presigned_url" binding:"required"`
}

// UploadVolumeBackupResponse reports the uploaded compressed archive size in bytes.
type UploadVolumeBackupResponse struct {
	SizeBytes int64 `json:"size_bytes"`
}

// RestoreVolumeBackupRequest instructs the worker to GET an archive from a presigned URL and unpack it into the mount.
type RestoreVolumeBackupRequest struct {
	PresignedURL string `json:"presigned_url" binding:"required"`
}

// UploadImageBackupRequest instructs the worker to tar.gz and split a server's ext4 .img file and upload parts via presigned PUT URLs.
type UploadImageBackupRequest struct {
	PresignedURLs []string `json:"presigned_urls" binding:"required,min=1"`
	MaxPartBytes  int64    `json:"max_part_bytes,omitempty"`
	ContainerID   string   `json:"container_id,omitempty"`
}

// UploadImageBackupResponse reports how many parts and bytes were uploaded for an ext4 image backup.
type UploadImageBackupResponse struct {
	PartsUploaded int     `json:"parts_uploaded"`
	PartSizes     []int64 `json:"part_sizes"`
	TotalBytes    int64   `json:"total_bytes"`
}

// RestoreImageBackupRequest instructs the worker to download, merge, decompress, and atomically restore a server's ext4 .img file.
type RestoreImageBackupRequest struct {
	PresignedURLs []string `json:"presigned_urls" binding:"required,min=1"`
}

// RestoreImageBackupResponse reports the restored ext4 .img file size in bytes.
type RestoreImageBackupResponse struct {
	SizeBytes int64 `json:"size_bytes"`
}
