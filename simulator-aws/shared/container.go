package simulator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
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
	IPAddress    string            // static IPv4 within Network (optional; the VPC ENI IP)
	NetworkMode  string            // Docker network mode (e.g. "container:<id>" for shared netns)
	Name         string            // container name (optional, auto-generated if empty)
	Tty          bool              // allocate a pseudo-TTY
	OpenStdin    bool              // keep stdin open
	Binds        []string          // bind mounts (e.g., "vol:/path")
	ExtraHosts   []string          // --add-host entries (e.g., "host.docker.internal:host-gateway")
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
}

// ContainerHandle manages a running container.
type ContainerHandle struct {
	ContainerID string
	cancel      context.CancelFunc
	done        <-chan ProcessResult
	cli         *client.Client
}

// Wait blocks until the container exits.
func (h *ContainerHandle) Wait() ProcessResult { return <-h.done }

// Cancel stops and removes the container.
func (h *ContainerHandle) Cancel() { h.cancel() }

// dockerClient is the shared Docker client. Initialized once at startup.
var (
	dockerClient     *client.Client
	dockerClientOnce sync.Once
	dockerClientErr  error
)

// InitDocker initializes the shared Docker client and verifies connectivity.
// Must be called at simulator startup. Fatally exits if Docker is not available.
func InitDocker(provider string, preserveWorkloads bool, stateDir string) *client.Client {
	configureSimulatorIdentity(provider, stateDir)
	dockerClientOnce.Do(func() {
		dockerClient, dockerClientErr = client.NewClientWithOpts(
			client.FromEnv,
			client.WithAPIVersionNegotiation(),
		)
		if dockerClientErr != nil {
			return
		}
		// Verify connectivity
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, dockerClientErr = dockerClient.Ping(ctx)
	})
	if dockerClientErr != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Docker/Podman not available: %v\n", dockerClientErr)
		fmt.Fprintf(os.Stderr, "Simulators require Docker or Podman for workload execution. Install Docker/Podman, or set SIM_RUNTIME=process only for explicit API-only runs that do not execute workloads.\n")
		os.Exit(1)
	}
	if !preserveWorkloads {
		if err := startContainerReaper(provider); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
			os.Exit(1)
		}
	}
	return dockerClient
}

// DockerClient returns the shared Docker client. InitDocker must have been called first.
func DockerClient() *client.Client {
	return dockerClient
}

// ContainerPID returns the host PID of a running container's main process, used
// to plumb a veth into the container's network namespace (the netns VPC fabric).
func ContainerPID(containerID string) (int, error) {
	cli := DockerClient()
	if cli == nil {
		return 0, fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, err
	}
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
		_ = dockerClient.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
		_ = dockerClient.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		return true
	})

	nets, err := dockerClient.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "sockerless-sim-run="+simulatorRunID)),
	})
	if err == nil {
		for _, n := range nets {
			_ = dockerClient.NetworkRemove(ctx, n.ID)
		}
	}
}

// StartContainer pulls the image (if needed), creates and starts a container.
// Returns a ContainerHandle immediately. Call handle.Wait() to block until exit.
// Stdout/stderr are streamed to the LogSink.
func StartContainer(cfg ContainerConfig, sink LogSink) *ContainerHandle {
	resultCh := make(chan ProcessResult, 1)

	cli := DockerClient()
	if cli == nil {
		resultCh <- ProcessResult{
			ExitCode:  -1,
			StartedAt: time.Now(),
			StoppedAt: time.Now(),
			Error:     fmt.Errorf("docker client not initialized"),
		}
		return &ContainerHandle{cancel: func() {}, done: resultCh, cli: cli}
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		result := runContainer(ctx, cli, cfg, sink)
		resultCh <- result
	}()

	// Wait briefly for the container to start so we can capture the ID
	// The ContainerHandle is returned immediately; the goroutine runs in background
	return &ContainerHandle{cancel: cancel, done: resultCh, cli: cli}
}

