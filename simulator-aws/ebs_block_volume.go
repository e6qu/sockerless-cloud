package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An Amazon ECS managed EBS volume is a block device of the size the task
// asked for, formatted with the filesystem it asked for. Each one is an image
// file of that size held in a volume of its own, attached to a loop device,
// and handed to the engine as the block device its volume is mounted from —
// so the workload sees the volume's own capacity and fills it up where EBS
// would.
//
// The loop device is attached here, by a privileged helper container running
// losetup, and never by the container engine. Leaving it to the engine — the
// local driver's DriverOpts "o": "loop" — works on one engine and cannot work
// on the other, because the two do entirely different things with "o":
//
//   - Podman hands it to mount(8). libpod/volume_internal_common.go: "We need
//     to use the actual mount command. Convincing unix.Mount to use the same
//     semantics as the mount command itself seems prohibitively difficult."
//     It runs /bin/mount -o <o> -t <type> <device> <mountpoint>, and mount(8)
//     implements -o loop in userspace by calling losetup itself.
//
//   - moby hands it to mount(2). daemon/volume/local/local_unix.go calls
//     mount.Mount(device, path, type, o) from github.com/moby/sys/mount, whose
//     parseOptions maps each comma-separated option to an MS_* flag and passes
//     everything it does not recognise straight through as the filesystem's
//     own mount data: "If the option does not exist in the flags table ... then
//     it is a data value for a specific fs type". "loop" has never been in that
//     table, in any moby release, so xfs and ext4 are handed an option they
//     have never heard of and the kernel answers EINVAL:
//
//     failed to mount local volume: mount /var/lib/docker/volumes/
//     sockerless-ebs-vol-68b3e422-image/_data/disk.img:/var/lib/docker/
//     volumes/sockerless-ebs-vol-68b3e422/_data, data: loop: invalid argument
//
// A loop device is a kernel-global object with no namespace of its own: the
// helper container attaches one, and the daemon — another process, in another
// mount namespace — mounts /dev/loopN from the same kernel. So the volume is
// created with a device the engine only has to open, and neither engine is
// asked to parse an option only one of them understands.
//
// The SELinux "context=" option further down is a different thing entirely:
// that is real mount(2) data, which the kernel and mount(8) both accept, and
// without it an SELinux host denies the workload every write to the volume.
// It stays.

// ebsHelperImage is the image that formats a block volume's backing file and
// attaches and detaches its loop device. The engine builds it once from
// ebsHelperDockerfile.
const ebsHelperImage = "sockerless-ebs-helper:1"

// The losetup package is util-linux's losetup. Alpine's default is busybox's
// applet of the same name, which takes no long options and cannot list the
// devices a given backing file is attached to, which is how the detach path
// finds them.
const ebsHelperDockerfile = "FROM alpine:latest\nRUN apk add --no-cache e2fsprogs xfsprogs losetup\n"

// ebsImageFile is the backing file's path inside its holder volume.
const ebsImageFile = "disk.img"

// ebsImageMount is where a helper container binds the holder volume.
const ebsImageMount = "/image"

// ebsDefaultFilesystemType is the filesystem Amazon ECS formats a managed
// volume with when the task names none.
const ebsDefaultFilesystemType = "xfs"

var ebsFilesystemTypes = []string{"ext3", "ext4", "xfs"}

func ebsValidFilesystemType(fsType string) bool {
	return slices.Contains(ebsFilesystemTypes, fsType)
}

// ebsImageHolderName names the volume that holds a block volume's image file.
func ebsImageHolderName(volumeName string) string {
	return volumeName + "-image"
}

