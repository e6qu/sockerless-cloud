package simulator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/netip"
	"os"
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
)

// parsePlatform splits an "os/arch" or "os/arch/variant" string into an
// ocispec.Platform. Returns an error on empty or malformed input —
// every caller must be explicit per `feedback_sim_host_model.md`. No
// silent fallback to "image default / host arch".
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

// ContainerConfig describes a container to run.
//
// Architecture carries the workload's target arch (e.g. "linux/arm64",
// "linux/amd64"). The simulator never derives this from the host —
// the workload's spec carries the field; cloud-product translators
// pass it through. Empty string means "use the image's default" which
// in practice resolves to the host arch via Docker (treat that as a
// not-yet-migrated caller).
type ContainerConfig struct {
	Image        string            // container image (e.g., "alpine:latest")
	Architecture string            // OS/arch (e.g. "linux/arm64"); see field-level docstring above
	Command      []string          // entrypoint override (empty = use image default)
	Args         []string          // command/args (empty = use image default)
	Env          map[string]string // environment variables
	Timeout      time.Duration     // max execution time (0 = no limit)
	Labels       map[string]string // container labels for tracking
	Network      string            // Docker network to join (optional)
	// ENIAddress is the workload's VPC elastic network interface IPv4 address
	// in CIDR notation ("10.230.0.4/16"), its prefix length being the VPC
	// CIDR's. The container's bridge-native primary address comes from the
	// network's host-side allocator subnet (EnsureVPCNetwork), never from the
	// VPC CIDR; after start, an ephemeral CAP_NET_ADMIN setup container adds
	// ENIAddress as a secondary address on eth0, so the workload genuinely
	// owns its VPC address while holding no network capability itself —
	// matching real Amazon ECS, where the platform plumbs the ENI and the
	// task gets no CAP_NET_ADMIN. The connected route the kernel derives from
	// the prefix makes every same-VPC peer on the shared bridge reachable on
	// its ENI address, while same-CIDR VPCs sit on different bridges and
	// never see each other.
	ENIAddress  string
	NetworkMode string   // Docker network mode (e.g. "container:<id>" for shared netns)
	Name        string   // container name (optional, auto-generated if empty)
	Tty         bool     // allocate a pseudo-TTY
	OpenStdin   bool     // keep stdin open
	Binds       []string // bind mounts (e.g., "vol:/path")
	ExtraHosts  []string // --add-host entries (e.g., "host.docker.internal:host-gateway")
	// DNS are the resolvers written into the container's /etc/resolv.conf. A
	// workload in its own network namespace cannot reach the embedded resolver
	// Docker would otherwise configure, so the namespace's own resolver is named
	// here; empty leaves Docker's default in place.
	DNS        []string
	WorkingDir string // working directory inside the container (optional)

	// PublishPorts maps containerPort → hostPort (bound on 127.0.0.1).
	// Used by host-addressed data planes that must reach a workload's
	// listener cross-platform: container IPs are only routable from the
	// host on Linux, while a loopback port binding works on Docker
	// Desktop (macOS/Windows) too. The caller allocates the host port.
	PublishPorts map[int]int

	// Sandbox: per-platform capability + permission restrictions. Each
	// cloud-product handler picks the matching profile (SandboxLambda,
	// SandboxFargate, and so on). Zero value = no sandbox enforcement;
	// callers without an explicit profile see a one-time warning at
	// startup but the container still runs. Production callers must
	// always set Sandbox.
	Sandbox SandboxProfile

	// MemoryBytes is the hard memory limit (cgroup memory.max) applied to the
	// container, in bytes. Zero = unbounded. Cloud handlers translate the
	// product's advertised sizing (ECS/Fargate task or container memory) here
	// so the container's cgroup matches what the metadata advertises.
	MemoryBytes int64

	// NanoCPU is the CPU limit (cgroup cpu.max) in units of 1e-9 CPUs — e.g.
	// 1_000_000_000 == 1 vCPU. Zero = unbounded. Cloud handlers translate the
	// product's advertised CPU sizing here.
	NanoCPU int64

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
	done        <-chan ProcessResult
	cli         *client.Client
	memory      *memoryPeakObserver
}

// Wait blocks until the container exits.
func (h *ContainerHandle) Wait() ProcessResult { return <-h.done }

// Cancel stops and removes the container.
func (h *ContainerHandle) Cancel() { h.cancel() }

