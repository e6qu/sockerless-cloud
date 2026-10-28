// Package sidecar runs the container that stands in for a cloud network's
// fixed addresses from inside the network: the resolver the cloud's DHCP hands
// out and the metadata endpoints, both forwarded to the simulator. One sidecar
// serves one engine network at the address the cloud reserves for them, so a
// workload on the network reaches them the way it reaches them in the cloud,
// and the simulator attributes what arrives through a sidecar to its network.
package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// goImage compiles the sidecar on the engine.
const goImage = "mirror.gcr.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb"

const dockerfile = `FROM ` + goImage + ` AS build
ARG GOPROXY
COPY ` + sim.EngineBuildTrustFile + ` /tmp/
RUN cat /tmp/` + sim.EngineBuildTrustFile + ` >> /etc/ssl/certs/ca-certificates.crt
WORKDIR /src
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/sidecar ./sidecar/cmd/sidecar

FROM scratch
COPY --from=build /out/sidecar /sidecar
ENTRYPOINT ["/sidecar"]
`

// build compiles the sidecar from the sources the simulator carries.
var build = sim.EngineBuild{
	Name:  "sockerless-sidecar",
	Bases: []string{goImage},
	Context: func(string) (map[string][]byte, error) {
		files := map[string][]byte{"Dockerfile": []byte(dockerfile)}
		err := fs.WalkDir(sim.SidecarSource, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(name, "_test.go") {
				return err
			}
			data, err := sim.SidecarSource.ReadFile(name)
			files[name] = data
			return err
		})
		return files, err
	},
}

// Spec places one sidecar.
type Spec struct {
	// Network is the engine network the sidecar serves.
	Network string
	// Address is the sidecar's fixed address inside the network's range: the
	// address the cloud reserves for its resolver and metadata service.
	Address netip.Addr
	Name    string
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
	image, platform, err := EnsureImage(ctx, cli)
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

// EnsureImage builds the sidecar image for the engine's own architecture,
// once per engine and source version, and returns the image and its platform.
func EnsureImage(ctx context.Context, cli *client.Client) (string, string, error) {
	image, platform, err := sim.EnsureEngineBuild(ctx, cli, build)
	if err != nil {
		return "", "", fmt.Errorf("sidecar: %w", err)
	}
	return image, platform, nil
}

// DNSUpstream renders the simulator's per-network listener as the sidecar
// reaches it.
func DNSUpstream(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// ShareVolume is one engine volume a share container serves, under Export,
// the name a guest mounts it by.
type ShareVolume struct {
	Volume   string
	Export   string
	ReadOnly bool
}

// SharesSpec places one share container: the sidecar program serving engine
// volumes over NFSv4 from inside another container's network namespace, the
// node agent's, where every guest reaches it at the agent's address.
type SharesSpec struct {
	Name string
	// NamespaceOf is the container whose network namespace the share
	// container joins.
	NamespaceOf string
	// Port is the TCP port the shares are served on in that namespace.
	Port    int
	Volumes []ShareVolume
	Labels  map[string]string
}

// StartShares runs a share container and returns its handle once it serves,
// or when ctx ends first; the container goes with the handle's Cancel. The
// server reads and writes the volumes' files as their owners, which the
// capabilities it keeps allow and nothing else.
func StartShares(ctx context.Context, spec SharesSpec, sink sim.LogSink) (*sim.ContainerHandle, error) {
	if spec.NamespaceOf == "" || spec.Port == 0 || len(spec.Volumes) == 0 {
		return nil, errors.New("sidecar: a share container needs a namespace, a port and volumes")
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting a share container"); err != nil {
		return nil, err
	}
	image, platform, err := EnsureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	binds := make([]string, 0, len(spec.Volumes))
	for _, v := range spec.Volumes {
		bind := v.Volume + ":/exports/" + v.Export
		if v.ReadOnly {
			bind += ":ro"
		}
		binds = append(binds, bind)
	}
	serving := make(chan struct{})
	var once sync.Once
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         spec.Name,
		NetworkMode:  "container:" + spec.NamespaceOf,
		Args:         []string{"shares"},
		Binds:        binds,
		Labels:       spec.Labels,
		Env:          map[string]string{"SIDECAR_SHARES_LISTEN": ":" + strconv.Itoa(spec.Port)},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
			ReadonlyRootfs:   true,
		},
	}, sim.FuncSink(func(line sim.LogLine) {
		// The caller's sink holds the line before the start returns.
		sink.WriteLog(line)
		if strings.HasPrefix(line.Text, "sidecar: shares ") {
			once.Do(func() { close(serving) })
		}
	}))
	if err != nil {
		return nil, err
	}
	select {
	case <-serving:
		return handle, nil
	case <-handle.Exited():
		handle.Cancel()
		return nil, fmt.Errorf("sidecar: the share container %s ended before it served", spec.Name)
	case <-ctx.Done():
		handle.Cancel()
		return nil, ctx.Err()
	}
}

