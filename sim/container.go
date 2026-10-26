package sim

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// parsePlatform splits an "os/arch" or "os/arch/variant" string into an
// ocispec.Platform. It refuses empty or malformed input: every caller names
// its platform, and none falls back to the host's.
func parsePlatform(s string) (*ocispec.Platform, error) {
	if s == "" {
		return nil, fmt.Errorf("ContainerConfig.Architecture is required (e.g. \"linux/arm64\")")
	}
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		return &ocispec.Platform{OS: parts[0], Architecture: parts[1]}, nil
	case 3:
		return &ocispec.Platform{OS: parts[0], Architecture: parts[1], Variant: parts[2]}, nil
	default:
		return nil, fmt.Errorf("ContainerConfig.Architecture %q must be \"os/arch\" or \"os/arch/variant\"", s)
	}
}

// NetworkAttachment is a further network a workload joins at an address.
type NetworkAttachment struct {
	Network     string
	IPv4Address string
}

// NoGateway is the Gateway of a workload with no default route: one whose
// subnet's routing names no way out.
const NoGateway = "none"

type ContainerConfig struct {
	Image        string // container image (e.g., "alpine:latest")
	Architecture string // OS/arch (e.g. "linux/arm64") from the workload's spec, never the host's
	// RegistryAuth is the credential the engine presents to Image's registry
	// when it pulls — the engine API's X-Registry-Auth value, which
	// RegistryCredential renders. Empty pulls anonymously.
	RegistryAuth string
	Command      []string          // entrypoint override (empty = use image default)
	Args         []string          // command/args (empty = use image default)
	Env          map[string]string // environment variables
	Timeout      time.Duration     // max execution time (0 = no limit)
	Labels       map[string]string // container labels for tracking
	Network      string            // Docker network to join (optional)
	// NetworkAliases are the names Docker's embedded DNS resolves to this
	// container on Network.
	NetworkAliases []string
	// IPv4Address is the fixed address the container takes on Network, inside
	// the network's range: the address the cloud API reports for the
	// workload's interface. Empty lets the engine pick one.
	IPv4Address string
	// MACAddress is the hardware address of the workload's interface on
	// Network, as the cloud assigns it to the interface. Empty lets the
	// engine pick one.
	MACAddress string
	// ExtraNetworks are further engine networks the workload has an
	// interface on, each at a fixed address in the network's range, with no
	// route out through it: a site integrated into more than one virtual
	// network.
	ExtraNetworks []NetworkAttachment
	// Gateway is where the workload's default route goes: empty for the
	// network's own gateway, an address on the network for a next hop the
	// cloud's routing names (a NAT gateway), or NoGateway for no route out.
	// A backend that cannot route a workload leaves it on the network's
	// gateway.
	Gateway string
	// Hostname is the name the container sees as its own; empty leaves the
	// engine's default.
	Hostname string
	// Devices are host device nodes the engine hands the container, as
	// "/dev/kvm" or "/dev/net/tun"; only a platform container the simulator
	// runs on its own behalf (a node agent) names any.
	Devices []string
	// KeepOnExit leaves the container in place once it exits, with its
	// filesystem, for RestartContainer, CommitContainer or RemoveContainer: a
	// virtual machine instance's root disk outlives a stop.
	KeepOnExit bool
	// Seed files are written into the container's filesystem after it is
	// created and before it starts: what a cloud hands an instance through
	// its platform rather than its image or its environment — user data, the
	// instance's own init.
	Seed []SeedFile
	// Ingress is what the workload may receive on its network: stages a
	// packet passes in turn, each a rule list whose first match decides,
	// where an accept hands it to the next stage and a stage nothing matches
	// drops it; no stages admit everything. A cloud compiles its security
	// groups, firewall rules or network security groups into it. The microVM
	// backend enforces it at the guest's tap; the container backend cannot
	// filter between two containers on one network and runs the workload
	// unfiltered.
	Ingress     [][]netrules.PacketRule
	NetworkMode string // Docker network mode (e.g. "container:<id>" for shared netns)
	// PidMode joins another container's pid namespace ("container:<id>"):
	// a platform helper reading a workload's processes. Empty is the
	// container's own namespace.
	PidMode    string
	Name       string   // container name (optional, auto-generated if empty)
	Tty        bool     // allocate a pseudo-TTY
	OpenStdin  bool     // keep stdin open
	Binds      []string // bind mounts (e.g., "vol:/path")
	ExtraHosts []string // --add-host entries (e.g., "host.docker.internal:host-gateway")
	// DNS are the resolvers written into the container's /etc/resolv.conf. A
	// workload in its own network namespace cannot reach the embedded resolver
	// Docker would otherwise configure, so the namespace's own resolver is named
	// here; empty leaves Docker's default in place.
	DNS        []string
	WorkingDir string // working directory inside the container (optional)

	// PublishPorts lists container TCP ports the engine publishes on
	// 127.0.0.1, each on a host port the engine allocates when it starts the
	// container; read it back with ContainerHandle.PublishedPort. Host-addressed
	// data planes use it to reach a workload's listener cross-platform:
	// container IPs are only routable from the host on Linux, while a loopback
	// port binding works on Docker Desktop (macOS/Windows) too. The engine
	// allocates the port because a port the caller picked is free only until
	// something else binds it.
	PublishPorts []int

	// Sandbox restricts the workload as its cloud platform does; each
	// cloud-product handler picks the matching profile (SandboxLambda,
	// SandboxFargate, and so on). The zero value enforces nothing.
	Sandbox SandboxProfile

	// MemoryBytes is the hard memory limit (cgroup memory.max) in bytes; zero
	// is unbounded. Cloud handlers translate the product's advertised sizing
	// (an Amazon ECS task's or container's memory) here, so the cgroup matches
	// what the metadata advertises.
	MemoryBytes int64

	// NanoCPU is the CPU limit (cgroup cpu.max) in units of 1e-9 CPUs — e.g.
	// 1_000_000_000 == 1 vCPU. Zero = unbounded. Cloud handlers translate the
	// product's advertised CPU sizing here.
	NanoCPU int64

	// CancelGracePeriod is how long the workload gets between the SIGTERM a
	// cancellation sends and the SIGKILL that follows it — the cloud's own
	// documented stop grace (Cloud Run's ten seconds, an Amazon ECS task's
	// stopTimeout). Zero kills the workload outright, which is what a cloud
	// does to a command it has cancelled rather than a workload it is stopping.
	CancelGracePeriod time.Duration

	// TrackMemoryPeak makes the handle observe the container's memory usage
	// through the engine's stats endpoint for as long as it runs, so a cloud
	// product that reports what a workload consumed — the AWS Lambda
	// invocation REPORT's Max Memory Used — has a measurement to report.
	// Read it back with ContainerHandle.MemoryPeakBytes.
	TrackMemoryPeak bool
}

// ContainerHandle manages a running container.
type ContainerHandle struct {
	ContainerID string
	cancel      context.CancelFunc
	// exited closes once the container has exited and been removed, after
	// result is set.
	exited chan struct{}
	result ProcessResult
	cli    *client.Client
	memory *memoryPeakObserver
	// startedAt is the moment the engine was asked to start the container,
	// which its process can only follow.
	startedAt time.Time
}

