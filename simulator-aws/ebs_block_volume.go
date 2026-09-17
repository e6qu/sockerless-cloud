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

	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An Amazon ECS managed EBS volume is a block device of the size the task
// asked for, formatted with the filesystem it asked for. Each one is an image
// file of that size held in a volume of its own, loop-mounted by the container
// engine as the volume the task binds, so the workload sees the volume's own
// capacity and fills it up where EBS would.

// ebsFormatImage is the image that formats a volume's backing file. The engine
// builds it once from ebsFormatDockerfile.
const ebsFormatImage = "sockerless-ebs-format:1"

const ebsFormatDockerfile = "FROM alpine:latest\nRUN apk add --no-cache e2fsprogs xfsprogs\n"

// ebsImageFile is the backing file's path inside its holder volume.
const ebsImageFile = "disk.img"

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

var ebsFormatImageMu sync.Mutex

func ebsEnsureFormatImage(ctx context.Context, cli *dockerclient.Client) error {
	ebsFormatImageMu.Lock()
	defer ebsFormatImageMu.Unlock()
	if _, err := cli.ImageInspect(ctx, ebsFormatImage); err == nil {
		return nil
	}
	var buildContext bytes.Buffer
	archive := tar.NewWriter(&buildContext)
	if err := archive.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(ebsFormatDockerfile))}); err != nil {
		return err
	}
	if _, err := archive.Write([]byte(ebsFormatDockerfile)); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	result, err := cli.ImageBuild(ctx, &buildContext, dockerclient.ImageBuildOptions{
		Tags:       []string{ebsFormatImage},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("build %s: %w", ebsFormatImage, err)
	}
	defer func() { _ = result.Body.Close() }()
	return ebsDrainBuild(result.Body)
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
			return fmt.Errorf("build %s: malformed build stream: %w", ebsFormatImage, err)
		}
		if msg := event.Error + event.ErrorDetail.Message; msg != "" {
			return fmt.Errorf("build %s: %s", ebsFormatImage, msg)
		}
	}
}

// ebsCreateBlockVolume creates volumeName as a sizeGiB block device formatted
// with fsType. An engine that cannot loop-mount the image fails the first
// container that binds the volume, and the task reports why.
func ebsCreateBlockVolume(ctx context.Context, volumeName string, sizeGiB int, fsType string) error {
	cli := sim.DockerClient()
	if cli == nil {
		return errors.New("managed EBS volumes need a container engine")
	}
	if err := ebsEnsureFormatImage(ctx, cli); err != nil {
		return err
	}
	// The engine built the image for its own architecture, which the
	// simulator's is not guaranteed to be.
	built, err := cli.ImageInspect(ctx, ebsFormatImage)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", ebsFormatImage, err)
	}
	holder := ebsImageHolderName(volumeName)
	if _, err := cli.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{Name: holder}); err != nil {
		return fmt.Errorf("create %s: %w", holder, err)
	}
	inspected, err := cli.VolumeInspect(ctx, holder, dockerclient.VolumeInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect %s: %w", holder, err)
	}
	format := "mkfs." + fsType + " -q"
	if fsType == "xfs" {
		format += " -f"
	} else {
		format += " -F"
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        ebsFormatImage,
		Architecture: "linux/" + built.Architecture,
		Command: []string{"sh", "-c", fmt.Sprintf(
			"truncate -s %dG /image/%s && %s /image/%s", sizeGiB, ebsImageFile, format, ebsImageFile)},
		Binds:   []string{holder + ":/image"},
		Timeout: 10 * time.Minute,
	}, discardLogSink{})
	if err != nil {
		return fmt.Errorf("start format container: %w", err)
	}
	if res := handle.Wait(); res.Error != nil {
		return fmt.Errorf("format: %w", res.Error)
	} else if res.ExitCode != 0 {
		return fmt.Errorf("format exited with code %d", res.ExitCode)
	}

	options := "loop"
	info, err := cli.Info(ctx, dockerclient.InfoOptions{})
	if err != nil {
		return fmt.Errorf("read container engine info: %w", err)
	}
	if slices.ContainsFunc(info.Info.SecurityOptions, func(opt string) bool { return strings.Contains(opt, "name=selinux") }) {
		// An SELinux host denies a container writes to a filesystem mounted
		// without the container label.
		options += `,context="system_u:object_r:container_file_t:s0"`
	}
	if _, err := cli.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{
		Name:   volumeName,
		Driver: "local",
		DriverOpts: map[string]string{
			"type":   fsType,
			"device": strings.TrimSuffix(inspected.Volume.Mountpoint, "/") + "/" + ebsImageFile,
			"o":      options,
		},
	}); err != nil {
		return fmt.Errorf("create %s: %w", volumeName, err)
	}
	return nil
}
