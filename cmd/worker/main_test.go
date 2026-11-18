package main

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/dreamoutbox/minecraft-server-hosting-platform/internal/worker"
)

type spyContainerManager struct {
	events *[]string
}

func (s *spyContainerManager) CreateAndStart(ctx context.Context, req worker.CreateContainerRequest) (*worker.CreateContainerResponse, error) {
	return &worker.CreateContainerResponse{}, nil
}

func (s *spyContainerManager) Stop(ctx context.Context, containerID string) error { return nil }
func (s *spyContainerManager) Delete(ctx context.Context, containerID string) error {
	return nil
}
func (s *spyContainerManager) GetStatus(ctx context.Context, containerID string) (*worker.ContainerStatusResponse, error) {
	return &worker.ContainerStatusResponse{}, nil
}
func (s *spyContainerManager) GetLogs(ctx context.Context, containerID string, tail int) (*worker.ContainerLogsResponse, error) {
	return &worker.ContainerLogsResponse{}, nil
}
func (s *spyContainerManager) ExecCommand(ctx context.Context, containerID string, user string, command string) (*worker.ExecCommandResponse, error) {
	return &worker.ExecCommandResponse{}, nil
}
func (s *spyContainerManager) CountActive(ctx context.Context) (int, error) { return 0, nil }
func (s *spyContainerManager) CleanupAll(ctx context.Context) error {
	*s.events = append(*s.events, "containers.cleanup_all")
	return nil
}

type spyVolumeManager struct {
	events *[]string
}

func (s *spyVolumeManager) CreateVolume(ctx context.Context, volumeID string, diskLimitGB int) (string, string, error) {
	return "", "", nil
}
func (s *spyVolumeManager) DeleteVolume(ctx context.Context, volumeID string) error { return nil }
func (s *spyVolumeManager) EnsureMounted(ctx context.Context, volumeID string) error {
	return nil
}
func (s *spyVolumeManager) UnmountAll(ctx context.Context) error {
	*s.events = append(*s.events, "volumes.unmount_all")
	return nil
}
func (s *spyVolumeManager) ExportVolume(ctx context.Context, volumeID string, w io.Writer) error {
	return nil
}
func (s *spyVolumeManager) ImportVolume(ctx context.Context, volumeID string, r io.Reader) error {
	return nil
}
func (s *spyVolumeManager) GetVolumePath(volumeID string) (string, bool) { return "", false }

func TestShutdownWorkerOrder(t *testing.T) {
	var events []string
	cm := &spyContainerManager{events: &events}
	vm := &spyVolumeManager{events: &events}

	shutdownWorker(nil, cm, vm)

	want := []string{"containers.cleanup_all", "volumes.unmount_all"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("unexpected shutdown sequence: got %v, want %v", events, want)
	}
}