// MemoryPeakBytes reports the highest memory usage the container engine
// accounted to this container while it ran. It is zero when the engine reported
// none — a container started without ContainerConfig.TrackMemoryPeak, or one
// the engine stopped accounting for before the first sample — and a caller that
// reports the figure must then report nothing rather than a substitute.
func (h *ContainerHandle) MemoryPeakBytes() uint64 {
	if h.memory == nil {
		return 0
	}
	return h.memory.peakBytes()
}

// dockerClient is the shared Docker client. Initialized once at startup.
var (
	dockerClient     *client.Client
	dockerClientOnce sync.Once
	dockerClientErr  error
)

// engineUnavailableAdvice is what an operator can do about a container engine
// the simulator could not reach.
const engineUnavailableAdvice = "simulators require Docker or Podman for workload execution; " +
	"install Docker/Podman, or set SIM_RUNTIME=process only for explicit API-only runs that do not execute workloads"

// InitDocker initializes the shared Docker client and verifies connectivity.
// Called at simulator startup for every runtime mode that executes workloads.
//
// It reports an error rather than exiting the process, so that the decision to
// refuse to start belongs to startup — which can say what it was configuring
// and can be exercised by a test — instead of to a library call. A simulator
// that cannot reach an engine must not reach the point of serving requests: the
// alternative is a process that answers its health check while every workload
// it is asked to run fails at first use.
func InitDocker(provider string, preserveWorkloads bool, stateDir string) (*client.Client, error) {
	configureSimulatorIdentity(provider, stateDir)
	dockerClientOnce.Do(func() {
		dockerClient, dockerClientErr = client.New(client.FromEnv)
		if dockerClientErr != nil {
			return
		}
		// Verify connectivity
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, dockerClientErr = dockerClient.Ping(ctx, client.PingOptions{}); dockerClientErr != nil {
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

// DockerClient returns the shared Docker client. InitDocker must have been called first.
func DockerClient() *client.Client {
	return dockerClient
}

// RequireContainerRuntime reports why an operation that runs a container cannot
// run in this process. Startup refuses to serve when a workload-executing mode
// cannot reach an engine, so a missing client here means only one thing: the
// simulator was deliberately started API-only. Naming that says which of the
// two very different situations the caller is in — an engine that went away,
// or a process that never had one — instead of leaving a nil variable to be
// reported as if it were an engine fault.
func RequireContainerRuntime(operation string) error {
	if dockerClient != nil {
		return nil
	}
	return fmt.Errorf("%s requires a container runtime: this simulator was started API-only (SIM_RUNTIME=%s) and holds no container engine client",
		operation, RuntimeModeAPIOnly)
}

// ContainerPID returns the host PID of a running container's main process, used
// to plumb a veth into the container's network namespace (the netns VPC fabric).
func ContainerPID(containerID string) (int, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("reading a container's process id"); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return 0, err
	}
	info := inspected.Container
	if info.State == nil || info.State.Pid <= 0 {
		return 0, fmt.Errorf("container %s has no running PID", containerID)
	}
	return info.State.Pid, nil
}

// managedContainers tracks containers created by this simulator instance for cleanup.
var managedContainers sync.Map // containerID -> true

// CleanupContainers stops and removes all simulator-managed containers.
// Also prunes any Docker networks labeled `sockerless-sim=true` that
// aren't in use (typically namespace-backed networks that weren't
// explicitly removed by a DeleteNamespace call).
// Called on simulator shutdown.
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
		_, _ = dockerClient.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		return true
	})

	nets, err := dockerClient.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "sockerless-sim-run="+simulatorRunID),
	})
	if err == nil {
		for _, n := range nets.Items {
			_, _ = dockerClient.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
		}
	}
}