// StartContainerSync is like StartContainer but returns the handle with ContainerID populated.
// Blocks until the container is created and started (but not until it exits).
func StartContainerSync(cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	cli := DockerClient()
	if cli == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan ProcessResult, 1)

	containerID, err := createAndStartContainer(ctx, cli, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	managedContainers.Store(containerID, true)

	// Stream logs and wait for exit in background
	go func() {
		result := waitAndCaptureLogs(ctx, cli, containerID, cfg, sink)
		managedContainers.Delete(containerID)
		// Remove container after exit
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rmCancel()
		_ = cli.ContainerRemove(rmCtx, containerID, container.RemoveOptions{Force: true})
		resultCh <- result
	}()

	handle := &ContainerHandle{
		ContainerID: containerID,
		cancel:      cancel,
		done:        resultCh,
		cli:         cli,
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
	if dockerClient == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}
	labelFilters := filters.NewArgs()
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
	summaries, err := dockerClient.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: labelFilters,
	})
	if err != nil {
		return nil, err
	}
	found := make([]ExistingContainer, 0, len(summaries))
	for _, summary := range summaries {
		inspection, err := dockerClient.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
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
					entry.PublishedPorts[containerPort.Int()] = publicPort
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
	if dockerClient == nil {
		return fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	timeout := 5
	_ = dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	return dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

// StartExistingContainer resumes an exited persistent workload container
// without replacing it, preserving its filesystem, mounts, identity, and port
// bindings.
func StartExistingContainer(containerID string) error {
	if dockerClient == nil {
		return fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return dockerClient.ContainerStart(ctx, containerID, container.StartOptions{})
}

// AdoptContainer attaches lifecycle observation to a container created by an
// earlier persistent simulator process. It never restarts the workload; callers
// decide whether an exited cloud workload should remain terminal or resume.
func AdoptContainer(containerID string, cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	if dockerClient == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan ProcessResult, 1)
	managedContainers.Store(containerID, true)
	go func() {
		result := waitAndCaptureLogs(ctx, dockerClient, containerID, cfg, sink)
		managedContainers.Delete(containerID)
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		_ = dockerClient.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true})
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
	_ = dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// WaitContainerRemoved waits until the runtime no longer owns containerID.
// Lifecycle callers use this after requesting a stop when the cloud resource
// must not report its terminal state while its network endpoint or mounts are
// still present.
func WaitContainerRemoved(containerID string, timeout time.Duration) error {
	if dockerClient == nil {
		return fmt.Errorf("docker client not initialized")
	}
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := dockerClient.ContainerInspect(ctx, containerID)
		cancel()
		if containerNotFoundError(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect container %s while waiting for removal: %w", containerID, err)
		}
		if !time.Now().Before(deadline) {
			removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			removeErr := dockerClient.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true})
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
	if client.IsErrNotFound(err) {
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
	if dockerClient == nil {
		return fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return dockerClient.VolumeRemove(ctx, name, true)
}

func runContainer(ctx context.Context, cli *client.Client, cfg ContainerConfig, sink LogSink) ProcessResult {
	containerID, err := createAndStartContainer(ctx, cli, cfg)
	if err != nil {
		return ProcessResult{
			ExitCode:  -1,
			StartedAt: time.Now(),
			StoppedAt: time.Now(),
			Error:     err,
		}
	}

	managedContainers.Store(containerID, true)
	defer func() {
		managedContainers.Delete(containerID)
		// Remove container after exit
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rmCancel()
		_ = cli.ContainerRemove(rmCtx, containerID, container.RemoveOptions{Force: true})
	}()

	return waitAndCaptureLogs(ctx, cli, containerID, cfg, sink)
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
	backoff := 2 * time.Second
	const maxAttempts = 5
	for attempt := 1; ; attempt++ {
		reader, err := cli.ImagePull(ctx, imageName, image.PullOptions{Platform: platform})
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
	return strings.Contains(msg, "toomanyrequests") ||
		strings.Contains(msg, "rate exceeded") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "status code 429") ||
		strings.Contains(msg, "status code 503") ||
		strings.Contains(msg, "service unavailable")
}

func createAndStartContainer(ctx context.Context, cli *client.Client, cfg ContainerConfig) (string, error) {
	// Pull image
	pullPolicy := os.Getenv("SIM_PULL_POLICY")
	if pullPolicy == "" {
		pullPolicy = "if-not-present"
	}

	shouldPull := pullPolicy == "always"
	if pullPolicy == "if-not-present" {
		_, _, err := cli.ImageInspectWithRaw(ctx, cfg.Image)
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
	if inspect, _, err := cli.ImageInspectWithRaw(ctx, cfg.Image); err == nil && inspect.ID != "" {
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
		containerCfg.ExposedPorts = nat.PortSet{}
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
		DNS:        cfg.DNS,
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
		port, err := nat.NewPort("tcp", strconv.Itoa(containerPort))
		if err != nil {
			return "", fmt.Errorf("publish port %d: %w", containerPort, err)
		}
		containerCfg.ExposedPorts[port] = struct{}{}
		if hostCfg.PortBindings == nil {
			hostCfg.PortBindings = nat.PortMap{}
		}
		publicPort := ""
		if hostPort > 0 {
			publicPort = strconv.Itoa(hostPort)
		}
		hostCfg.PortBindings[port] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: publicPort}}
	}

	// Enforce sandbox parity with the real cloud platform. Empty profile
	// means no enforcement; non-empty must apply cleanly so caller errors
	// fail loudly.
	if err := cfg.Sandbox.Apply(hostCfg, containerCfg); err != nil {
		return "", fmt.Errorf("sandbox enforce: %w", err)
	}

	var networkCfg *network.NetworkingConfig
	if cfg.Network != "" && cfg.NetworkMode == "" {
		endpoint := &network.EndpointSettings{}
		if cfg.IPAddress != "" {
			// Pin the container to its VPC ENI IP so DescribeTasks's
			// privateIPv4Address is the container's real, routable address.
			endpoint.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: cfg.IPAddress}
		}
		networkCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.Network: endpoint,
			},
		}
	}

	platform, err := parsePlatform(cfg.Architecture)
	if err != nil {
		return "", err
	}
	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, platform, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Cleanup on start failure
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		if hint := MissingNetfilterTableHint(err); hint != "" {
			return "", fmt.Errorf("container start: %w (%s)", err, hint)
		}
		return "", fmt.Errorf("container start: %w", err)
	}

	return resp.ID, nil
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
		_ = cli.ContainerKill(killCtx, containerID, "KILL")
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
				_ = cli.ContainerStop(stopCtx, containerID, container.StopOptions{Timeout: &timeout})
			}
		}()
	}

	// Wait for container to exit.
	var result ProcessResult
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
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
// forwards the rest. Used from the single-goroutine post-exit drain.
type lineSkippingSink struct {
	sink LogSink
	skip map[string]int
}

