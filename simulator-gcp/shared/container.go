package simulator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	NetworkMode  string            // Docker network mode (e.g. "container:<id>" for shared netns)
	Name         string            // container name (optional, auto-generated if empty)
	Tty          bool              // allocate a pseudo-TTY
	OpenStdin    bool              // keep stdin open
	Binds        []string          // bind mounts (e.g., "vol:/path")
	ExtraHosts   []string          // --add-host entries (e.g., "host.docker.internal:host-gateway")

	// Sandbox enforces per-platform capability + permission parity
	// with the real cloud. Zero value = no enforcement;
	// every production sim caller must set this.
	Sandbox SandboxProfile
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
func InitDocker(provider string) *client.Client {
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
	if err := startContainerReaper(provider); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	return dockerClient
}

// DockerClient returns the shared Docker client. InitDocker must have been called first.
func DockerClient() *client.Client {
	return dockerClient
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

// HTTPContainerConfig describes a container that exposes an HTTP
// listener on its $PORT (default 8080), to be invoked over HTTP from
// the host. Used by the FaaS-bootstrap invocation flow (Cloud
// Functions Gen2, Lambda container mode) where the function image's
// ENTRYPOINT serves HTTP and the platform POSTs requests to it.
type HTTPContainerConfig struct {
	Image        string            // overlay image (must be locally available)
	Architecture string            // OS/arch (e.g. "linux/arm64"); workload's spec — never derived from host
	HostPort     int               // host port to publish container's :8080 to
	Command      []string          // entrypoint override (empty = use image default)
	Args         []string          // command/args (empty = use image default)
	Env          map[string]string // env vars (must include PORT to match the published port-target)
	Name         string            // container name (optional, auto-generated if empty)
	Labels       map[string]string // container labels for tracking
	Binds        []string          // bind mounts (e.g., "vol:/path")
	ExtraHosts   []string          // --add-host entries (e.g., "metadata.google.internal:host-gateway")

	// Sandbox: per-platform capability + permission parity.
	Sandbox SandboxProfile
}

// StartHTTPContainer starts a container detached, with its container-
// internal :8080 published to the requested host port. Returns the
// container ID; the caller is responsible for stopping/removing the
// container via StopAndRemoveContainer when done. The image must
// already be present in the local docker daemon (no pull-from-network
// fallback — the sim runs entirely on local images so caller failures
// are surfaced as real "image not present" errors instead of hanging
// on registry-pull retries).
func StartHTTPContainer(ctx context.Context, cfg HTTPContainerConfig) (string, error) {
	cli := DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}

	// Verify the image is local — surface a clear error rather than
	// blocking on a pull from a registry that won't accept us.
	// Resolve to the image ID for ContainerCreate: Podman's docker-compat API
	// resolves a short name on inspect but not on create, so a locally-built
	// "name:tag" inspects fine yet create reports "no such image". The ID is
	// unambiguous on both Docker and Podman.
	inspect, _, err := cli.ImageInspectWithRaw(ctx, cfg.Image)
	if err != nil {
		return "", fmt.Errorf("image %q not present locally: %w", cfg.Image, err)
	}
	imageRef := cfg.Image
	if inspect.ID != "" {
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

	exposedPort := nat.Port("8080/tcp")
	containerCfg := &container.Config{
		Image:        imageRef,
		Env:          env,
		Labels:       labels,
		ExposedPorts: nat.PortSet{exposedPort: struct{}{}},
	}
	if len(cfg.Command) > 0 {
		containerCfg.Entrypoint = cfg.Command
	}
	if len(cfg.Args) > 0 {
		containerCfg.Cmd = cfg.Args
	}
	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			exposedPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: strconv.Itoa(cfg.HostPort)}},
		},
		Binds:      cfg.Binds,
		ExtraHosts: cfg.ExtraHosts,
	}
	// Sandbox parity.
	if err := cfg.Sandbox.Apply(hostCfg, containerCfg); err != nil {
		return "", fmt.Errorf("sandbox enforce: %w", err)
	}

	platform, err := parsePlatform(cfg.Architecture)
	if err != nil {
		return "", err
	}
	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, platform, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("container start: %w", err)
	}

	managedContainers.Store(resp.ID, true)
	return resp.ID, nil
}

// StopAndRemoveContainer stops and removes the container. Idempotent
// — best-effort cleanup; errors are silenced because callers
// typically defer this and there's nothing useful to report.
func StopAndRemoveContainer(containerID string) {
	cli := DockerClient()
	if cli == nil {
		return
	}
	managedContainers.Delete(containerID)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	timeout := 5
	_ = cli.ContainerStop(stopCtx, containerID, container.StopOptions{Timeout: &timeout})
	_ = cli.ContainerRemove(stopCtx, containerID, container.RemoveOptions{Force: true})
}