// Wait blocks until the container exits, and returns how it exited to every
// caller.
func (h *ContainerHandle) Wait() ProcessResult {
	<-h.exited
	return h.result
}

// Exited closes once the container has exited; Wait then returns at once.
func (h *ContainerHandle) Exited() <-chan struct{} { return h.exited }

// Cancel stops and removes the container.
func (h *ContainerHandle) Cancel() { h.cancel() }

// PublishedPort returns the 127.0.0.1 host port the engine bound for the
// container's containerPort/tcp, listed in ContainerConfig.PublishPorts.
func (h *ContainerHandle) PublishedPort(ctx context.Context, containerPort int) (int, error) {
	return PublishedHostPort(ctx, h.ContainerID, containerPort)
}

// maxNamespaceHops bounds how many network namespace joins a read follows.
const maxNamespaceHops = 8

// ErrNotRunning is wrapped by a read of a workload's published port when the
// workload has stopped, or is gone, and so publishes none.
var ErrNotRunning = errors.New("the workload is not running")

// PublishedHostPort returns the host port the engine bound for a running
// container's containerPort/tcp. The engine allocates the port when it starts
// the container and releases it when the container stops, so a resumed
// container holds a new one: read it after every start. A container that
// joined another's network namespace listens on the ports the engine bound
// for that one.
func PublishedHostPort(ctx context.Context, containerID string, containerPort int) (int, error) {
	if err := RequireContainerRuntime("reading a container's published port"); err != nil {
		return 0, err
	}
	var info container.InspectResponse
	for id, hops := containerID, 0; ; hops++ {
		inspected, err := dockerClient.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				return 0, fmt.Errorf("container %s: %w", id, ErrNotRunning)
			}
			return 0, fmt.Errorf("inspect container %s: %w", id, err)
		}
		info = inspected.Container
		if info.State == nil || !info.State.Running {
			return 0, fmt.Errorf("container %s publishes no port: %w", id, ErrNotRunning)
		}
		if info.HostConfig == nil || !info.HostConfig.NetworkMode.IsContainer() {
			break
		}
		if hops == maxNamespaceHops {
			return 0, fmt.Errorf("container %s joins a chain of namespaces too long to follow", containerID)
		}
		id = info.HostConfig.NetworkMode.ConnectedContainer()
	}
	port, err := network.ParsePort(strconv.Itoa(containerPort) + "/tcp")
	if err != nil {
		return 0, fmt.Errorf("container port %d: %w", containerPort, err)
	}
	if info.NetworkSettings != nil {
		for _, binding := range info.NetworkSettings.Ports[port] {
			if binding.HostPort == "" {
				continue
			}
			hostPort, err := strconv.Atoi(binding.HostPort)
			if err != nil {
				return 0, fmt.Errorf("container %s publishes %s on host port %q: %w", containerID, port, binding.HostPort, err)
			}
			return hostPort, nil
		}
	}
	return 0, fmt.Errorf("container %s publishes no host port for %s", containerID, port)
}

// MemoryPeakBytes reports the highest memory usage the container engine
// accounted to this container up to now. A container still running is read
// once more before the figure is returned, so whatever it holds at the moment
// of the call counts. It is zero when the engine reported none — a container
// started without ContainerConfig.TrackMemoryPeak, or one the engine stopped
// accounting for before any reading — and a caller that reports the figure
// must then report nothing rather than a substitute.
func (h *ContainerHandle) MemoryPeakBytes() uint64 {
	if h.memory == nil {
		return 0
	}
	return h.memory.read()
}

// StartedAt is the moment the engine was asked to start the container: its
// process starts after it. An adopted container reports the start the engine
// recorded, and none when the engine no longer held it.
func (h *ContainerHandle) StartedAt() time.Time { return h.startedAt }

// dockerClient is the shared engine client InitDocker sets.
var (
	dockerClient     *client.Client
	dockerClientOnce sync.Once
	dockerClientErr  error
)

// engineUnavailableAdvice is what an operator can do about a container engine
// the simulator could not reach.
const engineUnavailableAdvice = "simulators require Docker or Podman for workload execution; " +
	"install Docker/Podman, or set SIM_RUNTIME=process only for explicit API-only runs that do not execute workloads"

// Container-engine readiness probe timings. One short probe is not a
// readiness test: a busy engine — a Podman virtual machine serving several
// test suites at once, or one that has only just started — takes seconds to
// answer its first ping while being perfectly available, and reading that
// single timeout as proof of absence refuses to start with a message that is
// not true. The probe retries for a budget, and only the exhausted budget is
// an answer about the engine.
const (
	dockerPingAttemptTimeout = 5 * time.Second
	dockerPingBudget         = 30 * time.Second
	dockerPingRetryDelay     = 500 * time.Millisecond
)

// pingContainerEngine reports whether the container engine answers, retrying
// until the readiness budget is spent. It returns the last failure so the
// operator sees why the engine never answered.
func pingContainerEngine(cli *client.Client) error {
	deadline := time.Now().Add(dockerPingBudget)
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), dockerPingAttemptTimeout)
		_, err := cli.Ping(ctx, client.PingOptions{})
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no answer after %d attempts over %s: %w", attempt, dockerPingBudget, err)
		}
		time.Sleep(dockerPingRetryDelay)
	}
}

// InitDocker initializes the shared Docker client and verifies connectivity.
// Startup calls it for every runtime mode that executes workloads.
//
// It returns an error rather than exiting, so startup decides to refuse to
// start and can say what it was configuring: a simulator that cannot reach an
// engine must not answer its health check while every workload it runs fails
// at first use.
func InitDocker(provider string, preserveWorkloads bool, stateDir string) (*client.Client, error) {
	configureSimulatorIdentity(provider, stateDir)
	dockerClientOnce.Do(func() {
		dockerClient, dockerClientErr = client.New(client.FromEnv)
		if dockerClientErr != nil {
			return
		}
		if dockerClientErr = pingContainerEngine(dockerClient); dockerClientErr != nil {
			// A client that could not answer for the engine is not a client the
			// rest of the process may find and use.
			dockerClient = nil
		}
	})
	if dockerClientErr != nil {
		return nil, fmt.Errorf("container engine not available: %w (%s)", dockerClientErr, engineUnavailableAdvice)
	}
	if dockerClient == nil {
		return nil, fmt.Errorf("container engine not available: no engine client was built (%s)", engineUnavailableAdvice)
	}
	if !preserveWorkloads {
		if err := startContainerReaper(provider); err != nil {
			return nil, fmt.Errorf("start container reaper: %w", err)
		}
		sweepOrphanedWorkloads(dockerClient)
	}
	return dockerClient, nil
}

// DockerClient returns the shared Docker client InitDocker set.
func DockerClient() *client.Client {
	return dockerClient
}

// RequireContainerRuntime reports why an operation that runs a container cannot
// run in this process. Startup refuses to serve when a workload-executing mode
// cannot reach an engine, so a missing client here means the simulator was
// started API-only, not that an engine went away.
func RequireContainerRuntime(operation string) error {
	if dockerClient != nil {
		return nil
	}
	return fmt.Errorf("%s requires a container runtime: this simulator was started API-only (SIM_RUNTIME=%s) and holds no container engine client",
		operation, RuntimeModeAPIOnly)
}

// managedContainers tracks containers created by this simulator instance for cleanup.
var managedContainers sync.Map // containerID -> true