func (s *lineSkippingSink) WriteLog(line LogLine) {
	if s.skip[line.Stream] > 0 {
		s.skip[line.Stream]--
		return
	}
	s.sink.WriteLog(line)
}

// followContainerLogs streams the container's demuxed log lines to sink as
// they are produced; it returns when the container exits (Docker closes the
// follow stream) or ctx is cancelled.
func followContainerLogs(ctx context.Context, cli *client.Client, containerID string, sink LogSink) {
	reader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
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
	reader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
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
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Idempotent: return existing network if present.
	if existing, err := cli.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return existing.ID, nil
	}
	resp, err := cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: simulatorLabels(nil),
	})
	if err != nil {
		return "", fmt.Errorf("network create %s: %w", name, err)
	}
	return resp.ID, nil
}

// EnsureVPCNetwork creates (idempotently) a user-defined bridge network whose
// IPAM subnet is the VPC CIDR. Each VPC becomes a genuinely isolated L3 network:
// the bridge enforces the VPC's implicit local route (intra-VPC routability) and
// isolation across VPCs, and ECS tasks pinned to their ENI IP (within the CIDR)
// expose that real, routable address via DescribeTasks. Returns the network ID.
var vpcNetworkMu sync.Mutex

func EnsureVPCNetwork(name, cidr string) (string, error) {
	vpcNetworkMu.Lock()
	defer vpcNetworkMu.Unlock()
	cli := DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if existing, err := cli.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return existing.ID, nil
	}
	create := func() (string, error) {
		resp, err := cli.NetworkCreate(ctx, name, network.CreateOptions{
			Driver: "bridge",
			IPAM: &network.IPAM{
				Config: []network.IPAMConfig{{Subnet: cidr}},
			},
			Labels: simulatorLabels(map[string]string{"sockerless-sim-vpc": name}),
		})
		if err != nil {
			return "", err
		}
		return resp.ID, nil
	}
	id, err := create()
	if err == nil {
		return id, nil
	}
	// The subnet may be held by a network an earlier simulator left behind. A
	// VPC network is named for its VPC, so a run with a different VPC id does
	// not find the old network by name and then cannot create its own: the
	// subnet is taken under a name nothing looks for. Reclaiming it frees the
	// subnet without touching anything live, and then the create is retried
	// once.
	if reclaimOrphanedSubnet(ctx, cli, cidr) {
		if id, retryErr := create(); retryErr == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("vpc network create %s (%s): %w", name, cidr, err)
}

