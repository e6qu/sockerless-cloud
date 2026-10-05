package sim

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// volumeDeclaringTestImage declares `VOLUME /data`, so every container started
// from it gets an anonymous volume the engine creates on its behalf.
const volumeDeclaringTestImage = "public.ecr.aws/docker/library/redis:7.2-alpine"

// workloadVolumes reads the anonymous volume the image's VOLUME gave the
// container and confirms the named volume is mounted where the bind put it.
func workloadVolumes(t *testing.T, containerID, named string) string {
	t.Helper()
	inspected, err := DockerClient().ContainerInspect(t.Context(), containerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect workload: %v", err)
	}
	var anonymous string
	namedMounted := false
	for _, mount := range inspected.Container.Mounts {
		switch mount.Destination {
		case "/data":
			anonymous = mount.Name
		case "/named":
			namedMounted = mount.Name == named
		}
	}
	if anonymous == "" {
		t.Fatalf("the image's VOLUME /data gave the workload no anonymous volume: %+v", inspected.Container.Mounts)
	}
	if !namedMounted {
		t.Fatalf("named volume %s is not mounted at /named: %+v", named, inspected.Container.Mounts)
	}
	return anonymous
}

func assertVolumesAfterRemoval(t *testing.T, anonymous, named string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := DockerClient().VolumeInspect(ctx, anonymous, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
		_ = RemoveVolume(anonymous)
		t.Fatalf("anonymous volume %s outlived its removed container (inspect error: %v)", anonymous, err)
	}
	if !VolumeExists(named) {
		t.Fatalf("named volume %s did not survive the removal of a container that mounted it", named)
	}
}

func namedTestVolume(t *testing.T, path string) string {
	t.Helper()
	named := fmt.Sprintf("sockerless-sim-named-volume-%s-%d", path, time.Now().UnixNano())
	t.Cleanup(func() {
		if err := RemoveVolume(named); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove named volume %s: %v", named, err)
		}
	})
	return named
}

// A workload container the simulator removes takes the anonymous volumes its
// image declared with it, and leaves the named volumes it mounted, which
// outlive any one container by design.
func TestRemovedWorkloadLeavesNoAnonymousVolume(t *testing.T) {
	InitDocker("aws", true, t.TempDir())

	t.Run("run to completion", func(t *testing.T) {
		named := namedTestVolume(t, "run")
		handle, err := StartContainerSyncContext(t.Context(), ContainerConfig{
			Image:        volumeDeclaringTestImage,
			Architecture: "linux/" + runtime.GOARCH,
			Command:      []string{"sh"},
			Args:         []string{"-c", "sleep 300"},
			Name:         fmt.Sprintf("sockerless-sim-anonymous-volume-run-%d", time.Now().UnixNano()),
			Binds:        []string{named + ":/named"},
			Timeout:      2 * time.Minute,
		}, FuncSink(func(LogLine) {}))
		if err != nil {
			t.Fatalf("start workload: %v", err)
		}
		anonymous := workloadVolumes(t, handle.ContainerID, named)
		handle.Cancel()
		handle.Wait()
		assertVolumesAfterRemoval(t, anonymous, named)
	})

	t.Run("stop and remove", func(t *testing.T) {
		named := namedTestVolume(t, "http")
		containerID, err := StartHTTPContainer(t.Context(), HTTPContainerConfig{
			Image:        volumeDeclaringTestImage,
			Architecture: "linux/" + runtime.GOARCH,
			Command:      []string{"sh"},
			Args:         []string{"-c", "sleep 300"},
			Name:         fmt.Sprintf("sockerless-sim-anonymous-volume-http-%d", time.Now().UnixNano()),
			Binds:        []string{named + ":/named"},
		})
		if err != nil {
			t.Fatalf("start workload: %v", err)
		}
		anonymous := workloadVolumes(t, containerID, named)
		StopAndRemoveContainer(containerID, 0)
		assertVolumesAfterRemoval(t, anonymous, named)
	})
}