// CleanupContainers stops and removes all simulator-managed containers, then
// the run's networks and volumes. Shutdown calls it.
func CleanupContainers() {
	if dockerClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	managedContainers.Range(func(key, _ any) bool {
		id, ok := key.(string)
		if !ok {
			return true
		}
		timeout := 5
		_, _ = dockerClient.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
		_, _ = dockerClient.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		return true
	})

	runFilter := client.Filters{}.Add("label", "sockerless-sim-run="+simulatorRunID)
	nets, err := dockerClient.NetworkList(ctx, client.NetworkListOptions{Filters: runFilter})
	if err == nil {
		for _, n := range nets.Items {
			_, _ = dockerClient.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
		}
	}
	if simulatorRunID == "" {
		return
	}
	if err := removeRunVolumes(ctx, dockerClient, runFilter); err != nil {
		fmt.Fprintf(os.Stderr, "remove the run's volumes: %v\n", err)
	}
}

// StartContainerSyncContext pulls the image when needed, creates and starts a
// container, and returns its handle once it has started. Stdout and stderr
// stream to sink; handle.Wait blocks until exit.
//
// ctx bounds the start alone: once it is done the pull and the create stop and
// the call fails, while a container that already started runs until it exits
// or handle.Cancel stops it.
func StartContainerSyncContext(ctx context.Context, cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("starting a container"); err != nil {
		return nil, err
	}

	containerID, startedAt, err := createAndStartContainer(ctx, cli, cfg)
	if err != nil {
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	handle := &ContainerHandle{ContainerID: containerID, cancel: cancel, exited: make(chan struct{}), cli: cli, startedAt: startedAt}

	managedContainers.Store(containerID, true)

	var memory *memoryPeakObserver
	if cfg.TrackMemoryPeak {
		memory = newMemoryPeakObserver(cli, containerID)
		go memory.observe(lifetime)
	}
	handle.memory = memory

	go func() {
		result := waitAndCaptureLogs(lifetime, cli, containerID, cfg, sink)
		// The observation ends with the container, before it is removed: the
		// engine accounts for a container only while it runs.
		if memory != nil {
			memory.stop()
		}
		managedContainers.Delete(containerID)
		if !cfg.KeepOnExit {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer rmCancel()
			_, _ = cli.ContainerRemove(rmCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		handle.result = result
		close(handle.exited)
	}()
	return handle, nil
}

// ExistingContainer describes a workload container that outlived a persistent
// simulator control-plane process. Service slices use the same cloud-resource
// labels they supplied at creation to reclaim ownership after restart.
type ExistingContainer struct {
	ID             string
	Running        bool
	PublishedPorts map[int]int
	Labels         map[string]string
}

// FindExistingContainers returns every container carrying all requested labels,
// including exited containers whose terminal result has not yet been reconciled
// into the durable cloud resource.
func FindExistingContainers(labels map[string]string) ([]ExistingContainer, error) {
	if err := RequireContainerRuntime("listing existing containers"); err != nil {
		return nil, err
	}
	labelFilters := client.Filters{}
	if simulatorStateID != "" {
		labelFilters.Add("label", "sockerless-sim-state="+simulatorStateID)
	}
	for key, value := range labels {
		if value == "" {
			labelFilters.Add("label", key)
		} else {
			labelFilters.Add("label", key+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	summaries, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilters,
	})
	if err != nil {
		return nil, err
	}
	found := make([]ExistingContainer, 0, len(summaries.Items))
	for _, summary := range summaries.Items {
		inspected, err := dockerClient.ContainerInspect(ctx, summary.ID, client.ContainerInspectOptions{})
		if containerNotFoundError(err) {
			// The engine removed it after the listing.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect container %s: %w", summary.ID, err)
		}
		inspection := inspected.Container
		entry := ExistingContainer{
			ID:             summary.ID,
			Running:        inspection.State != nil && inspection.State.Running,
			PublishedPorts: map[int]int{},
			Labels:         map[string]string{},
		}
		if inspection.Config != nil {
			for key, value := range inspection.Config.Labels {
				entry.Labels[key] = value
			}
		}
		ports := inspection.NetworkSettings
		if inspection.HostConfig != nil && inspection.HostConfig.NetworkMode.IsContainer() {
			if owner, err := dockerClient.ContainerInspect(ctx, inspection.HostConfig.NetworkMode.ConnectedContainer(), client.ContainerInspectOptions{}); err == nil {
				ports = owner.Container.NetworkSettings
			} else if !cerrdefs.IsNotFound(err) {
				return nil, err
			}
		}
		if ports != nil {
			for containerPort, bindings := range ports.Ports {
				if len(bindings) == 0 {
					continue
				}
				publicPort, err := strconv.Atoi(bindings[0].HostPort)
				if err == nil {
					entry.PublishedPorts[int(containerPort.Num())] = publicPort
				}
			}
		}
		found = append(found, entry)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, nil
}

// HasPersistentWorkloadIdentity reports whether workload ownership is scoped
// to a durable simulator state directory.
func HasPersistentWorkloadIdentity() bool {
	return simulatorStateID != ""
}

// RemoveExistingContainer stops and removes a workload whose owning
// control-plane process can no longer drive it.
func RemoveExistingContainer(containerID string) error {
	if err := RequireContainerRuntime("removing a container"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	timeout := 5
	_, _ = dockerClient.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: &timeout})
	_, err := dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}

// StartExistingContainer resumes an exited persistent workload container
// without replacing it, preserving its filesystem, mounts, identity, and port
// bindings.
func StartExistingContainer(containerID string) error {
	if err := RequireContainerRuntime("starting an existing container"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{})
	return err
}

// AdoptContainer attaches lifecycle observation to a container created by an
// earlier persistent simulator process. It never restarts the workload; callers
// decide whether an exited cloud workload should remain terminal or resume.
// A container the earlier process left frozen by a volume capture it did not
// finish is thawed, since no capture of this process holds it.
func AdoptContainer(containerID string, cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	if err := RequireContainerRuntime("adopting a running container"); err != nil {
		return nil, err
	}
	if err := thawAbandonedFreeze(containerID); err != nil {
		return nil, err
	}
	startedAt, err := containerStartedAt(containerID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	handle := &ContainerHandle{ContainerID: containerID, cancel: cancel, exited: make(chan struct{}), cli: dockerClient, startedAt: startedAt}
	managedContainers.Store(containerID, true)
	go func() {
		result := waitAndCaptureLogs(ctx, dockerClient, containerID, cfg, sink)
		managedContainers.Delete(containerID)
		if !cfg.KeepOnExit {
			removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer removeCancel()
			_, _ = dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		handle.result = result
		close(handle.exited)
	}()
	return handle, nil
}

// containerStartedAt is when the engine last started the container, as its
// state records it; a container the engine no longer holds has no start to
// report, and the handle adopting it reports its end at once.
func containerStartedAt(containerID string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inspected, err := dockerClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if containerNotFoundError(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	if inspected.Container.State == nil {
		return time.Time{}, fmt.Errorf("container %s reports no state", containerID)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, inspected.Container.State.StartedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("container %s start time %q: %w", containerID, inspected.Container.State.StartedAt, err)
	}
	return startedAt, nil
}

// CommitContainer captures a kept container's filesystem as an image under
// reference ("name:tag") and returns the image ID: a machine image taken
// from a stopped instance.
func CommitContainer(ctx context.Context, containerID, reference string) (string, error) {
	if err := RequireContainerRuntime("committing a container"); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := dockerClient.ContainerCommit(ctx, containerID, client.ContainerCommitOptions{Reference: reference, NoPause: false})
	if err != nil {
		return "", fmt.Errorf("commit container %s: %w", containerID, err)
	}
	return result.ID, nil
}

// RemoveContainer removes a container the simulator kept, running or not,
// with the anonymous volumes its image declared.
func RemoveContainer(containerID string) error {
	if err := RequireContainerRuntime("removing a container"); err != nil {
		return err
	}
	managedContainers.Delete(containerID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if cerrdefs.IsConflict(err) && strings.Contains(err.Error(), "already in progress") {
		// Another remover holds the container; its removal is the one to wait on.
		return WaitContainerRemoved(containerID, 30*time.Second)
	}
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container %s: %w", containerID, err)
	}
	return nil
}

// StopContainer stops a running container by ID, sending SIGTERM and then
// SIGKILL once the grace the cloud grants a stopping workload has elapsed.
func StopContainer(containerID string, grace time.Duration) {
	if dockerClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second+grace)
	defer cancel()
	timeout := int(grace / time.Second)
	_, _ = dockerClient.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: &timeout})
}

// WaitContainerRemoved waits until the runtime no longer owns containerID.
// Lifecycle callers use this after requesting a stop when the cloud resource
// must not report its terminal state while its network endpoint or mounts are
// still present.
func WaitContainerRemoved(containerID string, timeout time.Duration) error {
	if err := RequireContainerRuntime("waiting for a container to be removed"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	removed := dockerClient.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	select {
	case <-removed.Result:
		return nil
	case err := <-removed.Error:
		if containerNotFoundError(err) {
			return nil
		}
		if ctx.Err() == nil {
			return fmt.Errorf("wait for container %s to be removed: %w", containerID, err)
		}
	}
	removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer removeCancel()
	_, removeErr := dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if removeErr == nil || containerNotFoundError(removeErr) {
		return nil
	}
	return fmt.Errorf("container %s was not removed within %s and forced removal failed: %w", containerID, timeout, removeErr)
}

// containerNotFoundError accepts Docker's typed 404 and Podman's
// Docker-compatible answer, which reports the missing container without
// Docker's NotFound classification.
func containerNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if cerrdefs.IsNotFound(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no container with id") &&
		strings.Contains(message, "no such container")
}

// RemoveVolume removes one explicitly named simulator-managed Docker volume.
// Callers own the lifecycle decision; this helper never enumerates or prunes
// unrelated volumes.
func RemoveVolume(name string) error {
	if err := RequireContainerRuntime("removing a volume"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	return err
}

// ensureBindVolumes creates each named volume a bind mounts that the engine
// does not hold yet, carrying the run's labels, so the reaper that collects a
// run's containers and networks collects its volumes too. An engine creates a
// missing named volume itself at container create, unlabelled, and such a
// volume outlives the simulator that made it.
func ensureBindVolumes(ctx context.Context, cli *client.Client, binds []string) error {
	for _, bind := range binds {
		source, _, found := strings.Cut(bind, ":")
		if !found || !namedVolumeSource(source) {
			continue
		}
		if _, err := cli.VolumeInspect(ctx, source, client.VolumeInspectOptions{}); err == nil {
			continue
		} else if !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect volume %s: %w", source, err)
		}
		if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: source, Labels: simulatorLabels(nil)}); err != nil {
			return fmt.Errorf("create volume %s: %w", source, err)
		}
	}
	return nil
}

// namedVolumeSource reports whether a bind's source names a volume rather
// than a host path.
func namedVolumeSource(source string) bool {
	return source != "" && !strings.ContainsAny(source, `/\`) && !strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "~")
}

// RemoveVolumesNamed removes every simulator-managed volume whose name
// starts with prefix: a workload's own volumes once it is gone.
func RemoveVolumesNamed(prefix string) error {
	if err := RequireContainerRuntime("removing volumes"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filters := client.Filters{}.Add("label", "sockerless-sim").Add("name", prefix)
	volumes, err := dockerClient.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	var errs []error
	for _, v := range volumes.Items {
		if !strings.HasPrefix(v.Name, prefix) {
			continue
		}
		if _, err := dockerClient.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", v.Name, err))
		}
	}
	return errors.Join(errs...)
}

// removeRunVolumes removes every volume labelled with the run, once the
// containers that mounted them are gone.
func removeRunVolumes(ctx context.Context, cli *client.Client, filters client.Filters) error {
	volumes, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	var errs []error
	for _, workloadVolume := range volumes.Items {
		if _, err := cli.VolumeRemove(ctx, workloadVolume.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", workloadVolume.Name, err))
		}
	}
	return errors.Join(errs...)
}

// WorkloadScope names the simulator that owns the workloads it starts: its
// state directory when it persists, which a restarted simulator shares, and
// its run otherwise. Names derived from it never collide with those of a
// concurrent simulator.
func WorkloadScope() string {
	if simulatorStateID != "" {
		return simulatorStateID
	}
	return simulatorRunID
}

// VolumeExists reports whether a named volume exists on the engine. Callers
// on the modeled tier (no engine) get false, which is the truth there.
func VolumeExists(name string) bool {
	if dockerClient == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	return err == nil
}

// drainImagePull consumes a docker image-pull response stream and
// surfaces the failure it may carry: the daemon reports pull errors as
// JSON events INSIDE a 200 response body, so discarding the stream
// turns a transient registry failure into an opaque "No such image" at
// container create.
func drainImagePull(reader io.Reader, imageName string) error {
	dec := json.NewDecoder(reader)
	for {
		var ev struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&ev); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("image pull %s: malformed pull stream: %w", imageName, err)
		}
		if ev.Error != "" {
			return fmt.Errorf("image pull %s: %s", imageName, ev.Error)
		}
		if ev.ErrorDetail.Message != "" {
			return fmt.Errorf("image pull %s: %s", imageName, ev.ErrorDetail.Message)
		}
	}
}

// InspectHeldImage inspects the image the engine holds for name. `docker load`
// restores no repository digest, so the engine holds a digest-pinned image a
// base-image archive restored only under the tag that spells its digest,
// repo:sha256-<hex>, which is where the archive saves it.
func InspectHeldImage(ctx context.Context, cli *client.Client, name string) (client.ImageInspectResult, error) {
	held, err := cli.ImageInspect(ctx, name)
	if err == nil {
		return held, nil
	}
	if tag := digestTag(name); tag != "" {
		if byTag, tagErr := cli.ImageInspect(ctx, tag); tagErr == nil {
			return byTag, nil
		}
	}
	return held, err
}

// digestTag is the local tag of a digest-pinned image, repo:sha256-<hex> for
// repo:tag@sha256:<hex>, or "" for a name that pins no digest.
func digestTag(name string) string {
	repo, digest, pinned := strings.Cut(name, "@sha256:")
	if !pinned {
		return ""
	}
	if slash, colon := strings.LastIndex(repo, "/"), strings.LastIndex(repo, ":"); colon > slash {
		repo = repo[:colon]
	}
	return repo + ":sha256-" + digest
}

// pullImage pulls imageName, pinned to platform when one is named, retrying
// registry throttling (toomanyrequests, 429, 503) with bounded exponential
// backoff: public mirrors rate-limit unauthenticated pulls, and a hard fail
// would turn a moment of throttle into a failed workload. Anything else fails
// at once.
func pullImage(ctx context.Context, cli *client.Client, imageName, platform, registryAuth string) error {
	pullOpts := client.ImagePullOptions{RegistryAuth: registryAuth}
	var wanted *ocispec.Platform
	if platform != "" {
		parsed, err := parsePlatform(platform)
		if err != nil {
			return err
		}
		wanted = parsed
		pullOpts.Platforms = []ocispec.Platform{*parsed}
	}
	// An image the host already holds needs no registry request, as `docker
	// run`'s default pull policy ("missing") does. It also keeps a data cap from
	// failing a workload the host could start: a capped registry refuses the
	// manifest check as readily as the layers, and the backoff below recovers
	// from throttling, never from a cap.
	//
	// Check a pinned platform against what the host holds: the simulator's own
	// architecture is routinely not the workload's, and starting an amd64 image
	// where arm64 was asked for is worse than fetching.
	held, err := InspectHeldImage(ctx, cli, imageName)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "[sim-pull] image pull %s: not held: %v\n", imageName, err)
	case wanted == nil ||
		((wanted.Architecture == "" || wanted.Architecture == held.Architecture) &&
			(wanted.OS == "" || wanted.OS == held.Os)):
		return nil
	default:
		fmt.Fprintf(os.Stderr, "[sim-pull] image pull %s: held for %s/%s, wanted %s\n",
			imageName, held.Os, held.Architecture, platform)
	}
	// Registries meter pulls per minute (mirror.gcr.io answers "Requests per
	// project ... per minute"), so the waits span more than one: 2+4+8+16+32s.
	backoff := 2 * time.Second
	const maxAttempts = 6
	for attempt := 1; ; attempt++ {
		reader, err := cli.ImagePull(ctx, imageName, pullOpts)
		var pullErr error
		if err != nil {
			pullErr = fmt.Errorf("image pull %s: %w", imageName, err)
		} else {
			pullErr = drainImagePull(reader, imageName)
			_ = reader.Close()
		}
		if pullErr == nil {
			return nil
		}
		if attempt >= maxAttempts || !isTransientRegistryErr(pullErr) {
			return pullErr
		}
		fmt.Fprintf(os.Stderr, "[sim-pull] image pull %s: attempt %d/%d failed: %v; retrying in %s\n",
			imageName, attempt, maxAttempts, pullErr, backoff)
		select {
		case <-ctx.Done():
			return pullErr
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// selinuxRelabelBinds appends the shared SELinux relabel option (`z`) to each
// bind so a workload container on an SELinux-enforcing host (a rootful Podman
// machine, say) can read and write the bind-mounted directory. The containers
// that mount a simulator file share all share it, hence `z` and not the
// private `Z`. Hosts without SELinux ignore the option. A bind that already
// states a relabel option keeps it: the engine refuses a second one.
func selinuxRelabelBinds(binds []string) []string {
	out := make([]string, 0, len(binds))
	for _, b := range binds {
		parts := strings.SplitN(b, ":", 3)
		if len(parts) < 2 {
			out = append(out, b)
			continue
		}
		if len(parts) == 3 && parts[2] != "" {
			if bindHasRelabelOption(parts[2]) {
				out = append(out, b)
			} else {
				out = append(out, b+",z")
			}
		} else {
			out = append(out, parts[0]+":"+parts[1]+":z")
		}
	}
	return out
}

func bindHasRelabelOption(options string) bool {
	for _, option := range strings.Split(options, ",") {
		switch option {
		case "z", "Z", "O":
			return true
		}
	}
	return false
}

// StopAndRemoveContainer stops and removes the container, giving its process
// the grace the cloud grants between SIGTERM and SIGKILL. It is idempotent
// cleanup that callers defer, so it logs a removal that did not finish rather
// than returning it.
func StopAndRemoveContainer(containerID string, grace time.Duration) {
	cli := DockerClient()
	if cli == nil {
		return
	}
	managedContainers.Delete(containerID)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second+grace)
	defer stopCancel()
	timeout := int(grace / time.Second)
	_, _ = cli.ContainerStop(stopCtx, containerID, client.ContainerStopOptions{Timeout: &timeout})
	// The container's own watcher may be removing it already, and the engine
	// then answers this remove at once; waiting for the removal returns only
	// once the container and its anonymous volumes are gone.
	removed := cli.ContainerWait(stopCtx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	_, _ = cli.ContainerRemove(stopCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	select {
	case <-removed.Result:
	case err := <-removed.Error:
		if !cerrdefs.IsNotFound(err) {
			log.Printf("remove container %s: %v", containerID, err)
		}
	}
}

// PullImage makes the simulator runtime's bounded, stream-aware pull path
// available to cloud-product translators that must inspect an image before
// they can construct its container configuration.
func PullImage(ctx context.Context, imageName, platform string) error {
	return PullImageWithCredential(ctx, imageName, platform, "")
}

// PullImageOn pulls like PullImage through the engine cli rather than the
// simulator runtime's own.
func PullImageOn(ctx context.Context, cli *client.Client, imageName, platform string) error {
	return pullImage(ctx, cli, imageName, platform, "")
}

// PullImageWithCredential pulls like PullImage, presenting registryAuth (the
// engine API's X-Registry-Auth value) to the image's registry.
func PullImageWithCredential(ctx context.Context, imageName, platform, registryAuth string) error {
	cli := DockerClient()
	if cli == nil {
		return fmt.Errorf("container runtime is not initialized")
	}
	return pullImage(ctx, cli, imageName, platform, registryAuth)
}

// isTransientRegistryErr classifies pull failures worth retrying:
// registry-side throttling and momentary unavailability.
func isTransientRegistryErr(err error) bool {
	msg := strings.ToLower(err.Error())
	// A data cap is not throttling. Amazon ECR Public, which serves the images
	// AWS publishes such as the AWS Lambda base images, answers an exhausted
	// anonymous allowance with "toomanyrequests: Data limit exceeded", which
	// waiting does not clear; fail at once so the reason sits where the failure is.
	if strings.Contains(msg, "data limit exceeded") {
		return false
	}
	return strings.Contains(msg, "toomanyrequests") ||
		strings.Contains(msg, "rate exceeded") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "status code 429") ||
		strings.Contains(msg, "status code 503") ||
		strings.Contains(msg, "service unavailable")
}

func createAndStartContainer(ctx context.Context, cli *client.Client, cfg ContainerConfig) (string, time.Time, error) {
	pullPolicy := os.Getenv("SIM_PULL_POLICY")
	if pullPolicy == "" {
		pullPolicy = "if-not-present"
	}

	// pullImage decides "if-not-present", holding the image present only when the
	// host has it for the platform asked for: an image held under the name for
	// another architecture would run the wrong binary — a Go program crashes at
	// once under emulation.
	if pullPolicy != "never" {
		if err := pullImage(ctx, cli, cfg.Image, cfg.Architecture, cfg.RegistryAuth); err != nil {
			return "", time.Time{}, err
		}
	}

	// Create by image ID: Podman's Docker-compatible API resolves a short name
	// ("name:tag") on inspect and pull but not on create, where a locally built
	// image reports "no such image".
	imageRef := cfg.Image
	if inspect, err := InspectHeldImage(ctx, cli, cfg.Image); err == nil && inspect.ID != "" {
		imageRef = inspect.ID
	}

	var env []string
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}

	labels := simulatorLabels(nil)
	for k, v := range cfg.Labels {
		labels[k] = v
	}

	containerCfg := &container.Config{
		Image:       imageRef,
		Env:         env,
		Labels:      labels,
		Tty:         cfg.Tty,
		OpenStdin:   cfg.OpenStdin,
		AttachStdin: cfg.OpenStdin,
		WorkingDir:  cfg.WorkingDir,
		Hostname:    cfg.Hostname,
	}
	if len(cfg.PublishPorts) > 0 {
		containerCfg.ExposedPorts = network.PortSet{}
	}

	if len(cfg.Command) > 0 {
		containerCfg.Entrypoint = cfg.Command
	}
	if len(cfg.Args) > 0 {
		containerCfg.Cmd = cfg.Args
	}

	hostCfg := &container.HostConfig{
		Binds:      selinuxRelabelBinds(cfg.Binds),
		ExtraHosts: cfg.ExtraHosts,
	}
	for _, resolver := range cfg.DNS {
		addr, err := netip.ParseAddr(resolver)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("dns resolver %q: %w", resolver, err)
		}
		hostCfg.DNS = append(hostCfg.DNS, addr)
	}
	// Bound the container's cgroup the way the cloud product reports: a Fargate
	// task that advertises 512 CPU / 1024 MiB sees a matching memory.max and
	// cpu.max, not the host's full capacity.
	if cfg.MemoryBytes > 0 {
		hostCfg.Memory = cfg.MemoryBytes
	}
	if cfg.NanoCPU > 0 {
		hostCfg.NanoCPUs = cfg.NanoCPU
	}
	if cfg.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(cfg.NetworkMode)
	}
	if cfg.PidMode != "" {
		hostCfg.PidMode = container.PidMode(cfg.PidMode)
	}
	for _, device := range cfg.Devices {
		hostCfg.Devices = append(hostCfg.Devices, container.DeviceMapping{PathOnHost: device, PathInContainer: device, CgroupPermissions: "rwm"})
	}
	for _, containerPort := range cfg.PublishPorts {
		port, err := network.ParsePort(strconv.Itoa(containerPort) + "/tcp")
		if err != nil {
			return "", time.Time{}, fmt.Errorf("publish port %d: %w", containerPort, err)
		}
		containerCfg.ExposedPorts[port] = struct{}{}
		if hostCfg.PortBindings == nil {
			hostCfg.PortBindings = network.PortMap{}
		}
		hostCfg.PortBindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1")}}
	}

	// An empty profile enforces nothing; a non-empty one must apply cleanly.
	if err := cfg.Sandbox.Apply(hostCfg, containerCfg); err != nil {
		return "", time.Time{}, fmt.Errorf("sandbox enforce: %w", err)
	}

	var networkCfg *network.NetworkingConfig
	if cfg.Network != "" && cfg.NetworkMode == "" {
		endpoint := &network.EndpointSettings{Aliases: cfg.NetworkAliases}
		if cfg.IPv4Address != "" {
			addr, err := netip.ParseAddr(cfg.IPv4Address)
			if err != nil || !addr.Is4() {
				return "", time.Time{}, fmt.Errorf("container network address %q is not an IPv4 address", cfg.IPv4Address)
			}
			endpoint.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: addr}
		}
		if cfg.MACAddress != "" {
			mac, err := net.ParseMAC(cfg.MACAddress)
			if err != nil {
				return "", time.Time{}, fmt.Errorf("container MAC address %q: %w", cfg.MACAddress, err)
			}
			endpoint.MacAddress = network.HardwareAddr(mac)
		}
		networkCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{cfg.Network: endpoint},
		}
	}

	if err := ensureBindVolumes(ctx, cli, cfg.Binds); err != nil {
		return "", time.Time{}, err
	}
	platform, err := parsePlatform(cfg.Architecture)
	if err != nil {
		return "", time.Time{}, err
	}
	create := client.ContainerCreateOptions{
		Config:           containerCfg,
		HostConfig:       hostCfg,
		NetworkingConfig: networkCfg,
		Platform:         platform,
		Name:             cfg.Name,
	}
	resp, err := cli.ContainerCreate(ctx, create)
	if err != nil && cerrdefs.IsConflict(err) && reclaimOrphanedName(ctx, cli, cfg.Name) {
		resp, err = cli.ContainerCreate(ctx, create)
	}
	if err != nil {
		// The engine can finish a create whose request the client abandoned
		// when ctx ended, so remove what it may have made under the name.
		if cfg.Name != "" && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			removeCreated(ctx, cli, cfg.Name)
		}
		return "", time.Time{}, fmt.Errorf("container create: %w", err)
	}
	if err := seedContainer(ctx, cli, resp.ID, cfg.Seed); err != nil {
		removeCreated(ctx, cli, resp.ID)
		return "", time.Time{}, err
	}

	startedAt := time.Now()
	if _, err := cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		removeCreated(ctx, cli, resp.ID)
		if hint := MissingNetfilterTableHint(err); hint != "" {
			return "", time.Time{}, fmt.Errorf("container start: %w (%s)", err, hint)
		}
		return "", time.Time{}, fmt.Errorf("container start: %w", err)
	}

	return resp.ID, startedAt, nil
}