// reclaimOrphanedSubnet removes simulator networks holding the given subnet
// that no container is attached to and that another simulator run created,
// reporting whether it removed any.
//
// All three conditions are load-bearing. The simulator label keeps the sweep
// inside networks this project made. The exact subnet keeps it to the one
// blocking the caller. No attached containers means nothing is using it, and a
// different run id means the process that made it is not this one — together,
// a network that only a dead run could still own. A live run's own empty
// network is deliberately left alone, which is why two simultaneous VPCs
// sharing a CIDR still conflict; that is a separate problem, and silently
// deleting a peer's network would not solve it.
func reclaimOrphanedSubnet(ctx context.Context, cli *client.Client, cidr string) bool {
	nets, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "sockerless-sim=true")),
	})
	if err != nil {
		return false
	}
	reclaimed := false
	for _, n := range nets {
		if n.Labels["sockerless-sim-run"] == simulatorRunID {
			continue
		}
		details, err := cli.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil || len(details.Containers) > 0 {
			continue
		}
		holds := false
		for _, cfg := range details.IPAM.Config {
			if cfg.Subnet == cidr {
				holds = true
				break
			}
		}
		if !holds {
			continue
		}
		if cli.NetworkRemove(ctx, n.ID) == nil {
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
		_, inspectErr := cli.NetworkInspect(ctx, name, network.InspectOptions{})
		if inspectErr != nil {
			cancel()
			return nil // already gone
		}
		lastErr = cli.NetworkRemove(ctx, name)
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
	if cli == nil {
		return fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cli.NetworkConnect(ctx, networkName, containerName, &network.EndpointSettings{
		Aliases: aliases,
	})
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
	return cli.NetworkDisconnect(ctx, networkName, containerName, true)
}

// DisconnectContainerNetworks detaches a running container from every Docker
// network it currently has. The container keeps its process namespace alive,
// which lets callers attach their own network fabric afterward.
func DisconnectContainerNetworks(containerID string) error {
	cli := DockerClient()
	if cli == nil {
		return fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}
	for networkName := range info.NetworkSettings.Networks {
		if err := cli.NetworkDisconnect(ctx, networkName, containerID, true); err != nil {
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
	if cli == nil {
		return fmt.Errorf("docker client not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return err
	}
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
	info, err := dockerClient.ServerVersion(ctx)
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
	if dockerClient == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	networkName := "bridge"
	if strings.Contains(strings.ToLower(RuntimeInfo()), "podman") {
		networkName = "podman"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := dockerClient.NetworkInspect(ctx, networkName, network.InspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect default container network %s: %w", networkName, err)
	}
	if info.IPAM.Config == nil {
		return "", fmt.Errorf("default container network %s has no IPAM configuration", networkName)
	}
	for _, config := range info.IPAM.Config {
		ip := net.ParseIP(strings.TrimSpace(config.Gateway))
		if ip == nil || ip.To4() == nil {
			continue
		}
		return ip.To4().String(), nil
	}
	return "", fmt.Errorf("default container network %s has no IPv4 gateway", networkName)
}
