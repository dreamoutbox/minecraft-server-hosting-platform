package nodemanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/dreamoutbox/minecraft-server-hosting-platform/shared/nodemanagerclient"
)

const (
	managedPlatformLabel = "minecraft-server-hosting"
	managedNamePrefix    = "mc-server-"
	defaultStopTimeout   = 10
)

// ContainerManager defines lifecycle operations on Docker containers for Minecraft servers.
type ContainerManager interface {
	CreateAndStart(ctx context.Context, req nodemanagerclient.CreateContainerRequest) (*nodemanagerclient.CreateContainerResponse, error)
	Stop(ctx context.Context, containerID string) error
	Delete(ctx context.Context, containerID string) error
	GetStatus(ctx context.Context, containerID string) (*nodemanagerclient.ContainerStatusResponse, error)
	GetLogs(ctx context.Context, containerID string, tail int) (*nodemanagerclient.ContainerLogsResponse, error)
	ExecCommand(ctx context.Context, containerID string, user string, command string) (*nodemanagerclient.ExecCommandResponse, error)
	CountActive(ctx context.Context) (int, error)
	CleanupAll(ctx context.Context) error
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
func (d *DockerContainerManager) CreateAndStart(ctx context.Context, req nodemanagerclient.CreateContainerRequest) (*nodemanagerclient.CreateContainerResponse, error) {
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

	envVars := []string{
		"EULA=TRUE",
		"ONLINE_MODE=FALSE",
		"CREATE_CONSOLE_IN_PIPE=true",
		"USE_MEOWICE_FLAGS=TRUE",
	}
	if req.ServerType != "" {
		envVars = append(envVars, "TYPE="+strings.ToUpper(req.ServerType))
	}
	if req.ServerVersion != "" {
		envVars = append(envVars, "VERSION="+req.ServerVersion)
	}

	containerConfig := &container.Config{
		Image: img,
		Env:   envVars,
		ExposedPorts: nat.PortSet{
			containerPort: struct{}{},
		},
		Labels: map[string]string{
			"platform":  managedPlatformLabel,
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

	containerName := fmt.Sprintf("%s%d", managedNamePrefix, req.ServerID)

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

	return &nodemanagerclient.CreateContainerResponse{
		ContainerID: resp.ID,
		Port:        req.Port,
		MountPath:   req.MountPath,
	}, nil
}

// Stop sends a graceful stop signal to a container, succeeding if already absent.
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
		if client.IsErrNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed stopping container %s: %w", containerID, err)
	}
	return nil
}

// Delete forces removal of the specified container, succeeding if already absent.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID or name.
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
		if client.IsErrNotFound(err) {
			return nil
		}
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
func (d *DockerContainerManager) GetStatus(ctx context.Context, containerID string) (*nodemanagerclient.ContainerStatusResponse, error) {
	inspect, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return &nodemanagerclient.ContainerStatusResponse{
				ContainerID: containerID,
				Status:      nodemanagerclient.ContainerStatusUnknown,
			}, nil
		}
		return nil, fmt.Errorf("failed inspecting container %s: %w", containerID, err)
	}

	var status nodemanagerclient.ContainerStatus
	switch {
	case inspect.State.Running:
		status = nodemanagerclient.ContainerStatusRunning
	case inspect.State.Status == "exited" || !inspect.State.Running:
		status = nodemanagerclient.ContainerStatusStopped
	default:
		status = nodemanagerclient.ContainerStatusUnknown
	}

	return &nodemanagerclient.ContainerStatusResponse{
		ContainerID: containerID,
		Status:      status,
	}, nil
}

// GetLogs fetches the trailing stdout/stderr lines from a container's console output.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID.
//   - tail: Maximum number of trailing lines to return (defaults to 200 if <= 0).
//
// Example usage:
//
//	logs, err := cm.GetLogs(ctx, "abc123container", 200)
func (d *DockerContainerManager) GetLogs(ctx context.Context, containerID string, tail int) (*nodemanagerclient.ContainerLogsResponse, error) {
	if tail <= 0 {
		tail = 200
	}
	if tail > 2000 {
		tail = 2000
	}

	reader, err := d.cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return nil, fmt.Errorf("failed fetching logs for container %s: %w", containerID, err)
	}
	defer func() { _ = reader.Close() }()

	combined, err := demuxLogStream(reader)
	if err != nil {
		return nil, fmt.Errorf("failed reading container log stream %s: %w", containerID, err)
	}

	return &nodemanagerclient.ContainerLogsResponse{
		ContainerID: containerID,
		Logs:        combined,
	}, nil
}