// reclaimOrphanedName removes the container holding name when a simulator
// process that is gone made it: the engine's names are the simulator's to
// give, and a run that died before its cleanup leaves a container no live
// simulator drives.
func reclaimOrphanedName(ctx context.Context, cli *client.Client, name string) bool {
	if name == "" {
		return false
	}
	held, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil || held.Container.Config == nil {
		return false
	}
	labels := held.Container.Config.Labels
	if labels["sockerless-sim"] != "true" || labels["sockerless-sim-run"] == simulatorRunID || !ownerIsDead(labels) {
		return false
	}
	_, err = cli.ContainerRemove(ctx, held.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err == nil
}

// SeedFile is one file written into a container before it starts.
type SeedFile struct {
	// Path is absolute inside the container; its directory is created.
	Path string
	Mode int64
	Data []byte
}

// seedContainer writes the files into the created container through the
// engine's archive endpoint, one archive rooted at /.
func seedContainer(ctx context.Context, cli *client.Client, containerID string, files []SeedFile) error {
	if len(files) == 0 {
		return nil
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, f := range files {
		if !path.IsAbs(f.Path) {
			return fmt.Errorf("seed file %q: path must be absolute", f.Path)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(f.Path, "/"), Mode: mode, Size: int64(len(f.Data))}); err != nil {
			return err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if _, err := cli.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: "/", Content: &archive}); err != nil {
		return fmt.Errorf("seed container: %w", err)
	}
	return nil
}

// removeCreated removes a container whose start failed. It outlives ctx: a
// start abandoned because ctx ended still must not leave its container behind.
func removeCreated(ctx context.Context, cli *client.Client, containerID string) {
	removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = cli.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}

// MissingNetfilterTableHint names the kernel dependency behind a container
// runtime's refusal to wire a container onto a network, and returns "" for
// every other failure. Docker 28 and later programs a raw-table PREROUTING DROP
// rule when it attaches a container to a bridge network, so a kernel built
// without the corresponding netfilter table cannot start the workload at all;
// the runtime reports the table it could not initialise but not the module that
// supplies it, which leaves a minimal guest kernel (a Firecracker microVM, a
// container-optimised image) looking like a simulator defect.
func MissingNetfilterTableHint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const marker = "can't initialize iptables table `"
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.IndexByte(rest, '\'')
	if j <= 0 {
		return ""
	}
	table := rest[:j]
	return fmt.Sprintf("the kernel running the container runtime has no netfilter %q table; "+
		"load the iptable_%s module or boot a kernel built with CONFIG_IP_NF_%s",
		table, table, strings.ToUpper(table))
}