// StreamContainerLogs follows the container's stdout/stderr and
// writes each line to the provided sink. Returns when the container
// exits or `ctx` is cancelled. Used by the FaaS-bootstrap invocation
// flow to forward bootstrap + user-subprocess output into Cloud
// Logging while the invocation is in flight.
func StreamContainerLogs(ctx context.Context, containerID string, sink LogSink) {
	cli := DockerClient()
	if cli == nil {
		return
	}
	reader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	})
	if err != nil {
		return
	}
	streamDockerLogs(reader, sink)
}

// StopContainer stops a running container by ID.
func StopContainer(containerID string) {
	if dockerClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	timeout := 10
	_ = dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
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

	// Create by image ID — Podman's docker-compat API resolves a short name on
	// inspect/pull but not on create (locally-built "name:tag" reports "no such
	// image"). The ID is unambiguous on both Docker and Podman.
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
	if cfg.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(cfg.NetworkMode)
	}
	// Sandbox parity.
	if err := cfg.Sandbox.Apply(hostCfg, containerCfg); err != nil {
		return "", fmt.Errorf("sandbox enforce: %w", err)
	}

	var networkCfg *network.NetworkingConfig
	if cfg.Network != "" && cfg.NetworkMode == "" {
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
	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, platform, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Cleanup on start failure
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("container start: %w", err)
	}

	return resp.ID, nil
}

func waitAndCaptureLogs(ctx context.Context, cli *client.Client, containerID string, cfg ContainerConfig, sink LogSink) ProcessResult {
	startedAt := time.Now()

	// Stream logs live while the workload runs — Google Cloud Run forwards each container line to Cloud Logging as it is produced,
	// so a long-running workload accumulates logs in near-real time instead
	// of becoming visible only after it exits. The stream runs on a detached
	// context so a caller-side cancel cannot truncate it before the exit
	// drain below.
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
		timeout := 5
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = cli.ContainerStop(stopCtx, containerID, container.StopOptions{Timeout: &timeout})
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

// ResolveLocalImage maps cloud registry URIs back to Docker Hub images for local execution.
// Cloud backends resolve "alpine:latest" to cloud-specific registries:
//   - GCP AR: "us-central1-docker.pkg.dev/project/docker-hub/library/alpine:latest"
//   - AWS ECR: "123456789.dkr.ecr.eu-west-1.amazonaws.com/alpine:latest"
//   - Azure ACR: "myacr.azurecr.io/library/alpine:latest"
//
// The simulator runs containers locally where only Docker Hub images exist,
// so these URIs must be resolved back to their original form.
func ResolveLocalImage(image string) string {
	// GCP Artifact Registry pull-through cache
	if strings.Contains(image, "-docker.pkg.dev/") && strings.Contains(image, "/docker-hub/") {
		idx := strings.Index(image, "/docker-hub/")
		dockerPath := image[idx+len("/docker-hub/"):]
		dockerPath = strings.TrimPrefix(dockerPath, "library/")
		return dockerPath
	}
	// AWS ECR pull-through cache. Strip docker-hub/ first, then library/.
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

// RemoveDockerNetwork removes a simulator-managed Docker network if
// it exists. Errors are returned so callers can log them; idempotent
// for a missing network.
func RemoveDockerNetwork(name string) error {
	cli := DockerClient()
	if cli == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cli.NetworkInspect(ctx, name, network.InspectOptions{}); err != nil {
		return nil // already gone
	}
	return cli.NetworkRemove(ctx, name)
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

// FindContainerByIP inspects all simulator-managed Docker containers
// and returns the name of the one whose bridge/default-network IP
// matches `ip`. Used by the Cloud DNS slice: when an A record is
// added to a private zone, the simulator finds the container whose
// IP matches the record's Rrdatas[0] and connects it to the zone's
// Docker network with the record name as DNS alias. Returns "" if
// no managed container has the given IP.
func FindContainerByIP(ip string) string {
	if dockerClient == nil || ip == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := dockerClient.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "sockerless-sim=true")),
	})
	if err != nil {
		return ""
	}
	for _, c := range list {
		insp, err := dockerClient.ContainerInspect(ctx, c.ID)
		if err != nil || insp.NetworkSettings == nil {
			continue
		}
		for _, ep := range insp.NetworkSettings.Networks {
			if ep != nil && ep.IPAddress == ip {
				if len(c.Names) > 0 {
					return strings.TrimPrefix(c.Names[0], "/")
				}
				return c.ID
			}
		}
	}
	return ""
}

// ContainerIPv4 returns a running container's primary IPv4 address on its
// docker network, or "" if unavailable. A sim running inside a harness
// container reaches a workload by this bridge IP (routable container-to-
// container) rather than a host-published port, which binds the host's
// loopback — not the sim container's.
func ContainerIPv4(id string) string {
	if dockerClient == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	insp, err := dockerClient.ContainerInspect(ctx, id)
	if err != nil || insp.NetworkSettings == nil {
		return ""
	}
	if insp.NetworkSettings.IPAddress != "" {
		return insp.NetworkSettings.IPAddress
	}
	for _, ep := range insp.NetworkSettings.Networks {
		if ep != nil && ep.IPAddress != "" {
			return ep.IPAddress
		}
	}
	return ""
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
