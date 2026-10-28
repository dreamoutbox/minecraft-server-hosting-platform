package worker

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// ContainerManager defines lifecycle operations on Docker containers for Minecraft servers.
type ContainerManager interface {
	CreateAndStart(ctx context.Context, req CreateContainerRequest) (*CreateContainerResponse, error)
	Stop(ctx context.Context, containerID string) error
	Delete(ctx context.Context, containerID string) error
	GetStatus(ctx context.Context, containerID string) (*ContainerStatusResponse, error)
	CountActive(ctx context.Context) (int, error)
}

// DockerContainerManager coordinates with the local Docker daemon socket via Docker SDK.
type DockerContainerManager struct {
	cli *client.Client
}

// NewDockerContainerManager establishes a connection to the local Docker daemon socket.
//
// Arguments:
//   - None (negotiates API version directly against local docker.sock).
//
// Example usage:
//
//	cm, err := worker.NewDockerContainerManager()
//	if err != nil {
//	    log.Fatalf("failed connecting to Docker: %v", err)
//	}
func NewDockerContainerManager() (*DockerContainerManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed creating docker client: %w", err)
	}
	return &DockerContainerManager{cli: cli}, nil
}

// CreateAndStart pulls the server image if needed, configures limits and port mapping, and starts the container.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - req: Configuration specifying server ID, limits, port, and volume mount path.
//
// Example usage:
//
//	resp, err := cm.CreateAndStart(ctx, req)
func (d *DockerContainerManager) CreateAndStart(ctx context.Context, req CreateContainerRequest) (*CreateContainerResponse, error) {
	img := req.Image
	if img == "" {
		img = "itzg/minecraft-server:latest"
	}

	// Pull image if not available locally
	_, _, err := d.cli.ImageInspectWithRaw(ctx, img)
	if err != nil {
		rc, pullErr := d.cli.ImagePull(ctx, img, image.PullOptions{})
		if pullErr != nil {
			return nil, fmt.Errorf("failed pulling docker image %s: %w", img, pullErr)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}

	containerPort := nat.Port("25565/tcp")
	hostPortStr := strconv.Itoa(req.Port)

	portMap := nat.PortMap{
		containerPort: []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: hostPortStr,
			},
		},
	}

	binds := []string{}
	if req.MountPath != "" {
		binds = append(binds, fmt.Sprintf("%s:/data:rw", req.MountPath))
	}

	containerConfig := &container.Config{
		Image: img,
		Env: []string{
			"EULA=TRUE",
			"ONLINE_MODE=FALSE",
		},
		ExposedPorts: nat.PortSet{
			containerPort: struct{}{},
		},
		Labels: map[string]string{
			"platform":  "minecraft-server-hosting",
			"server_id": strconv.FormatUint(uint64(req.ServerID), 10),
		},
	}

	hostConfig := &container.HostConfig{
		PortBindings: portMap,
		Binds:        binds,
		Resources: container.Resources{
			Memory:   int64(req.MemLimitMB) * 1024 * 1024,
			NanoCPUs: int64(req.CPULimit) * 1e9,
		},
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyUnlessStopped,
		},
	}

	containerName := fmt.Sprintf("mc-server-%d", req.ServerID)

	resp, err := d.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		if strings.Contains(err.Error(), "Conflict") || strings.Contains(err.Error(), "already in use") {
			_ = d.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
			resp, err = d.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
		}
		if err != nil {
			return nil, fmt.Errorf("failed creating container: %w", err)
		}
	}

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("failed starting container: %w", err)
	}

	return &CreateContainerResponse{
		ContainerID: resp.ID,
		Port:        req.Port,
		MountPath:   req.MountPath,
	}, nil
}

// Stop sends a graceful stop signal to a container.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: ID or name of the Docker container.
//
// Example usage:
//
//	err := cm.Stop(ctx, "abc123container")
func (d *DockerContainerManager) Stop(ctx context.Context, containerID string) error {
	stopTimeout := 15
	stopOpts := container.StopOptions{Timeout: &stopTimeout}
	if err := d.cli.ContainerStop(ctx, containerID, stopOpts); err != nil {
		return fmt.Errorf("failed stopping container %s: %w", containerID, err)
	}
	return nil
}

// Delete forces removal of the specified container.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID.
//
// Example usage:
//
//	err := cm.Delete(ctx, "abc123container")
func (d *DockerContainerManager) Delete(ctx context.Context, containerID string) error {
	removeOpts := container.RemoveOptions{
		Force:         true,
		RemoveVolumes: false,
	}
	if err := d.cli.ContainerRemove(ctx, containerID, removeOpts); err != nil {
		return fmt.Errorf("failed removing container %s: %w", containerID, err)
	}
	return nil
}

// GetStatus inspects container runtime state and reports status enum.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target container ID.
//
// Example usage:
//
//	status, err := cm.GetStatus(ctx, "abc123container")
func (d *DockerContainerManager) GetStatus(ctx context.Context, containerID string) (*ContainerStatusResponse, error) {
	inspect, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return &ContainerStatusResponse{
				ContainerID: containerID,
				Status:      ContainerStatusUnknown,
			}, nil
		}
		return nil, fmt.Errorf("failed inspecting container %s: %w", containerID, err)
	}

	var status ContainerStatus
	switch {
	case inspect.State.Running:
		status = ContainerStatusRunning
	case inspect.State.Status == "exited" || !inspect.State.Running:
		status = ContainerStatusStopped
	default:
		status = ContainerStatusUnknown
	}

	return &ContainerStatusResponse{
		ContainerID: containerID,
		Status:      status,
	}, nil
}

// CountActive returns the number of currently running containers managed by this platform.
//
// Arguments:
//   - ctx: Operation cancellation context.
//
// Example usage:
//
//	count, err := cm.CountActive(ctx)
func (d *DockerContainerManager) CountActive(ctx context.Context) (int, error) {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{
		All: false,
	})
	if err != nil {
		return 0, fmt.Errorf("failed listing containers: %w", err)
	}

	active := 0
	for _, c := range containers {
		if strings.HasPrefix(c.Names[0], "/mc-server-") || c.Labels["platform"] == "minecraft-server-hosting" {
			active++
		}
	}
	return active, nil
}