func waitAndCaptureLogs(ctx context.Context, cli *client.Client, containerID string, cfg ContainerConfig, sink LogSink) ProcessResult {
	startedAt := time.Now()
	killCancelledContainer := func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second+cfg.CancelGracePeriod)
		defer killCancel()
		if cfg.CancelGracePeriod > 0 {
			grace := int(cfg.CancelGracePeriod / time.Second)
			_, _ = cli.ContainerStop(killCtx, containerID, client.ContainerStopOptions{Timeout: &grace})
			return
		}
		_, _ = cli.ContainerKill(killCtx, containerID, client.ContainerKillOptions{Signal: "KILL"})
	}

	// Stream logs live while the workload runs, as the awslogs driver forwards
	// each line to Amazon CloudWatch Logs as the workload writes it. The stream
	// runs on a detached context so a caller's cancel cannot truncate it before
	// the exit drain below.
	counting := &lineCountingSink{sink: sink, counts: map[string]int{}}
	followCtx, followCancel := context.WithCancel(context.Background())
	defer followCancel()
	followDone := make(chan struct{})
	go func() {
		defer close(followDone)
		followContainerLogs(followCtx, cli, containerID, counting)
	}()

	if cfg.Timeout > 0 {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(cfg.Timeout):
				timeout := 5
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer stopCancel()
				_, _ = cli.ContainerStop(stopCtx, containerID, client.ContainerStopOptions{Timeout: &timeout})
			}
		}()
	}

	var result ProcessResult
	wait := cli.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	statusCh, errCh := wait.Result, wait.Error
	select {
	case err := <-errCh:
		if err != nil {
			if ctx.Err() != nil {
				// ContainerWait reports the cancelled request on errCh as
				// well as closing ctx.Done. Kill the workload on either
				// branch so a cancelled AWS CodeBuild command cannot keep
				// running and produce effects after StopBuild returned.
				killCancelledContainer()
			}
			result = ProcessResult{
				ExitCode:  -1,
				StartedAt: startedAt,
				StoppedAt: time.Now(),
				Error:     err,
			}
		}
	case status := <-statusCh:
		result = ProcessResult{
			ExitCode:  int(status.StatusCode),
			StartedAt: startedAt,
			StoppedAt: time.Now(),
		}
	case <-ctx.Done():
		killCancelledContainer()
		result = ProcessResult{
			ExitCode:  137,
			StartedAt: startedAt,
			StoppedAt: time.Now(),
		}
	}

	// The follow stream ends when the container exits; give it a bounded
	// grace to deliver its tail, then release it.
	select {
	case <-followDone:
	case <-time.After(10 * time.Second):
		followCancel()
		<-followDone
	}

	// Drain what the follow stream did not deliver: it may have attached after a
	// short-lived container exited, or seen EOF before the last buffered lines
	// were demuxed. Both reads walk the same per-stream buffer in order, so
	// skipping the lines the live stream wrote appends exactly the missing tail.
	// A detached context keeps a caller's cancel from interrupting the read.
	readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readCancel()
	written := &lineCountingSink{sink: sink, counts: map[string]int{}}
	var drainErr error
	for attempt := 1; attempt <= 3; attempt++ {
		// Each attempt reads the buffer from its start, so it skips what the
		// follow stream and every earlier attempt already wrote.
		skip := counting.delivered()
		for stream, n := range written.delivered() {
			skip[stream] += n
		}
		if drainErr = drainContainerLogs(readCtx, cli, containerID, &lineSkippingSink{sink: written, skip: skip}); drainErr == nil || readCtx.Err() != nil {
			break
		}
	}
	if drainErr != nil {
		fmt.Fprintf(os.Stderr, "[sim-logs] drain container %s after exit: %v; lines it wrote after the last one read are not delivered\n", containerID, drainErr)
	}

	return result
}