// ebsFormatSandbox confines the helper that creates and formats the backing
// file. Writing and formatting a file inside a volume the helper owns needs no
// privilege at all, so it gets none.
var ebsFormatSandbox = sim.SandboxProfile{
	CapDrop:          []string{"ALL"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// ebsLoopSandbox confines the helper that attaches and detaches the loop
// device. Attaching one needs the real thing: an ioctl on /dev/loop-control
// and on a block device node the helper may have to create. This is the
// platform-managed system container SandboxProfile.Privileged exists for, not
// a workload — no task's code ever runs under it, and the only command it runs
// is losetup.
var ebsLoopSandbox = sim.SandboxProfile{
	Privileged:       true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// ebsAttachScript attaches the backing file to a free loop device and prints
// that device, and nothing else, on stdout.
const ebsAttachScript = `set -eu
img="` + ebsImageMount + `/` + ebsImageFile + `"
attempt=0
while [ "$attempt" -lt 16 ]; do
	attempt=$((attempt + 1))
	# losetup --find asks the kernel through /dev/loop-control for a free loop
	# device, creating one when every existing device is busy. It appends a
	# literal " (lost)" to the name when the device node is missing, so take
	# the first field rather than the whole line.
	free=$(losetup --find)
	dev=${free%% *}
	if [ ! -b "$dev" ]; then
		# This container's /dev is a private tmpfs the engine populated when
		# the container was created, so a device the kernel only made just now
		# has no node in it even though the device exists. /sys/block carries
		# the numbers to make one with.
		majmin=$(cat "/sys/block/${dev#/dev/}/dev")
		mknod "$dev" b "${majmin%%:*}" "${majmin##*:}"
	fi
	# Another volume being created at this moment can claim the device the
	# kernel had just called free. That is contention, not failure: ask again.
	if losetup "$dev" "$img"; then
		echo "$dev"
		exit 0
	fi
done
echo "could not attach $img to a loop device in $attempt attempts; the losetup errors above say why" >&2
exit 1
`

// ebsDetachScript detaches every loop device the backing file is attached to.
const ebsDetachScript = `set -eu
img="` + ebsImageMount + `/` + ebsImageFile + `"
if [ ! -f "$img" ]; then
	# A holder volume whose image was never formatted has nothing attached.
	exit 0
fi
# Ask by backing file rather than by a device number remembered somewhere else:
# losetup --associated matches on the file's device and inode, so it names
# exactly the loop devices this volume's own image is attached to, however the
# kernel renders the path (it renders it against the reader's mount namespace,
# so the recorded path is not this container's) and however many there are.
for dev in $(losetup --noheadings --output NAME --associated "$img"); do
	losetup --detach "$dev"
	echo "detached $dev"
done
`

var ebsHelperImageMu sync.Mutex

// ebsEnsureHelperImage builds the helper image if the engine does not already
// have it, and reports the architecture the engine built it for — which the
// simulator's own is not guaranteed to be.
func ebsEnsureHelperImage(ctx context.Context, cli *dockerclient.Client) (string, error) {
	ebsHelperImageMu.Lock()
	defer ebsHelperImageMu.Unlock()
	if built, err := cli.ImageInspect(ctx, ebsHelperImage); err == nil {
		return built.Architecture, nil
	}
	var buildContext bytes.Buffer
	archive := tar.NewWriter(&buildContext)
	if err := archive.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(ebsHelperDockerfile))}); err != nil {
		return "", err
	}
	if _, err := archive.Write([]byte(ebsHelperDockerfile)); err != nil {
		return "", err
	}
	if err := archive.Close(); err != nil {
		return "", err
	}
	result, err := cli.ImageBuild(ctx, &buildContext, dockerclient.ImageBuildOptions{
		Tags:       []string{ebsHelperImage},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return "", fmt.Errorf("build %s: %w", ebsHelperImage, err)
	}
	defer func() { _ = result.Body.Close() }()
	if err := ebsDrainBuild(result.Body); err != nil {
		return "", err
	}
	built, err := cli.ImageInspect(ctx, ebsHelperImage)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", ebsHelperImage, err)
	}
	return built.Architecture, nil
}

func ebsDrainBuild(stream io.Reader) error {
	dec := json.NewDecoder(stream)
	for {
		var event struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&event); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("build %s: malformed build stream: %w", ebsHelperImage, err)
		}
		if msg := event.Error + event.ErrorDetail.Message; msg != "" {
			return fmt.Errorf("build %s: %s", ebsHelperImage, msg)
		}
	}
}

// ebsHelperSink collects a helper container's few output lines: the loop
// device it attached, and whatever it said when it could not.
type ebsHelperSink struct {
	mu     sync.Mutex
	stdout []string
	all    []string
}

func (s *ebsHelperSink) WriteLog(line sim.LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all = append(s.all, line.Text)
	if line.Stream == "stdout" {
		s.stdout = append(s.stdout, line.Text)
	}
}

func (s *ebsHelperSink) out() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(strings.Join(s.stdout, "\n"))
}

func (s *ebsHelperSink) said() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	said := strings.TrimSpace(strings.Join(s.all, "; "))
	if said == "" {
		return "no output"
	}
	return said
}