// StartContainerSync pulls the image (if needed), creates and starts a
// container, returning the handle with ContainerID populated.
// Blocks until the container is created and started (but not until it exits).
// Stdout/stderr are streamed to the LogSink; call handle.Wait() to block until exit.
func StartContainerSync(cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("starting a container"); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan ProcessResult, 1)

	containerID, err := createAndStartContainer(ctx, cli, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	managedContainers.Store(containerID, true)

	var memory *memoryPeakObserver
	if cfg.TrackMemoryPeak {
		memory = newMemoryPeakObserver()
		go memory.observe(ctx, cli, containerID)
	}

	// Stream logs and wait for exit in background
	go func() {
		result := waitAndCaptureLogs(ctx, cli, containerID, cfg, sink)
		// The observation ends with the container, before it is removed: the
		// engine accounts for a container only while it runs.
		if memory != nil {
			memory.stop()
		}
		managedContainers.Delete(containerID)
		// Remove container after exit
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rmCancel()
		_, _ = cli.ContainerRemove(rmCtx, containerID, client.ContainerRemoveOptions{Force: true})
		resultCh <- result
	}()

	handle := &ContainerHandle{
		ContainerID: containerID,
		cancel:      cancel,
		done:        resultCh,
		cli:         cli,
		memory:      memory,
	}
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
		if err != nil {
			return nil, err
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
		if inspection.NetworkSettings != nil {
			for containerPort, bindings := range inspection.NetworkSettings.Ports {
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
	_, err := dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
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
func AdoptContainer(containerID string, cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	if err := RequireContainerRuntime("adopting a running container"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan ProcessResult, 1)
	managedContainers.Store(containerID, true)
	go func() {
		result := waitAndCaptureLogs(ctx, dockerClient, containerID, cfg, sink)
		managedContainers.Delete(containerID)
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		_, _ = dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true})
		resultCh <- result
	}()
	return &ContainerHandle{
		ContainerID: containerID,
		cancel:      cancel,
		done:        resultCh,
		cli:         dockerClient,
	}, nil
}

// StopContainer stops a running container by ID.
func StopContainer(containerID string) {
	if dockerClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	timeout := 1
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
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := dockerClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		cancel()
		if containerNotFoundError(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect container %s while waiting for removal: %w", containerID, err)
		}
		if !time.Now().Before(deadline) {
			removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, removeErr := dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true})
			removeCancel()
			if removeErr == nil || containerNotFoundError(removeErr) {
				return nil
			}
			return fmt.Errorf("container %s was not removed within %s and forced removal failed: %w", containerID, timeout, removeErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// containerNotFoundError accepts Docker's typed 404 and Podman's
// Docker-compatible API response. Podman currently returns the missing
// container database condition without Docker's NotFound error classification.
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

// pullImage pulls imageName (optionally platform-pinned), retrying
// transient registry throttling: public mirrors rate-limit
// unauthenticated pulls (toomanyrequests / 429 / 503) and a hard fail
// turns a moment of throttle into a failed workload. Bounded
// exponential backoff per the strict rate-limit rule; everything
// non-transient fails immediately.
func pullImage(ctx context.Context, cli *client.Client, imageName, platform string) error {
	pullOpts := client.ImagePullOptions{}
	var wanted *ocispec.Platform
	if platform != "" {
		parsed, err := parsePlatform(platform)
		if err != nil {
			return err
		}
		wanted = parsed
		pullOpts.Platforms = []ocispec.Platform{*parsed}
	}
	// An image the host already holds needs no registry request. This is what
	// `docker run` itself does — its default pull policy is "missing" — and it
	// is what keeps a data cap from turning a workload the host could start
	// into a failure: a capped registry refuses the manifest check as readily
	// as the layers, and the backoff below cannot recover from a cap, only
	// from a moment of throttle.
	//
	// A pinned platform is checked against what the host holds rather than
	// assumed: the simulator's own architecture is routinely not the
	// workload's, and starting an amd64 image where arm64 was asked for would
	// be a worse answer than fetching.
	if held, err := cli.ImageInspect(ctx, imageName); err == nil {
		if wanted == nil ||
			((wanted.Architecture == "" || wanted.Architecture == held.Architecture) &&
				(wanted.OS == "" || wanted.OS == held.Os)) {
			return nil
		}
	}
	backoff := 2 * time.Second
	const maxAttempts = 5
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
		select {
		case <-ctx.Done():
			return pullErr
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// PullImage makes the simulator runtime's bounded, stream-aware pull path
// available to cloud-product translators that must inspect an image before
// they can construct its container configuration.
func PullImage(ctx context.Context, imageName, platform string) error {
	cli := DockerClient()
	if cli == nil {
		return fmt.Errorf("container runtime is not initialized")
	}
	return pullImage(ctx, cli, imageName, platform)
}

// isTransientRegistryErr classifies pull failures worth retrying:
// registry-side throttling and momentary unavailability.
func isTransientRegistryErr(err error) bool {
	msg := strings.ToLower(err.Error())
	// A data cap is not throttling. The ECR Public Gallery answers an exhausted
	// anonymous allowance with "toomanyrequests: Data limit exceeded", which
	// reads like the rate limit below it and is nothing like it: waiting does
	// not clear it, so the backoff only spends two minutes arriving at the same
	// answer. Saying so at once leaves the reason in the log where the failure
	// is, instead of five identical lines above it.
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

func createAndStartContainer(ctx context.Context, cli *client.Client, cfg ContainerConfig) (string, error) {
	// Validate the ENI address before any resource is created.
	var eniAddress netip.Prefix
	if cfg.ENIAddress != "" {
		parsed, err := netip.ParsePrefix(cfg.ENIAddress)
		if err != nil {
			return "", fmt.Errorf("vpc eni address %q: %w", cfg.ENIAddress, err)
		}
		eniAddress = parsed
	}

	// Pull image
	pullPolicy := os.Getenv("SIM_PULL_POLICY")
	if pullPolicy == "" {
		pullPolicy = "if-not-present"
	}

	shouldPull := pullPolicy == "always"
	if pullPolicy == "if-not-present" {
		_, err := cli.ImageInspect(ctx, cfg.Image)
		if err != nil {
			shouldPull = true
		}
	}

	if shouldPull {
		if err := pullImage(ctx, cli, cfg.Image, cfg.Architecture); err != nil {
			return "", err
		}
	}

	// Resolve the image to its ID for ContainerCreate. Podman's docker-compat
	// API resolves a short name ("name:tag") on inspect/pull but not on create:
	// a locally-built image inspects fine yet create reports "no such image".
	// The image ID is unambiguous on both Docker and Podman, so create by ID.
	imageRef := cfg.Image
	if inspect, err := cli.ImageInspect(ctx, cfg.Image); err == nil && inspect.ID != "" {
		imageRef = inspect.ID
	}

	// Build container config
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
	}
	if len(cfg.PublishPorts) > 0 {
		containerCfg.ExposedPorts = network.PortSet{}
	}

	// Set entrypoint and command separately
	if len(cfg.Command) > 0 {
		containerCfg.Entrypoint = cfg.Command
	}
	if len(cfg.Args) > 0 {
		containerCfg.Cmd = cfg.Args
	}

	hostCfg := &container.HostConfig{
		Binds:      cfg.Binds,
		ExtraHosts: cfg.ExtraHosts,
	}
	for _, resolver := range cfg.DNS {
		addr, err := netip.ParseAddr(resolver)
		if err != nil {
			return "", fmt.Errorf("dns resolver %q: %w", resolver, err)
		}
		hostCfg.DNS = append(hostCfg.DNS, addr)
	}
	// Apply the advertised resource limits to the container's cgroup so the
	// workload is actually bounded the way the cloud product reports (e.g. a
	// Fargate task that advertises 512 CPU / 1024 MiB sees a matching
	// memory.max / cpu.max, not the host's full capacity).
	if cfg.MemoryBytes > 0 {
		hostCfg.Memory = cfg.MemoryBytes
	}
	if cfg.NanoCPU > 0 {
		hostCfg.NanoCPUs = cfg.NanoCPU
	}
	if cfg.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(cfg.NetworkMode)
	}
	for containerPort, hostPort := range cfg.PublishPorts {
		port, err := network.ParsePort(strconv.Itoa(containerPort) + "/tcp")
		if err != nil {
			return "", fmt.Errorf("publish port %d: %w", containerPort, err)
		}
		containerCfg.ExposedPorts[port] = struct{}{}
		if hostCfg.PortBindings == nil {
			hostCfg.PortBindings = network.PortMap{}
		}
		publicPort := ""
		if hostPort > 0 {
			publicPort = strconv.Itoa(hostPort)
		}
		hostCfg.PortBindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: publicPort}}
	}

	// Enforce sandbox parity with the real cloud platform. Empty profile
	// means no enforcement; non-empty must apply cleanly so caller errors
	// fail loudly.
	if err := cfg.Sandbox.Apply(hostCfg, containerCfg); err != nil {
		return "", fmt.Errorf("sandbox enforce: %w", err)
	}

	var networkCfg *network.NetworkingConfig
	if cfg.Network != "" && cfg.NetworkMode == "" {
		// The bridge-native primary address is the network's own (allocated by
		// Docker IPAM from the host-side pool subnet); the VPC ENI address is
		// plumbed as a secondary after start (cfg.ENIAddress), so DescribeTasks's
		// privateIPv4Address remains the container's real, routable address.
		networkCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.Network: {},
			},
		}
	}

	platform, err := parsePlatform(cfg.Architecture)
	if err != nil {
		return "", err
	}
	resp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           containerCfg,
		HostConfig:       hostCfg,
		NetworkingConfig: networkCfg,
		Platform:         platform,
		Name:             cfg.Name,
	})
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if _, err := cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		// Cleanup on start failure
		_, _ = cli.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
		if hint := MissingNetfilterTableHint(err); hint != "" {
			return "", fmt.Errorf("container start: %w (%s)", err, hint)
		}
		return "", fmt.Errorf("container start: %w", err)
	}

	if eniAddress.IsValid() {
		if err := attachENIAddress(resp.ID, eniAddress, cfg.Architecture); err != nil {
			// A workload that already ran to completion has no network
			// namespace left to plumb — and no longer needs one: real Amazon
			// ECS detaches the ENI when the task stops, so a short-lived task
			// that exits before its address lands is a finished task, not a
			// failed launch. Only a live workload missing its ENI address is
			// a failure.
			if inspected, inspectErr := cli.ContainerInspect(ctx, resp.ID, client.ContainerInspectOptions{}); inspectErr == nil &&
				inspected.Container.State != nil && !inspected.Container.State.Running {
				return resp.ID, nil
			}
			_, _ = cli.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
			return "", fmt.Errorf("attach vpc eni address %s: %w", eniAddress, err)
		}
	}

	return resp.ID, nil
}