// lineCountingSink forwards every line to the underlying sink while counting
// deliveries per stream, so the post-exit drain can skip exactly the lines
// the live follow stream already wrote.
type lineCountingSink struct {
	mu     sync.Mutex
	sink   LogSink
	counts map[string]int
}

func (s *lineCountingSink) WriteLog(line LogLine) {
	s.mu.Lock()
	s.counts[line.Stream]++
	s.mu.Unlock()
	s.sink.WriteLog(line)
}

// delivered snapshots the per-stream line counts. Call only after the follow
// stream has finished.
func (s *lineCountingSink) delivered() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.counts))
	for stream, n := range s.counts {
		out[stream] = n
	}
	return out
}

// lineSkippingSink drops the first skip[stream] lines of each stream and
// forwards the rest. The post-exit drain demuxes stdout and stderr in two
// goroutines that share this sink, so the skip ledger takes a lock the same
// way the counting sink's does.
type lineSkippingSink struct {
	mu   sync.Mutex
	sink LogSink
	skip map[string]int
}

func (s *lineSkippingSink) WriteLog(line LogLine) {
	s.mu.Lock()
	if s.skip[line.Stream] > 0 {
		s.skip[line.Stream]--
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.sink.WriteLog(line)
}

// followContainerLogs streams the container's demuxed log lines to sink as
// they are produced; it returns when the container exits (Docker closes the
// follow stream) or ctx is cancelled.
func followContainerLogs(ctx context.Context, cli *client.Client, containerID string, sink LogSink) {
	reader, err := cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sim-logs] follow container %s: %v; the post-exit drain reads its log instead\n", containerID, err)
		return
	}
	if err := streamDockerLogs(reader, sink); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "[sim-logs] follow container %s: %v; the post-exit drain reads the rest\n", containerID, err)
	}
}