// ebsRunHelper runs one command over the volume holding a block volume's image
// file and returns what it wrote to stdout. Every failure names the step and
// carries what the container said about it, because the caller turns this into
// the task's StoppedReason and that is all an operator gets to read.
func ebsRunHelper(ctx context.Context, cli *dockerclient.Client, step, holder, script string, sandbox sim.SandboxProfile, timeout time.Duration) (string, error) {
	architecture, err := ebsEnsureHelperImage(ctx, cli)
	if err != nil {
		return "", err
	}
	var sink ebsHelperSink
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        ebsHelperImage,
		Architecture: "linux/" + architecture,
		Command:      []string{"sh", "-c", script},
		Binds:        []string{holder + ":" + ebsImageMount},
		Timeout:      timeout,
		Sandbox:      sandbox,
	}, &sink)
	if err != nil {
		return "", fmt.Errorf("start %s container: %w", step, err)
	}
	if result := handle.Wait(); result.Error != nil {
		return "", fmt.Errorf("%s: %w: %s", step, result.Error, sink.said())
	} else if result.ExitCode != 0 {
		return "", fmt.Errorf("%s exited with code %d: %s", step, result.ExitCode, sink.said())
	}
	return sink.out(), nil
}

// ebsCreateBlockVolume creates volumeName as a sizeGiB block device formatted
// with fsType.
func ebsCreateBlockVolume(ctx context.Context, volumeName string, sizeGiB int, fsType string) error {
	cli := sim.DockerClient()
	if cli == nil {
		return errors.New("managed EBS volumes need a container engine")
	}
	holder := ebsImageHolderName(volumeName)
	if _, err := cli.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{Name: holder}); err != nil {
		return fmt.Errorf("create %s: %w", holder, err)
	}
	format := "mkfs." + fsType + " -q"
	if fsType == "xfs" {
		format += " -f"
	} else {
		format += " -F"
	}
	// truncate leaves the image sparse, so a volume costs what the workload
	// writes to it while presenting the capacity it was asked for.
	formatScript := fmt.Sprintf("set -eu\ntruncate -s %dG %s/%s\n%s %s/%s\n",
		sizeGiB, ebsImageMount, ebsImageFile, format, ebsImageMount, ebsImageFile)
	if _, err := ebsRunHelper(ctx, cli, "format", holder, formatScript, ebsFormatSandbox, 10*time.Minute); err != nil {
		return err
	}

	// Everything the volume options need is read before the loop device is
	// attached, so that the only work between attaching one and handing it to
	// the engine is handing it to the engine.
	driverOpts := map[string]string{"type": fsType}
	info, err := cli.Info(ctx, dockerclient.InfoOptions{})
	if err != nil {
		return fmt.Errorf("read container engine info: %w", err)
	}
	if slices.ContainsFunc(info.Info.SecurityOptions, func(opt string) bool { return strings.Contains(opt, "name=selinux") }) {
		// An SELinux host denies a container writes to a filesystem mounted
		// without the container label. Unlike "loop", this is real mount(2)
		// data that both engines pass through to the kernel unchanged.
		driverOpts["o"] = `context="system_u:object_r:container_file_t:s0"`
	}

	device, err := ebsRunHelper(ctx, cli, "loop device attach", holder, ebsAttachScript, ebsLoopSandbox, 2*time.Minute)
	if err != nil {
		return err
	}
	// From here on a loop device is attached and only this call knows it:
	// nothing else would ever collect one, so every failure below gives it
	// back rather than leak a kernel object per volume that did not work out.
	failed := func(err error) error {
		if detachErr := ebsDetachLoopDevices(ctx, cli, holder); detachErr != nil {
			return fmt.Errorf("%w (and its loop device stayed attached: %v)", err, detachErr)
		}
		return err
	}
	// A volume mounted from something that is not a block device would pass
	// for working while having none of a volume's capacity: refuse it here,
	// where what happened is still known.
	if !strings.HasPrefix(device, "/dev/loop") {
		return failed(fmt.Errorf("loop device attach named %q, which is not a loop device", device))
	}
	driverOpts["device"] = device
	if _, err := cli.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{
		Name:       volumeName,
		Driver:     "local",
		DriverOpts: driverOpts,
	}); err != nil {
		return failed(fmt.Errorf("create %s: %w", volumeName, err))
	}
	return nil
}

// ebsDetachLoopDevices gives back every loop device the block volume's image
// file is attached to. A loop device is the kernel's, not the engine's:
// removing the volume mounted from it unmounts the filesystem and leaves the
// device attached to a file nothing will ever read again, so every path that
// removes a block volume has to come through here or leak one device per
// volume it removed.
//
// A holder volume the engine does not have is not an error: a snapshot volume,
// or any volume that was never a block volume, has no image file and nothing
// attached to it.
func ebsDetachLoopDevices(ctx context.Context, cli *dockerclient.Client, holder string) error {
	if _, err := cli.VolumeInspect(ctx, holder, dockerclient.VolumeInspectOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", holder, err)
	}
	_, err := ebsRunHelper(ctx, cli, "loop device detach", holder, ebsDetachScript, ebsLoopSandbox, 2*time.Minute)
	return err
}
