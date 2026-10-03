package sim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Volume snapshots for the managed-database services.
//
// A database server's data directory is a named volume on the container
// engine. A snapshot captures that volume's contents into a new volume, and a
// restore clones a snapshot volume into a fresh server volume — the same
// operation in the other direction.
//
// The capture is one command: `cp -a --reflink=auto`. On a container engine
// whose volume store sits on btrfs, on XFS with reflinks, or on OpenZFS 2.2+
// with block cloning enabled, that command clones blocks copy-on-write — the
// snapshot is O(metadata), effectively instant, and shares storage with its
// source until either diverges, exactly the behaviour an operator provisions
// such a filesystem for. On any other filesystem the same command performs a
// full copy, which is slower and still a complete, real capture. One code
// path; the filesystem underneath decides the speed; the cloud API above
// never changes shape either way.
//
// A database engine writing to the source while cp walks it would leave a
// capture that holds each file as of a different moment, which the engine's
// crash recovery cannot always open. SnapshotVolume therefore freezes every
// running container that mounts the source writable — the cgroup freezer
// behind the Docker Engine's pause — for the length of the copy, so the
// capture is one crash-consistent point in time, the property a block-level
// storage snapshot gives. A frozen engine keeps its client connections; their
// I/O waits until the copy ends and the engine thaws.
//
// The copy runs in a one-shot helper container with the source mounted
// read-only, because the volume store belongs to the engine and may not even
// be on this host (a Docker Desktop virtual machine, a remote engine). The
// helper reports the filesystem it found, so logs say whether a deployment is
// getting the instant path without the API ever saying anything different.

const volumeSnapshotImage = "public.ecr.aws/docker/library/alpine:3.22"

// volumeCopySandbox confines the copy helper. It is the simulator's own
// plumbing, not a cloud workload, so it gets exactly what `cp -a` needs to
// preserve ownership and modes as root and nothing a workload profile grants.
var volumeCopySandbox = SandboxProfile{
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SnapshotVolume captures the full contents of the src volume into the dst
// volume, creating dst. It returns the filesystem the volume store reported,
// for logging — "btrfs" and "zfs" are the copy-on-write substrates.
func SnapshotVolume(ctx context.Context, src, dst string) (filesystem string, err error) {
	thaw, err := freezeVolumeWriters(ctx, src)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, thaw()) }()
	// `cp --reflink=auto` uses the filesystem's block cloning when it exists
	// and copies otherwise; `-a` keeps ownership, modes and timestamps, which
	// database engines check on startup. The trailing `/.` copies dotfiles.
	script := `set -e
stat -f -c %T /snapshot-src
cp -a --reflink=auto /snapshot-src/. /snapshot-dst/`
	var sink volumeSnapshotSink
	handle, err := StartContainerSyncContext(ctx, ContainerConfig{
		Image:        volumeSnapshotImage,
		Architecture: "linux/amd64",
		Command:      []string{"/bin/sh"},
		Args:         []string{"-c", script},
		Timeout:      10 * time.Minute,
		Binds: []string{
			src + ":/snapshot-src:ro",
			dst + ":/snapshot-dst",
		},
		Labels:  map[string]string{"sockerless-volume-snapshot": dst},
		Sandbox: volumeCopySandbox,
	}, &sink)
	if err != nil {
		return "", fmt.Errorf("start volume snapshot helper: %w", err)
	}
	result := handle.Wait()
	output := strings.TrimSpace(sink.String())
	if result.ExitCode != 0 || result.Error != nil {
		return "", fmt.Errorf("volume snapshot %s -> %s failed (exit %d, err %v): %s",
			src, dst, result.ExitCode, result.Error, output)
	}
	// The first output line is the stat -f filesystem type.
	filesystem = output
	if i := strings.IndexByte(filesystem, '\n'); i >= 0 {
		filesystem = filesystem[:i]
	}
	return strings.TrimSpace(filesystem), nil
}

// CaptureVolume snapshots the src volume into dst and logs, under
// "[sim-<tag>]", whether the filesystem gave the copy-on-write path. With no
// container engine, or no src volume — an engine that never started — there
// is nothing to capture, and dst is not created.
func CaptureVolume(ctx context.Context, src, dst, tag string) error {
	if RequireContainerRuntime("capturing volume "+src) != nil || !VolumeExists(src) {
		return nil
	}
	filesystem, err := SnapshotVolume(ctx, src, dst)
	if err != nil {
		return err
	}
	if VolumeSnapshotIsInstant(filesystem) {
		fmt.Fprintf(os.Stderr, "[sim-%s] volume %s captured copy-on-write on %s\n", tag, dst, filesystem)
	} else {
		fmt.Fprintf(os.Stderr, "[sim-%s] volume %s captured by full copy on %s (put the engine's volume store on btrfs, XFS with reflinks, or OpenZFS block cloning for instant snapshots)\n", tag, dst, filesystem)
	}
	return nil
}