// vpcENISetupImage runs the ephemeral network-setup container that plumbs a
// workload's elastic network interface address — busybox from the Amazon ECR
// Public Gallery (always carries the ip applet, avoids Docker Hub throttling).
const vpcENISetupImage = "public.ecr.aws/docker/library/busybox:latest"

// attachENIAddress adds the elastic network interface address as a secondary
// IPv4 on a running container's eth0. A short-lived setup container joins the
// workload's network namespace with CAP_NET_ADMIN and runs `ip addr add`; the
// capability lives and dies with the setup container, so the workload keeps
// its cloud-faithful, capability-free sandbox. The kernel derives the VPC's
// connected route from the address's prefix, so no separate route needs to be
// installed for same-VPC reachability.
func attachENIAddress(containerID string, address netip.Prefix, architecture string) error {
	var outputMu sync.Mutex
	var output []string
	sink := FuncSink(func(line LogLine) {
		outputMu.Lock()
		output = append(output, line.Text)
		outputMu.Unlock()
	})
	handle, err := StartContainerSync(ContainerConfig{
		Image:        vpcENISetupImage,
		Architecture: architecture,
		Command:      []string{"ip"},
		Args:         []string{"addr", "add", address.String(), "dev", "eth0"},
		NetworkMode:  "container:" + containerID,
		Timeout:      30 * time.Second,
		Sandbox: SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"NET_ADMIN"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
		},
	}, sink)
	if err != nil {
		return err
	}
	result := handle.Wait()
	if result.Error != nil {
		return result.Error
	}
	if result.ExitCode != 0 {
		outputMu.Lock()
		defer outputMu.Unlock()
		return fmt.Errorf("ip addr add %s dev eth0 exited %d: %s", address, result.ExitCode, strings.Join(output, "; "))
	}
	return nil
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
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		_, _ = cli.ContainerKill(killCtx, containerID, client.ContainerKillOptions{Signal: "KILL"})
	}

	// Stream logs live while the workload runs — the awslogs driver forwards
	// each line to CloudWatch Logs as it is produced, so a long-running
	// service task accumulates logs in near-real time instead of becoming
	// visible only after it exits. The stream runs on a detached context so
	// a caller-side cancel cannot truncate it before the exit drain below.
	counting := &lineCountingSink{sink: sink, counts: map[string]int{}}
	followCtx, followCancel := context.WithCancel(context.Background())
	defer followCancel()
	followDone := make(chan struct{})
	go func() {
		defer close(followDone)
		followContainerLogs(followCtx, cli, containerID, counting)
	}()

	// Enforce timeout via a separate goroutine.
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

	// Wait for container to exit.
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

	// Drain any remainder the follow stream did not deliver — it may have
	// attached after a very short-lived container had already exited, or
	// seen EOF before the final buffered lines were demuxed. Both reads
	// walk the same per-stream log buffer in order, so skipping the lines
	// the live stream already wrote appends exactly the missing tail
	// without duplicating anything. Use a detached context with a generous
	// timeout so any caller-side cancel doesn't interrupt the read.
	readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readCancel()
	drainContainerLogs(readCtx, cli, containerID, &lineSkippingSink{sink: sink, skip: counting.delivered()})

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
		Timestamps: false,
	})
	if err != nil {
		return
	}
	defer reader.Close()
	streamDockerLogs(reader, sink)
}

