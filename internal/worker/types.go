package worker

// ContainerStatus represents the container runtime state reported by the worker daemon.
type ContainerStatus string

const (
	ContainerStatusRunning ContainerStatus = "running"
	ContainerStatusStopped ContainerStatus = "stopped"
	ContainerStatusUnknown ContainerStatus = "unknown"
)

// CreateContainerRequest specifies container parameters sent to the worker daemon.
type CreateContainerRequest struct {
	ServerID    uint   `json:"server_id"`
	Port        int    `json:"port"`
	CPULimit    int    `json:"cpu_limit"`
	MemLimitMB  int    `json:"mem_limit_mb"`
	DiskLimitGB int    `json:"disk_limit_gb"`
	MountPath   string `json:"mount_path"`
	Image       string `json:"image"`
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
	Hostname            string `json:"hostname"`
	ActiveContainers    int    `json:"active_containers"`
	MaxServers          int    `json:"max_servers"`
	FreeDiskBytes       uint64 `json:"free_disk_bytes"`
	TotalDiskBytes      uint64 `json:"total_disk_bytes"`
}
