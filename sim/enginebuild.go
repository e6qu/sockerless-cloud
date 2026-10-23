package sim

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// EngineBuild is an image the simulator builds on the container engine from
// sources it carries, for the engine's own architecture.
type EngineBuild struct {
	// Name is the image repository; the tag is a digest of the build context,
	// so a change to the sources builds a new image and an unchanged one
	// reuses the engine's copy.
	Name string
	// Context returns the build context for an architecture ("amd64",
	// "arm64"), its Dockerfile included.
	Context func(arch string) (map[string][]byte, error)
	// Bases are the images the Dockerfile's FROM lines name. They are pulled
	// first, with the retries a registry's throttle needs, so the build finds
	// them held.
	Bases []string
}

var engineBuildLocks = NewKeyedLocks()

// EnsureEngineBuild builds b on the engine cli reaches unless the engine holds
// it, and returns the image and its platform.
func EnsureEngineBuild(ctx context.Context, cli *client.Client, b EngineBuild) (string, string, error) {
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", "", fmt.Errorf("%s: engine info: %w", b.Name, err)
	}
	arch, err := EngineGoArch(info.Info.Architecture)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", b.Name, err)
	}
	files, err := b.Context(arch)
	if err != nil {
		return "", "", fmt.Errorf("%s: build context: %w", b.Name, err)
	}
	trust, err := hostTrustBundle()
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", b.Name, err)
	}
	files[EngineBuildTrustFile] = trust
	buildContext, sum, err := packBuildContext(files)
	if err != nil {
		return "", "", fmt.Errorf("%s: build context: %w", b.Name, err)
	}
	image := b.Name + ":" + sum[:16]
	platform := "linux/" + arch
	unlock := engineBuildLocks.Lock(image)
	defer unlock()
	if _, err := cli.ImageInspect(ctx, image); err == nil {
		return image, platform, nil
	}
	for _, base := range b.Bases {
		if err := PullImageOn(ctx, cli, base, platform); err != nil {
			return "", "", fmt.Errorf("%s: %w", b.Name, err)
		}
	}
	// The build reaches the network as the engine's own builds do, through
	// the proxy the engine is configured with; a proxy on the simulator's
	// loopback is out of a build container's reach. GOPROXY names a module
	// source, not a route, so it carries over.
	buildArgs := map[string]*string{}
	if v, ok := os.LookupEnv("GOPROXY"); ok {
		buildArgs["GOPROXY"] = &v
	}
	result, err := cli.ImageBuild(ctx, buildContext, client.ImageBuildOptions{
		Tags:       []string{image},
		Dockerfile: "Dockerfile",
		Remove:     true,
		BuildArgs:  buildArgs,
		Platforms:  []ocispec.Platform{{OS: "linux", Architecture: arch}},
	})
	if err != nil {
		return "", "", fmt.Errorf("%s: build %s: %w", b.Name, image, err)
	}
	defer func() { _ = result.Body.Close() }()
	if err := drainImageBuild(result.Body); err != nil {
		return "", "", fmt.Errorf("%s: build %s: %w", b.Name, image, err)
	}
	return image, platform, nil
}

// EngineBuildTrustFile is the build-context file holding the roots the
// simulator's host trusts beyond the system's own, from SSL_CERT_FILE, and
// empty when the host names none. A build that fetches over TLS appends it to
// its stage's CA store, so a build behind a TLS-inspecting proxy trusts what
// the simulator trusts:
//
//	COPY engine-build-ca.pem /tmp/
//	RUN cat /tmp/engine-build-ca.pem >> /etc/ssl/certs/ca-certificates.crt
const EngineBuildTrustFile = "engine-build-ca.pem"

func hostTrustBundle() ([]byte, error) {
	path := os.Getenv("SSL_CERT_FILE")
	if path == "" {
		return []byte{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the roots SSL_CERT_FILE names: %w", err)
	}
	return data, nil
}

// ReadEngineImageFile returns the file at path in image, read from a container
// created from the image and never started.
func ReadEngineImageFile(ctx context.Context, cli *client.Client, image, path string) ([]byte, error) {
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Entrypoint: []string{"/nonexistent"}},
	})
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w", path, image, err)
	}
	defer func() {
		_, _ = cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	copied, err := cli.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w", path, image, err)
	}
	defer func() { _ = copied.Content.Close() }()
	archive := tar.NewReader(copied.Content)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read %s from %s: the copy holds no regular file", path, image)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", path, image, err)
		}
		if header.Typeflag == tar.TypeReg {
			return io.ReadAll(archive)
		}
	}
}

// EngineGoArch names an engine's architecture as Go and OCI platforms do.
func EngineGoArch(engineArch string) (string, error) {
	switch engineArch {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("no build for the engine's architecture %q", engineArch)
}

// packBuildContext tars files in name order and digests names and contents.
func packBuildContext(files map[string][]byte) (io.Reader, string, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hash := sha256.New()
	for _, name := range names {
		data := files[name]
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(data)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, hex.EncodeToString(hash.Sum(nil)), nil
}

// drainImageBuild reads a build's progress stream to its end and returns the
// error it reports, with the last output line before it.
func drainImageBuild(stream io.Reader) error {
	dec := json.NewDecoder(stream)
	var last string
	for {
		var event struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&event); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("malformed build stream: %w", err)
		}
		if line := strings.TrimSpace(event.Stream); line != "" {
			last = line
		}
		if msg := event.Error + event.ErrorDetail.Message; msg != "" {
			return fmt.Errorf("%s (last output: %s)", msg, last)
		}
	}
}