// drainContainerLogs reads the full container log via non-follow
// ContainerLogs and forwards every demuxed line to sink. Called once
// the container has exited; Docker keeps the log buffer around until
// the container is removed.
func drainContainerLogs(ctx context.Context, cli *client.Client, containerID string, sink LogSink) {
	reader, err := cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Timestamps: false,
	})
	if err != nil {
		return
	}
	defer reader.Close()
	streamDockerLogs(reader, sink)
}

// streamDockerLogs demuxes Docker log output and sends lines to the sink.
func streamDockerLogs(reader io.ReadCloser, sink LogSink) {
	defer reader.Close()

	// Docker multiplexed output: use stdcopy to demux
	stdoutPR, stdoutPW := io.Pipe()
	stderrPR, stderrPW := io.Pipe()

	go func() {
		_, _ = stdcopy.StdCopy(stdoutPW, stderrPW, reader)
		_ = stdoutPW.Close()
		_ = stderrPW.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	scanStream := func(r io.Reader, stream string) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			sink.WriteLog(LogLine{
				Stream:    stream,
				Text:      scanner.Text(),
				Timestamp: time.Now(),
			})
		}
	}

	go scanStream(stdoutPR, "stdout")
	go scanStream(stderrPR, "stderr")

	wg.Wait()
}

