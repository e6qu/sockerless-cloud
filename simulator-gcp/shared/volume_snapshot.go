package simulator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Volume snapshots for the managed-database services.
//
// A database instance's data directory is a named volume on the container
// engine. A backup captures that volume's contents into a new volume, and a
// restore clones a backup volume back over an instance volume — the same
// operation in the other direction.
//
// The capture is one command: `cp -a --reflink=auto`. On a container engine
// whose volume store sits on btrfs, on XFS with reflinks, or on OpenZFS 2.2+
// with block cloning enabled, that command clones blocks copy-on-write — the
// backup is O(metadata), effectively instant, and shares storage with its
// source until either diverges, exactly the behaviour an operator provisions
// such a filesystem for. On any other filesystem the same command performs a
// full copy, which is slower and still a complete, real capture. One code
// path; the filesystem underneath decides the speed; the cloud API above
// never changes shape either way.
//
// The copy runs in a one-shot helper container with the source mounted
// read-only, because the volume store belongs to the engine and may not even
// be on this host (a Docker Desktop virtual machine, a remote engine). The
// helper reports the filesystem it found, so logs say whether a deployment is
// getting the instant path without the API ever saying anything different.

const volumeSnapshotImage = "public.ecr.aws/docker/library/alpine:3.22"

// SnapshotVolume captures the full contents of the src volume into the dst
// volume, creating dst. It returns the filesystem the volume store reported,
// for logging — "btrfs" and "zfs" are the copy-on-write substrates.
func SnapshotVolume(ctx context.Context, src, dst string) (filesystem string, err error) {
	// `cp --reflink=auto` uses the filesystem's block cloning when it exists
	// and copies otherwise; `-a` keeps ownership, modes and timestamps, which
	// database engines check on startup. The trailing `/.` copies dotfiles.
	script := `set -e
stat -f -c %T /snapshot-src
cp -a --reflink=auto /snapshot-src/. /snapshot-dst/`
	var sink volumeSnapshotSink
	handle, err := StartContainerSync(ContainerConfig{
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
		Sandbox: SandboxCloudRun,
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