// demuxLogStream strips Docker's 8-byte stream framing while preserving the
// chronological interleaving of stdout and stderr frames as a terminal would.
//
// Arguments:
//   - r: Raw multiplexed stream returned by Docker's ContainerLogs API.
//
// Example usage:
//
//	logs, err := demuxLogStream(reader)
func demuxLogStream(r io.Reader) (string, error) {
	// Pass the same buffer for both streams so frames stay in arrival order
	// instead of grouping all stderr lines after stdout.
	var combined bytes.Buffer
	if _, err := stdcopy.StdCopy(&combined, &combined, r); err != nil {
		return "", err
	}
	return combined.String(), nil
}

// ExecCommand sends a Minecraft server command via mc-send-to-console inside the running container.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - containerID: Target Docker container ID.
//   - command: Minecraft console command string (without leading slash).
//
// Example usage:
//
//	res, err := cm.ExecCommand(ctx, "abc123container", "list")
func (d *DockerContainerManager) ExecCommand(ctx context.Context, containerID string, user string, command string) (*nodemanagerclient.ExecCommandResponse, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil, errors.New("command cannot be empty")
	}

	execCfg := container.ExecOptions{
		User:         user,
		Cmd:          []string{"mc-send-to-console", trimmed},
		AttachStdout: true,
		AttachStderr: true,
	}
	execCreated, err := d.cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("failed creating exec in container %s: %w", containerID, err)
	}

	attachResp, err := d.cli.ContainerExecAttach(ctx, execCreated.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed attaching exec in container %s: %w", containerID, err)
	}
	defer attachResp.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader); err != nil {
		return nil, fmt.Errorf("failed reading exec output in container %s: %w", containerID, err)
	}

	inspectResp, err := d.cli.ContainerExecInspect(ctx, execCreated.ID)
	if err != nil {
		return nil, fmt.Errorf("failed inspecting exec in container %s: %w", containerID, err)
	}

	output := strings.TrimSpace(stdoutBuf.String() + stderrBuf.String())
	if inspectResp.ExitCode != 0 {
		return nil, fmt.Errorf("mc-send-to-console exited with code %d: %s", inspectResp.ExitCode, output)
	}

	return &nodemanagerclient.ExecCommandResponse{
		ContainerID: containerID,
		Command:     trimmed,
		Output:      output,
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
		if isManagedContainer(c) {
			active++
		}
	}
	return active, nil
}

// CleanupAll gracefully stops and removes all containers managed by this platform.
//
// Arguments:
//   - ctx: Operation cancellation context bounding stop and removal duration.
//
// Example usage:
//
//	err := cm.CleanupAll(ctx)
func (d *DockerContainerManager) CleanupAll(ctx context.Context) error {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		return fmt.Errorf("failed listing managed containers: %w", err)
	}

	var managed []types.Container
	for _, c := range containers {
		if isManagedContainer(c) {
			managed = append(managed, c)
		}
	}
	if len(managed) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(managed))
	stopTimeout := defaultStopTimeout

	for _, c := range managed {
		wg.Add(1)
		go func(ctr types.Container) {
			defer wg.Done()
			if ctr.State == "running" {
				// Best-effort graceful stop so Minecraft flushes world chunks before removal.
				_ = d.cli.ContainerStop(ctx, ctr.ID, container.StopOptions{Timeout: &stopTimeout})
			}
			if rmErr := d.Delete(ctx, ctr.ID); rmErr != nil {
				errCh <- rmErr
			}
		}(c)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	return errors.Join(errs...)
}

func isManagedContainer(c types.Container) bool {
	if c.Labels["platform"] == managedPlatformLabel {
		return true
	}
	for _, name := range c.Names {
		trimmed := strings.TrimPrefix(name, "/")
		if strings.HasPrefix(trimmed, managedNamePrefix) {
			return true
		}
	}
	return false
}