// drainContainerLogs reads the whole container log with a non-follow
// ContainerLogs and forwards every demuxed line to sink. Call it once the
// container has exited; Docker keeps the log until the container is removed.
func drainContainerLogs(ctx context.Context, cli *client.Client, containerID string, sink LogSink) error {
	reader, err := cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Timestamps: true,
	})
	if err != nil {
		return err
	}
	return streamDockerLogs(reader, sink)
}

// splitDockerLogTimestamp splits the RFC3339Nano timestamp Docker prepends to
// each line when ContainerLogs asks for timestamps. A line without one, from
// an engine that ignored the option, keeps its whole text and is dated now.
func splitDockerLogTimestamp(line string) (time.Time, string) {
	stamp, rest, found := strings.Cut(line, " ")
	if !found {
		return time.Now(), line
	}
	written, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Now(), line
	}
	return written, rest
}

func streamDockerLogs(reader io.ReadCloser, sink LogSink) error {
	defer reader.Close()

	stdoutPR, stdoutPW := io.Pipe()
	stderrPR, stderrPW := io.Pipe()

	var copyErr error
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, copyErr = stdcopy.StdCopy(stdoutPW, stderrPW, reader)
		_ = stdoutPW.CloseWithError(copyErr)
		_ = stderrPW.CloseWithError(copyErr)
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	scanErrs := make([]error, 2)

	scanStream := func(r io.Reader, stream string, slot int) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		// The json-file driver splits a line at 16 KiB; the room left over
		// takes the timestamp the read asks Docker to prepend.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Date the line by when the container wrote it, not by when this reader got
			// to it: a reader lagging behind a slow start would report its lag as the
			// workload's.
			stamp, text := splitDockerLogTimestamp(scanner.Text())
			sink.WriteLog(LogLine{
				Stream:    stream,
				Text:      text,
				Timestamp: stamp,
			})
		}
		if err := scanner.Err(); err != nil {
			scanErrs[slot] = fmt.Errorf("read %s: %w", stream, err)
			// Keep the demuxer unblocked so the other stream still ends.
			_, _ = io.Copy(io.Discard, r)
		}
	}

	go scanStream(stdoutPR, "stdout", 0)
	go scanStream(stderrPR, "stderr", 1)

	wg.Wait()
	<-copied
	return errors.Join(copyErr, scanErrs[0], scanErrs[1])
}