// Forward is a TCP listener on a workload's own loopback, Listen, that relays
// each connection to Upstream, an address on the workload's network.
type Forward struct {
	Listen   string
	Upstream string
}

// forwarderLabel marks a forwarder container; its value is the name of the
// workload it serves.
const forwarderLabel = "sockerless-sim-forwarder"

// ForwarderSpec places one forwarder: the sidecar program as the owner of a
// workload's network namespace, placed with the workload's network settings
// before the workload joins it, so the forwards listen before the workload's
// first process runs.
type ForwarderSpec struct {
	// Namespace is the network side of the workload's configuration: its
	// network, address, interface, aliases, hosts, resolvers, hostname and
	// published ports.
	Namespace sim.ContainerConfig
	Forwards  []Forward
	// Workload names the workload the forwarder serves, for its label.
	Workload string
}

// StartForwarder runs a forwarder and returns its handle once its forwards
// listen, or an error when ctx ends first; the container goes with the
// handle's Cancel. It holds no capability: the listeners are unprivileged
// ports.
func StartForwarder(ctx context.Context, spec ForwarderSpec, sink sim.LogSink) (*sim.ContainerHandle, error) {
	if len(spec.Forwards) == 0 {
		return nil, errors.New("sidecar: a forwarder needs forwards")
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting a forwarder"); err != nil {
		return nil, err
	}
	image, platform, err := EnsureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	args := []string{"forward"}
	for _, f := range spec.Forwards {
		if strings.ContainsRune(f.Listen, '=') || strings.ContainsRune(f.Upstream, '=') {
			return nil, fmt.Errorf("sidecar: forward %s to %s names an address with '='", f.Listen, f.Upstream)
		}
		args = append(args, f.Listen+"="+f.Upstream)
	}
	cfg := spec.Namespace
	cfg.Image = image
	cfg.Architecture = platform
	cfg.Command = nil
	cfg.Args = args
	cfg.Labels = map[string]string{forwarderLabel: spec.Workload}
	cfg.Sandbox = sim.SandboxProfile{
		CapDrop:          []string{"ALL"},
		NoNewPrivileges:  true,
		DenyDockerSocket: true,
		DenyHostNetwork:  true,
		ReadonlyRootfs:   true,
	}
	listening := make(chan struct{})
	var once sync.Once
	handle, err := sim.StartContainerSyncContext(ctx, cfg, sim.FuncSink(func(line sim.LogLine) {
		sink.WriteLog(line)
		if strings.HasPrefix(line.Text, "sidecar: forward ") {
			once.Do(func() { close(listening) })
		}
	}))
	if err != nil {
		return nil, err
	}
	select {
	case <-listening:
		return handle, nil
	case <-handle.Exited():
		handle.Cancel()
		return nil, fmt.Errorf("sidecar: the forwarder %s ended before it listened", cfg.Name)
	case <-ctx.Done():
		handle.Cancel()
		return nil, ctx.Err()
	}
}
