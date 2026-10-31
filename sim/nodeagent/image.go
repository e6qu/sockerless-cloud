package nodeagent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/moby/moby/client"
)

// The simulator builds the agent image on the engine from these sources: the
// agent and guestinit compile in a Go stage, and the runtime stage carries
// Firecracker and the guest kernel at pinned releases with their digests. Only
// an engine a service is configured onto microVMs for ever builds it.
//
//go:embed cmd/nodeagent/*.go cmd/guestinit/*.go
var sources embed.FS

const (
	firecrackerVersion = "v1.15.1"
	kernelVersion      = "6.1.155"
	goImage            = "mirror.gcr.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb"
	runtimeImage       = "mirror.gcr.io/library/ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
)

// The release archives and CI kernels Firecracker publishes, by architecture.
var assets = map[string]struct{ firecracker, kernel string }{
	"amd64": {"d4a32ab2322d887ca1bc4a4e7afa9cc35393e6362dfc2b3becb389d362e4275a", "e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2"},
	"arm64": {"00654ac1e702a22744121ea9f10a4f792ebd7c3a744cba587dfac9fcb79b41a5", "e3544b10603acbf3db492cb52e000d22ba202cb4b63b9add027565683e11c591"},
}

const goMod = "module sockerless-nodeagent\n\ngo 1.26\n\nrequire golang.org/x/sys v0.49.0\n"

func dockerfile(arch string) string {
	a := assets[arch]
	fcArch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
	return fmt.Sprintf(`FROM %[1]s AS build
WORKDIR /src
COPY go.mod ./
COPY nodeagent ./nodeagent
COPY guestinit ./guestinit
ENV GOFLAGS=-mod=mod CGO_ENABLED=0
RUN go build -trimpath -o /out/nodeagent ./nodeagent && go build -trimpath -o /out/guestinit ./guestinit

FROM %[2]s
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl e2fsprogs iproute2 nftables && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    curl -fsSL --retry 5 --retry-delay 5 -o /tmp/fc.tgz https://github.com/firecracker-microvm/firecracker/releases/download/%[3]s/firecracker-%[3]s-%[4]s.tgz; \
    echo "%[5]s  /tmp/fc.tgz" | sha256sum -c -; \
    tar -xzf /tmp/fc.tgz -C /tmp; \
    install -m 0755 /tmp/release-%[3]s-%[4]s/firecracker-%[3]s-%[4]s /usr/local/bin/firecracker; \
    rm -rf /tmp/fc.tgz /tmp/release-%[3]s-%[4]s; \
    mkdir -p /usr/local/lib/sockerless; \
    curl -fsSL --retry 5 --retry-delay 5 -o /usr/local/lib/sockerless/vmlinux https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/%[4]s/vmlinux-%[6]s; \
    echo "%[7]s  /usr/local/lib/sockerless/vmlinux" | sha256sum -c -
COPY --from=build /out/nodeagent /usr/local/bin/nodeagent
COPY --from=build /out/guestinit /usr/local/lib/sockerless/guestinit
ENTRYPOINT ["/usr/local/bin/nodeagent"]
`, goImage, runtimeImage, firecrackerVersion, fcArch, a.firecracker, kernelVersion, a.kernel)
}

var imageMu sync.Mutex

// EnsureImage builds the agent image for the engine's architecture unless the
// engine holds it, and returns its name: the sources' digest names it, so a
// change to the agent rebuilds it and an unchanged one reuses it.
func EnsureImage(ctx context.Context, cli *client.Client) (string, string, error) {
	imageMu.Lock()
	defer imageMu.Unlock()
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", "", fmt.Errorf("node agent: engine info: %w", err)
	}
	arch, err := goArch(info.Info.Architecture)
	if err != nil {
		return "", "", err
	}
	if _, ok := assets[arch]; !ok {
		return "", "", fmt.Errorf("node agent: no Firecracker release for %s", arch)
	}
	context, sum, err := buildContext(arch)
	if err != nil {
		return "", "", err
	}
	image := "sockerless-node-agent:" + sum[:16]
	platform := "linux/" + arch
	if _, err := cli.ImageInspect(ctx, image); err == nil {
		return image, platform, nil
	}
	// The build's FROM lines would pull the bases without retrying a
	// registry's throttle; pulled first, they are held when the build runs.
	for _, base := range []string{goImage, runtimeImage} {
		if err := sim.PullImageOn(ctx, cli, base, platform); err != nil {
			return "", "", fmt.Errorf("node agent: %w", err)
		}
	}
	buildArgs := map[string]*string{}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(key); ok {
			value := v
			buildArgs[key] = &value
		}
	}
	result, err := cli.ImageBuild(ctx, context, client.ImageBuildOptions{
		Tags:       []string{image},
		Dockerfile: "Dockerfile",
		Remove:     true,
		BuildArgs:  buildArgs,
	})
	if err != nil {
		return "", "", fmt.Errorf("node agent: build %s: %w", image, err)
	}
	defer func() { _ = result.Body.Close() }()
	if err := drainBuild(result.Body); err != nil {
		return "", "", fmt.Errorf("node agent: build %s: %w", image, err)
	}
	return image, platform, nil
}

// buildContext packs the Dockerfile and the sources, and digests them.
func buildContext(arch string) (io.Reader, string, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hash := sha256.New()
	add := func(name string, data []byte) error {
		hash.Write([]byte(name))
		hash.Write(data)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := add("Dockerfile", []byte(dockerfile(arch))); err != nil {
		return nil, "", err
	}
	if err := add("go.mod", []byte(goMod)); err != nil {
		return nil, "", err
	}
	for _, dir := range []string{"nodeagent", "guestinit"} {
		entries, err := fs.ReadDir(sources, "cmd/"+dir)
		if err != nil {
			return nil, "", err
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			data, err := sources.ReadFile("cmd/" + dir + "/" + e.Name())
			if err != nil {
				return nil, "", err
			}
			if err := add(dir+"/"+e.Name(), data); err != nil {
				return nil, "", err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, hex.EncodeToString(hash.Sum(nil)), nil
}

func goArch(engineArch string) (string, error) {
	switch engineArch {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("node agent: no build for the engine's architecture %q", engineArch)
}

func drainBuild(stream io.Reader) error {
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
		if strings.TrimSpace(event.Stream) != "" {
			last = strings.TrimSpace(event.Stream)
		}
		if msg := event.Error + event.ErrorDetail.Message; msg != "" {
			return fmt.Errorf("%s (last output: %s)", msg, last)
		}
	}
}