// ResolveLocalImage maps pull-through-cache coordinates back to their upstream
// Docker Hub images for local execution. Cloud backends can resolve
// "alpine:latest" to cloud-specific private registry caches:
//   - GCP AR: "us-central1-docker.pkg.dev/project/docker-hub/library/alpine:latest"
//   - AWS ECR: "123456789.dkr.ecr.eu-west-1.amazonaws.com/alpine:latest"
//   - Azure ACR: "myacr.azurecr.io/library/alpine:latest"
//
// Public registry coordinates are already directly pullable by the local
// container engine and remain unchanged.
func ResolveLocalImage(image string) string {
	// GCP Artifact Registry pull-through cache
	if strings.Contains(image, "-docker.pkg.dev/") && strings.Contains(image, "/docker-hub/") {
		idx := strings.Index(image, "/docker-hub/")
		dockerPath := image[idx+len("/docker-hub/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	// AWS ECR pull-through cache. Strip docker-hub/ first, THEN
	// library/ — the URI is always `<acct>.dkr.ecr.<region>.amazonaws.com/docker-hub/library/<name>`
	// for docker-hub pull-through cache hits, so reversing the order
	// would leave `library/<name>` stuck to the front.
	if strings.Contains(image, ".dkr.ecr.") && strings.Contains(image, ".amazonaws.com/") {
		idx := strings.Index(image, ".amazonaws.com/")
		dockerPath := image[idx+len(".amazonaws.com/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "docker-hub/")
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	// Azure ACR
	if strings.Contains(image, ".azurecr.io/") {
		idx := strings.Index(image, ".azurecr.io/")
		dockerPath := image[idx+len(".azurecr.io/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	return image
}

// EnsureDockerNetwork creates a user-defined Docker network with the
// given name if it doesn't exist. Returns the network ID (existing or
// newly created). Used by the Cloud Map simulator to back each private
// DNS namespace with a real Docker network so cross-container DNS works
// via Docker's embedded DNS resolver.
func EnsureDockerNetwork(name string) (string, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("creating a container network"); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Idempotent: return existing network if present.
	if existing, err := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
		return existing.Network.ID, nil
	}
	resp, err := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: simulatorLabels(nil),
	})
	if err != nil {
		return "", fmt.Errorf("network create %s: %w", name, err)
	}
	return resp.ID, nil
}

// vpcBridgePool is the reserved host-side pool VPC bridge subnets are carved
// from. A VPC's Docker network never uses the VPC's own CIDR: an AWS CIDR is
// private to its VPC — two live VPCs may legally share one — while a host
// bridge subnet is exclusive, so the bridge subnet comes from this pool and
// the VPC's addressing rides on top as secondary elastic-network-interface
// addresses (ContainerConfig.ENIAddress). Each VPC network takes one /24: the
// bridge carries only one primary address per running workload plus the
// gateway (the VPC's own addressing needs never touch bridge IPAM), giving
// 253 concurrent workloads per VPC and 256 concurrent VPC networks per host.
// 10.213.0.0/16 sits outside the container runtimes' first auto-assignment
// picks (Docker 172.17+/16 and 192.168.0.0/16 slices, Podman 10.88.0.0/16 and
// netavark's low 10.89+ slices), outside the ECS helper-VPC test range
// (10.225–10.249), and outside the fixed CIDRs the simulator suites use; any
// slice something else on the host does hold is skipped dynamically.
var vpcBridgePool = netip.MustParsePrefix("10.213.0.0/16")

// vpcBridgeSubnet returns pool slice index (0–255) as a /24 prefix.
func vpcBridgeSubnet(index int) netip.Prefix {
	addr := vpcBridgePool.Addr().As4()
	addr[2] = byte(index)
	return netip.PrefixFrom(netip.AddrFrom4(addr), 24)
}

var vpcNetworkMu sync.Mutex

// EnsureVPCNetwork creates (idempotently) the user-defined bridge network
// backing a VPC. The bridge subnet is allocated from vpcBridgePool, never
// from the VPC's CIDR, so two live VPCs sharing a CIDR coexist: a host
// subnet's exclusivity stays a host concern, invisible in the AWS surface,
// while workloads own their ENI address as a secondary on the shared bridge
// (ContainerConfig.ENIAddress) and DescribeTasks/task metadata keep reporting
// that real, same-VPC-routable address.
//
// The allocator's state is derived, never stored: the scan starts at a
// name-derived slice (so concurrent simulator processes rarely contend for
// the same slice), skips every subnet a network on the host already holds —
// which is also exactly what makes a simulator restart safe — and frees a
// slice only when a dead simulator run's leftover holds it. Returns the
// network ID.
func EnsureVPCNetwork(name string) (string, error) {
	vpcNetworkMu.Lock()
	defer vpcNetworkMu.Unlock()
	cli := DockerClient()
	if err := RequireContainerRuntime("creating a VPC network"); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if existing, err := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
		return existing.Network.ID, nil
	}
	inUse, err := hostSubnetsInUse(ctx, cli)
	if err != nil {
		return "", fmt.Errorf("vpc network create %s: list host subnets: %w", name, err)
	}
	const slices = 256
	start := int(vpcNetworkNameHash(name) % slices)
	var lastErr error
	for n := 0; n < slices; n++ {
		candidate := vpcBridgeSubnet((start + n) % slices)
		if prefixOverlapsAny(inUse, candidate) {
			// The slice may be held by a network an earlier simulator left
			// behind: a VPC network is named for its VPC, so a later run never
			// finds it by name, and without a reclaim the pool would silt up
			// with slices nothing can use. Reclaiming frees the slice without
			// touching anything live; a slice held by a live or foreign
			// network is simply skipped.
			if !reclaimOrphanedSubnet(ctx, cli, candidate.String()) {
				continue
			}
		}
		resp, createErr := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver: "bridge",
			IPAM: &network.IPAM{
				Config: []network.IPAMConfig{{Subnet: candidate}},
			},
			Labels: simulatorLabels(map[string]string{"sockerless-sim-vpc": name}),
		})
		if createErr == nil {
			return resp.ID, nil
		}
		if !subnetInUseError(createErr) {
			return "", fmt.Errorf("vpc network create %s (%s): %w", name, candidate, createErr)
		}
		// Lost the slice to a concurrent create between the scan and this
		// call — the next slice is as good.
		lastErr = createErr
	}
	if lastErr != nil {
		return "", fmt.Errorf("vpc network create %s: reserved bridge pool %s exhausted: %w", name, vpcBridgePool, lastErr)
	}
	return "", fmt.Errorf("vpc network create %s: reserved bridge pool %s exhausted", name, vpcBridgePool)
}

// vpcNetworkNameHash spreads VPC networks across the pool so simulator
// processes sharing a host start their scans at different slices.
func vpcNetworkNameHash(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32()
}

// hostSubnetsInUse returns every IPv4 subnet any Docker network on the host
// holds, whoever created it. The live networks are the allocator's only
// ledger: a restarted simulator re-derives allocation state from them, and a
// removed network returns its slice to the pool by ceasing to exist.
func hostSubnetsInUse(ctx context.Context, cli *client.Client) ([]netip.Prefix, error) {
	nets, err := cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	var used []netip.Prefix
	for _, n := range nets.Items {
		for _, cfg := range n.IPAM.Config {
			if cfg.Subnet.IsValid() {
				used = append(used, cfg.Subnet)
			}
		}
	}
	return used, nil
}

func prefixOverlapsAny(used []netip.Prefix, candidate netip.Prefix) bool {
	for _, p := range used {
		if p.Overlaps(candidate) {
			return true
		}
	}
	return false
}

// subnetInUseError classifies a network-create failure as "this subnet is
// taken": netavark reports "subnet … is already used on the host or by
// another config", Docker "Pool overlaps with other one on this address
// space". Anything else is a real daemon failure the caller must surface.
func subnetInUseError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already used") || strings.Contains(msg, "overlap")
}

// reclaimOrphanedSubnet removes simulator networks holding the given subnet
// that no container is attached to and that another simulator run created,
// reporting whether it removed any.
//
// All the conditions are load-bearing. The simulator label keeps the sweep
// inside networks this project made. The exact subnet keeps it to the one
// blocking the caller. No attached containers means nothing is using it, and a
// different run id means the process that made it is not this one — together,
// a network that only a dead run could still own. A live run's own empty
// network is deliberately left alone: the pool allocator skips its slice and
// takes another, so two live simulators never fight over a subnet.
func reclaimOrphanedSubnet(ctx context.Context, cli *client.Client, cidr string) bool {
	nets, err := cli.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "sockerless-sim=true"),
	})
	if err != nil {
		return false
	}
	reclaimed := false
	for _, n := range nets.Items {
		if n.Labels["sockerless-sim-run"] == simulatorRunID {
			continue
		}
		details, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil || len(details.Network.Containers) > 0 {
			continue
		}
		holds := false
		for _, cfg := range details.Network.IPAM.Config {
			if cfg.Subnet.String() == cidr {
				holds = true
				break
			}
		}
		if !holds {
			continue
		}
		if _, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err == nil {
			reclaimed = true
		}
	}
	return reclaimed
}

