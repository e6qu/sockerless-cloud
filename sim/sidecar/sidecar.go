// Package sidecar runs the container that stands in for a cloud network's
// fixed addresses from inside the network: the resolver the cloud's DHCP hands
// out and the metadata endpoints, both forwarded to the simulator. One sidecar
// serves one engine network at the address the cloud reserves for them, so a
// workload on the network reaches them the way it reaches them in the cloud,
// and the simulator attributes what arrives through a sidecar to its network.
package sidecar

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
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The binaries are built by build.sh with the pinned toolchain; the gate
// scripts/check-sidecar-build.sh keeps them equal to a rebuild of cmd/sidecar.
//
//go:embed bin/sidecar-linux-amd64 bin/sidecar-linux-arm64
var binaries embed.FS

// Spec places one sidecar.
type Spec struct {
	// Network is the engine network the sidecar serves.
	Network string
	// Address is the sidecar's fixed address inside the network's range: the
	// address the cloud reserves for its resolver and metadata service.
	Address netip.Addr
	// Name is the container's name.
	Name string
	// DNSUpstream is the host:port of the simulator's listener for this
	// network (dnsserver.Server.ListenNetwork), as the sidecar reaches it.
	DNSUpstream string
	// HTTPUpstream is the simulator's HTTP origin as the sidecar reaches it.
	HTTPUpstream string
	// Token is the credential the simulator minted for this sidecar; the
	// simulator trusts the querier address a request carries only with it.
	Token string
	// ExtraHosts are the host entries the sidecar needs to reach the upstreams
	// by name (workloadhost.ExtraHosts on an engine that runs in a machine).
	ExtraHosts []string
	Labels     map[string]string
}

// Start runs a sidecar and returns its handle; the container goes with the
// handle's Cancel.
func Start(ctx context.Context, spec Spec, sink sim.LogSink) (*sim.ContainerHandle, error) {
	if spec.Network == "" || !spec.Address.IsValid() || spec.DNSUpstream == "" || spec.HTTPUpstream == "" || spec.Token == "" {
		return nil, errors.New("sidecar: network, address, upstreams and token are required")
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting a network sidecar"); err != nil {
		return nil, err
	}
	image, platform, err := ensureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"sockerless-sim-sidecar": spec.Network}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	return sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         spec.Name,
		Network:      spec.Network,
		IPv4Address:  spec.Address.String(),
		ExtraHosts:   spec.ExtraHosts,
		Labels:       labels,
		Env: map[string]string{
			"SIDECAR_DNS_UPSTREAM":  spec.DNSUpstream,
			"SIDECAR_HTTP_UPSTREAM": spec.HTTPUpstream,
			"SIDECAR_TOKEN":         spec.Token,
		},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"NET_BIND_SERVICE"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
			ReadonlyRootfs:   true,
		},
	}, sink)
}

// WaitReady returns once the sidecar answers DNS at its address from the
// simulator's side, or ctx ends. On a host that routes no container address
// it returns at once: nothing there can probe the sidecar but a workload.
func WaitReady(ctx context.Context, address netip.Addr) error {
	probe := net.JoinHostPort(address.String(), "53")
	for {
		conn, err := net.DialTimeout("tcp4", probe, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) && !opErr.Timeout() && !isRefused(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sidecar at %s did not answer: %w", address, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func isRefused(err error) bool {
	var sysErr interface{ Error() string }
	return errors.As(err, &sysErr) && bytes.Contains([]byte(err.Error()), []byte("refused"))
}

var imageMu sync.Mutex

// ensureImage builds the sidecar image for the engine's own architecture from
// nothing but the embedded binary, once per engine and source version, and
// returns the image and its platform.
func ensureImage(ctx context.Context, cli *client.Client) (string, string, error) {
	imageMu.Lock()
	defer imageMu.Unlock()
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", "", fmt.Errorf("sidecar: engine info: %w", err)
	}
	arch, err := goArch(info.Info.Architecture)
	if err != nil {
		return "", "", err
	}
	binary, err := binaries.ReadFile("bin/sidecar-linux-" + arch)
	if err != nil {
		return "", "", fmt.Errorf("sidecar: no binary for %s: %w", arch, err)
	}
	sum := sha256.Sum256(binary)
	image := "sockerless-sidecar:" + hex.EncodeToString(sum[:])[:16]
	platform := "linux/" + arch
	if _, err := cli.ImageInspect(ctx, image); err == nil {
		return image, platform, nil
	}
	var context bytes.Buffer
	archive := tar.NewWriter(&context)
	dockerfile := "FROM scratch\nCOPY sidecar /sidecar\nENTRYPOINT [\"/sidecar\"]\n"
	for _, f := range []struct {
		name string
		mode int64
		data []byte
	}{{"Dockerfile", 0o644, []byte(dockerfile)}, {"sidecar", 0o755, binary}} {
		if err := archive.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data))}); err != nil {
			return "", "", err
		}
		if _, err := archive.Write(f.data); err != nil {
			return "", "", err
		}
	}
	if err := archive.Close(); err != nil {
		return "", "", err
	}
	result, err := cli.ImageBuild(ctx, &context, client.ImageBuildOptions{
		Tags:       []string{image},
		Dockerfile: "Dockerfile",
		Remove:     true,
		Platforms:  []ocispec.Platform{{OS: "linux", Architecture: arch}},
	})
	if err != nil {
		return "", "", fmt.Errorf("sidecar: build %s: %w", image, err)
	}
	defer func() { _ = result.Body.Close() }()
	if err := drainBuild(result.Body); err != nil {
		return "", "", fmt.Errorf("sidecar: build %s: %w", image, err)
	}
	return image, platform, nil
}

func goArch(engineArch string) (string, error) {
	switch engineArch {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("sidecar: no binary for the engine's architecture %q", engineArch)
}

func drainBuild(stream io.Reader) error {
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
			return fmt.Errorf("malformed build stream: %w", err)
		}
		if msg := event.Error + event.ErrorDetail.Message; msg != "" {
			return errors.New(msg)
		}
	}
}

// DNSUpstream renders the simulator's per-network listener as the sidecar
// reaches it.
func DNSUpstream(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