// RemoveVolumeSettled removes a volume, retrying for up to 30 seconds while
// the engine still tears down the container that held it: a container's
// removal completes on a goroutine its handle's Wait does not cover. A volume
// that stays is logged under "[sim-<tag>]".
func RemoveVolumeSettled(name, tag string) {
	if RequireContainerRuntime("removing volume "+name) != nil || !VolumeExists(name) {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := RemoveVolume(name)
		if err == nil || !VolumeExists(name) {
			return
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "[sim-%s] volume %s was not removed: %v\n", tag, name, err)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// VolumeSnapshotIsInstant reports whether the filesystem SnapshotVolume found
// clones blocks copy-on-write, for the log line that tells an operator
// whether their deployment has the instant path.
func VolumeSnapshotIsInstant(filesystem string) bool {
	switch strings.ToLower(filesystem) {
	case "btrfs", "zfs", "xfs":
		return true
	}
	return false
}

// volumeFreezes counts, per container, the captures that hold it frozen, so
// concurrent captures of one volume pause its writers once and thaw them when
// the last capture ends, and the commands that hold it thawed, which a capture
// waits out before it pauses the container. changed closes, and is replaced, on
// every count change.
var volumeFreezes = struct {
	sync.Mutex
	holds   map[string]int
	thawed  map[string]int
	changed chan struct{}
}{holds: map[string]int{}, thawed: map[string]int{}, changed: make(chan struct{})}

func broadcastVolumeFreezeChange() {
	close(volumeFreezes.changed)
	volumeFreezes.changed = make(chan struct{})
}

// awaitVolumeFreezeChange waits, with volumeFreezes locked on entry and on
// return, until a count changes or ctx ends.
func awaitVolumeFreezeChange(ctx context.Context) error {
	changed := volumeFreezes.changed
	volumeFreezes.Unlock()
	defer volumeFreezes.Lock()
	select {
	case <-changed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// HoldThawed waits until no volume capture holds the container frozen, then
// keeps new captures from freezing it until release runs. The Docker Engine
// refuses to exec into a paused container, so a command run into a database
// engine's container waits out a snapshot's brief I/O suspension under this
// hold.
func HoldThawed(ctx context.Context, containerID string) (release func(), err error) {
	volumeFreezes.Lock()
	defer volumeFreezes.Unlock()
	for volumeFreezes.holds[containerID] > 0 {
		if err := awaitVolumeFreezeChange(ctx); err != nil {
			return nil, fmt.Errorf("wait for container %s to thaw: %w", containerID, err)
		}
	}
	volumeFreezes.thawed[containerID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			volumeFreezes.Lock()
			defer volumeFreezes.Unlock()
			if volumeFreezes.thawed[containerID]--; volumeFreezes.thawed[containerID] == 0 {
				delete(volumeFreezes.thawed, containerID)
			}
			broadcastVolumeFreezeChange()
		})
	}, nil
}

// freezeVolumeWriters pauses every running container that mounts volume
// writable and returns the function that thaws them.
func freezeVolumeWriters(ctx context.Context, volume string) (thaw func() error, err error) {
	filters := client.Filters{}
	filters.Add("volume", volume)
	listed, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list the writers of volume %s: %w", volume, err)
	}
	var frozen []string
	thaw = func() error {
		var errs []error
		for _, id := range frozen {
			errs = append(errs, releaseVolumeFreeze(id))
		}
		return errors.Join(errs...)
	}
	for _, summary := range listed.Items {
		if !mountsVolumeWritable(summary.Mounts, volume) {
			continue
		}
		if err := holdVolumeFreeze(ctx, summary.ID); err != nil {
			return nil, errors.Join(fmt.Errorf("freeze container %s, a writer of volume %s: %w", summary.ID, volume, err), thaw())
		}
		frozen = append(frozen, summary.ID)
	}
	return thaw, nil
}

func mountsVolumeWritable(mounts []container.MountPoint, volume string) bool {
	for _, mount := range mounts {
		if mount.Name == volume && mount.RW {
			return true
		}
	}
	return false
}

func holdVolumeFreeze(ctx context.Context, containerID string) error {
	volumeFreezes.Lock()
	defer volumeFreezes.Unlock()
	for volumeFreezes.thawed[containerID] > 0 {
		if err := awaitVolumeFreezeChange(ctx); err != nil {
			return err
		}
	}
	if volumeFreezes.holds[containerID] == 0 {
		if _, err := dockerClient.ContainerPause(ctx, containerID, client.ContainerPauseOptions{}); err != nil {
			return err
		}
	}
	volumeFreezes.holds[containerID]++
	return nil
}

// releaseVolumeFreeze thaws the container once no capture holds it, on a
// context of its own: a capture cancelled mid-copy must still thaw its engine.
func releaseVolumeFreeze(containerID string) error {
	volumeFreezes.Lock()
	defer volumeFreezes.Unlock()
	volumeFreezes.holds[containerID]--
	if volumeFreezes.holds[containerID] > 0 {
		return nil
	}
	delete(volumeFreezes.holds, containerID)
	defer broadcastVolumeFreezeChange()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := dockerClient.ContainerUnpause(ctx, containerID, client.ContainerUnpauseOptions{}); err != nil && !containerNotFoundError(err) {
		return fmt.Errorf("thaw container %s: %w", containerID, err)
	}
	return nil
}

func thawAbandonedFreeze(containerID string) error {
	volumeFreezes.Lock()
	defer volumeFreezes.Unlock()
	if volumeFreezes.holds[containerID] > 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inspected, err := dockerClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if containerNotFoundError(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Paused {
		return nil
	}
	if _, err := dockerClient.ContainerUnpause(ctx, containerID, client.ContainerUnpauseOptions{}); err != nil {
		return fmt.Errorf("thaw container %s: %w", containerID, err)
	}
	return nil
}

// volumeSnapshotSink collects the helper's few output lines, which carry the
// filesystem type and any cp error.
type volumeSnapshotSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *volumeSnapshotSink) WriteLog(line LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line.Text)
}

func (s *volumeSnapshotSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}