// ResolveLocalImage maps pull-through-cache coordinates back to their upstream
// Docker Hub images for local execution. A cloud can resolve "alpine:latest"
// to a private registry cache of its own:
//   - Artifact Registry: "us-central1-docker.pkg.dev/project/docker-hub/library/alpine:latest"
//   - Amazon ECR: "123456789.dkr.ecr.eu-west-1.amazonaws.com/alpine:latest"
//   - Azure Container Registry: "myacr.azurecr.io/library/alpine:latest"
//
// The local engine pulls public registry coordinates directly, unchanged.
func ResolveLocalImage(image string) string {
	// Artifact Registry pull-through cache.
	if strings.Contains(image, "-docker.pkg.dev/") && strings.Contains(image, "/docker-hub/") {
		idx := strings.Index(image, "/docker-hub/")
		dockerPath := image[idx+len("/docker-hub/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	// Amazon ECR pull-through cache: strip docker-hub/ before library/, since a
	// Docker Hub cache hit is always
	// `<acct>.dkr.ecr.<region>.amazonaws.com/docker-hub/library/<name>`.
	if strings.Contains(image, ".dkr.ecr.") && strings.Contains(image, ".amazonaws.com/") {
		idx := strings.Index(image, ".amazonaws.com/")
		dockerPath := image[idx+len(".amazonaws.com/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "docker-hub/")
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	// Azure Container Registry.
	if strings.Contains(image, ".azurecr.io/") {
		idx := strings.Index(image, ".azurecr.io/")
		dockerPath := image[idx+len(".azurecr.io/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "docker-hub/")
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	return image
}

// EnsureDockerNetwork creates the user-defined engine network name unless it
// exists, and returns its ID. A user-defined network gives its containers the
// engine's embedded DNS.
func EnsureDockerNetwork(name string) (string, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("creating a container network"); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if existing, err := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
		return existing.Network.ID, nil
	}
	resp, err := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: simulatorLabels(nil),
	})
	if err != nil {
		// A concurrent caller created it first.
		if existing, inspectErr := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); inspectErr == nil {
			return existing.Network.ID, nil
		}
		return "", fmt.Errorf("network create %s: %w", name, err)
	}
	return resp.ID, nil
}

// networkRemoveRefused runs each time the engine refuses a network removal; a
// test replaces it to learn that a removal is waiting on a disconnect.
var networkRemoveRefused = func(string) {}

// RemoveDockerNetwork removes a simulator-managed Docker network if it exists,
// and is idempotent for a missing one. The engine refuses to remove a network
// with endpoints still attached, so each refusal waits for a container to
// disconnect from it before trying again.
func RemoveDockerNetwork(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return RemoveDockerNetworkContext(ctx, name)
}

// RemoveDockerNetworkContext is RemoveDockerNetwork waiting for the last
// container to disconnect for as long as ctx allows.
func RemoveDockerNetworkContext(ctx context.Context, name string) error {
	cli := DockerClient()
	if cli == nil {
		return nil
	}
	// Subscribe before the first attempt, so a disconnect that lands between a
	// refused removal and the wait still wakes it.
	disconnects := cli.Events(ctx, client.EventsListOptions{
		Filters: client.Filters{}.Add("type", "network").Add("event", "disconnect").Add("network", name),
	})
	for {
		if _, err := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err != nil {
			if cerrdefs.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("inspect network %s: %w", name, err)
		}
		_, removeErr := cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
		if removeErr == nil || cerrdefs.IsNotFound(removeErr) {
			return nil
		}
		networkRemoveRefused(name)
		select {
		case <-disconnects.Messages:
		case err := <-disconnects.Err:
			return fmt.Errorf("remove network %s: %w (watching its disconnects: %v)", name, removeErr, err)
		case <-ctx.Done():
			return fmt.Errorf("remove network %s: %w", name, removeErr)
		}
	}
}

// ConnectContainerToNetwork connects a running container to a Docker
// network with the given DNS aliases. Idempotent: if the container is
// already on the network, the call updates aliases and returns nil.
func ConnectContainerToNetwork(containerName, networkName string, aliases []string) error {
	cli := DockerClient()
	if err := RequireContainerRuntime("attaching a container to a network"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := cli.NetworkConnect(ctx, networkName, client.NetworkConnectOptions{
		Container: containerName,
		EndpointConfig: &network.EndpointSettings{
			Aliases: aliases,
		},
	})
	return err
}

type HostEntry struct {
	IP   string
	Name string
}

// RuntimeInfo returns the container runtime name and version for display.
func RuntimeInfo() string {
	if dockerClient == nil {
		return "not initialized"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := dockerClient.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	name := "Docker"
	for _, c := range info.Components {
		if strings.EqualFold(c.Name, "Podman Engine") {
			name = "Podman"
			break
		}
	}
	return fmt.Sprintf("%s %s", name, info.Version)
}

// DefaultContainerNetworkGatewayIPv4 returns the host-side gateway of the
// container runtime's default bridge. A simulator process running directly on
// Linux listens in the host namespace, so workload containers reach its
// callback listeners through this address. Standard host aliases can point
// outside that Linux host (notably inside a Podman machine), whereas the
// runtime-reported bridge gateway is the actual packet coordinate.
func DefaultContainerNetworkGatewayIPv4() (string, error) {
	if err := RequireContainerRuntime("reading the container network gateway"); err != nil {
		return "", err
	}
	networkName := "bridge"
	if strings.Contains(strings.ToLower(RuntimeInfo()), "podman") {
		networkName = "podman"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := dockerClient.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect default container network %s: %w", networkName, err)
	}
	if info.Network.IPAM.Config == nil {
		return "", fmt.Errorf("default container network %s has no IPAM configuration", networkName)
	}
	for _, config := range info.Network.IPAM.Config {
		gateway := config.Gateway.Unmap()
		if !gateway.Is4() {
			continue
		}
		return gateway.String(), nil
	}
	return "", fmt.Errorf("default container network %s has no IPv4 gateway", networkName)
}