// RemoveDockerNetwork removes a simulator-managed Docker network if
// it exists. Errors are returned so callers can log them; idempotent
// for a missing network.
func RemoveDockerNetwork(name string) error {
	cli := DockerClient()
	if cli == nil {
		return nil
	}
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, inspectErr := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if inspectErr != nil {
			cancel()
			return nil // already gone
		}
		_, lastErr = cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
		cancel()
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(250 * time.Millisecond)
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

// DisconnectContainerFromNetwork removes a running container from a
// Docker network. Idempotent for already-disconnected containers.
func DisconnectContainerFromNetwork(containerName, networkName string) error {
	cli := DockerClient()
	if cli == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := cli.NetworkDisconnect(ctx, networkName, client.NetworkDisconnectOptions{Container: containerName, Force: true})
	return err
}

// DisconnectContainerNetworks detaches a running container from every Docker
// network it currently has. The container keeps its process namespace alive,
// which lets callers attach their own network fabric afterward.
func DisconnectContainerNetworks(containerID string) error {
	cli := DockerClient()
	if err := RequireContainerRuntime("detaching a container from its networks"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	for networkName := range inspected.Container.NetworkSettings.Networks {
		if _, err := cli.NetworkDisconnect(ctx, networkName, client.NetworkDisconnectOptions{Container: containerID, Force: true}); err != nil {
			return err
		}
	}
	return nil
}

type HostEntry struct {
	IP   string
	Name string
}

// SyncContainerHostEntries rewrites a simulator-managed block in a container's
// /etc/hosts. Docker exposes the backing hosts file path in ContainerInspect;
// updating it gives netns-backed tasks real libc name resolution without
// attaching another Docker network to the namespace.
func SyncContainerHostEntries(containerName, marker string, entries []HostEntry) error {
	cli := DockerClient()
	if err := RequireContainerRuntime("writing a container's host entries"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	info := inspected.Container
	if info.HostsPath == "" {
		return fmt.Errorf("container %s has no hosts path", containerName)
	}
	content, err := os.ReadFile(info.HostsPath)
	if err != nil {
		return err
	}
	markerText := "# " + marker
	var kept []string
	for _, line := range strings.Split(string(content), "\n") {
		if strings.Contains(line, markerText) {
			continue
		}
		kept = append(kept, line)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].IP < entries[j].IP
		}
		return entries[i].Name < entries[j].Name
	})
	for _, entry := range entries {
		ip := strings.TrimSpace(entry.IP)
		name := strings.TrimSpace(entry.Name)
		if ip == "" || name == "" {
			continue
		}
		kept = append(kept, fmt.Sprintf("%s\t%s\t%s", ip, name, markerText))
	}
	next := strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n"
	return os.WriteFile(info.HostsPath, []byte(next), 0644)
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
