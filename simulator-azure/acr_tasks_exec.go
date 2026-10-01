package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/big"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// acrTaskRun carries out a task file's steps on the build host's container
// engine, the way the ACR Tasks run engine does on a run's agent: every step
// shares one volume mounted at /workspace and holding the run's source, cmd
// steps run containers on a network of the run's own where each answers to
// its step ID, build steps build with the source as their context, and push
// steps push to the registries the run holds credentials for.
type acrTaskRun struct {
	runID     string
	log       io.Writer
	engine    *client.Client
	dockerEnv []string
	platform  string
	network   string
	volume    string
	// registryHost is the run's registry login server without its port;
	// registryAuth is the engine API credential the run holds for it.
	registryHost string
	registryAuth string

	mu          sync.Mutex
	workDir     string
	volumeReady bool
	containers  []acrStepContainer
	built       []string
	pushed      []string
}

type acrStepContainer struct {
	name string
	keep bool
}

func (x *acrTaskRun) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(x.log, time.Now().UTC().Format("2006/01/02 15:04:05 ")+format+"\n", args...)
}

// credentialFor is the credential the engine presents to the registry an
// image names: the run's own for its registry, none for any other.
func (x *acrTaskRun) credentialFor(image string) string {
	if acrBareHost(acrImageDomain(image)) == x.registryHost {
		return x.registryAuth
	}
	return ""
}

// acrImageDomain is the registry host an image reference names, by the Docker
// reference grammar: the first path component when it holds a dot or a port
// or is localhost, Docker Hub otherwise.
func acrImageDomain(image string) string {
	first, _, ok := strings.Cut(image, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return "docker.io"
}

// writeProgress writes an engine pull or push progress stream to the run log
// the way the docker CLI prints one to a non-terminal, and returns the error
// the stream ends with.
func (x *acrTaskRun) writeProgress(messages iter.Seq2[jsonstream.Message, error]) error {
	for m, err := range messages {
		if err != nil {
			return err
		}
		if m.Error != nil {
			return m.Error
		}
		switch {
		case m.Stream != "":
			_, _ = io.WriteString(x.log, m.Stream)
		case m.Status == "" || m.Progress != nil:
		case m.ID != "":
			_, _ = fmt.Fprintf(x.log, "%s: %s\n", m.ID, m.Status)
		default:
			_, _ = fmt.Fprintln(x.log, m.Status)
		}
	}
	return nil
}

// acrRunTaskFile runs every step of task once its `when` dependencies have
// succeeded. A step that fails, unless it ignores errors, fails the run and
// stops the steps still running.
func acrRunTaskFile(ctx context.Context, task *acrTaskFile, x *acrTaskRun) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	if _, err := x.engine.NetworkCreate(ctx, x.network, client.NetworkCreateOptions{Driver: "bridge"}); err != nil {
		return fmt.Errorf("create the run's network: %w", err)
	}
	if _, err := x.engine.VolumeCreate(ctx, client.VolumeCreateOptions{Name: x.volume}); err != nil {
		_, _ = x.engine.NetworkRemove(context.Background(), x.network, client.NetworkRemoveOptions{})
		return fmt.Errorf("create the run's volume: %w", err)
	}
	defer x.cleanup()

	done := map[string]chan struct{}{}
	failed := map[string]error{}
	var failedMu sync.Mutex
	for _, s := range task.Steps {
		done[s.ID] = make(chan struct{})
	}
	var wg sync.WaitGroup
	for _, s := range task.Steps {
		wg.Add(1)
		go func(s *acrTaskStep) {
			defer wg.Done()
			defer close(done[s.ID])
			for _, dep := range s.deps {
				select {
				case <-done[dep]:
				case <-ctx.Done():
					return
				}
				failedMu.Lock()
				depErr := failed[dep]
				failedMu.Unlock()
				if depErr != nil {
					failedMu.Lock()
					failed[s.ID] = fmt.Errorf("step %s did not run: step %s failed", s.ID, dep)
					failedMu.Unlock()
					return
				}
			}
			if ctx.Err() != nil {
				return
			}
			err := x.runStep(ctx, s)
			if err != nil && s.IgnoreErrors {
				x.logf("Step ID: %s encountered an error: %v, but is set to ignore errors. Continuing...", s.ID, err)
				err = nil
			}
			if err != nil {
				failedMu.Lock()
				failed[s.ID] = err
				failedMu.Unlock()
				cancel(err)
			}
		}(s)
	}
	wg.Wait()
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return ctx.Err()
}

func (x *acrTaskRun) runStep(ctx context.Context, s *acrTaskStep) error {
	if s.StartDelay > 0 {
		x.logf("Waiting %d seconds before executing step ID: %s", s.StartDelay, s.ID)
		if err := acrWait(ctx, time.Duration(s.StartDelay)*time.Second); err != nil {
			return err
		}
	}
	x.logf("Executing step ID: %s. Timeout(sec): %d, Working directory: '%s', Network: '%s'",
		s.ID, s.Timeout, s.WorkingDirectory, acrTaskDefaultNetwork)
	started := time.Now()
	stepCtx, cancel := context.WithTimeoutCause(ctx, time.Duration(s.Timeout)*time.Second,
		fmt.Errorf("step %s exceeded its timeout of %d seconds", s.ID, s.Timeout))
	defer cancel()

	var err error
	switch s.kind() {
	case "cmd":
		err = x.runCmdStep(stepCtx, s)
	case "build":
		err = x.runBuildStep(stepCtx, s)
	default:
		err = x.runPushStep(stepCtx, s)
	}
	if err != nil && stepCtx.Err() != nil && ctx.Err() == nil {
		err = context.Cause(stepCtx)
	}
	status := "successful"
	if err != nil {
		status = "failed"
	}
	x.logf("Step ID: %s marked as %s (elapsed time in seconds: %f)", s.ID, status, time.Since(started).Seconds())
	return err
}

// attempts runs fn once, plus once per repeat, retrying each run that fails up
// to retries times, retryDelay seconds apart.
func attempts(ctx context.Context, s *acrTaskStep, fn func() error) error {
	for run := 0; run <= s.Repeat; run++ {
		var err error
		for try := 0; try <= s.Retries; try++ {
			if try > 0 && s.RetryDelay > 0 {
				if werr := acrWait(ctx, time.Duration(s.RetryDelay)*time.Second); werr != nil {
					return werr
				}
			}
			if err = fn(); err == nil || ctx.Err() != nil {
				break
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func acrWait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// stepWorkingDirectory resolves a step's working directory inside its
// container: /workspace unless the step names another, a relative one under
// /workspace, and the image's own when the step disables the override and
// names none.
func stepWorkingDirectory(s *acrTaskStep) string {
	wd := s.WorkingDirectory
	if s.DisableWorkingDirectoryOverride {
		return wd
	}
	if wd == "" {
		return acrTaskWorkspace
	}
	if !path.IsAbs(wd) {
		return path.Join(acrTaskWorkspace, wd)
	}
	return wd
}

// acrOCIPlatform reads an os[/architecture[/variant]] platform string.
func acrOCIPlatform(p string) *ocispec.Platform {
	if p == "" {
		return nil
	}
	parts := strings.SplitN(p, "/", 3)
	platform := &ocispec.Platform{OS: parts[0]}
	if len(parts) > 1 {
		platform.Architecture = parts[1]
	}
	if len(parts) > 2 {
		platform.Variant = parts[2]
	}
	return platform
}

// acrStepPorts reads a cmd step's `ports` (docker's
// [ip:][hostPort:]containerPort[/protocol] publish form) and `expose` into the
// ports its container exposes and binds on the host.
func acrStepPorts(publish, expose []string) (network.PortSet, network.PortMap, error) {
	exposed := network.PortSet{}
	bindings := network.PortMap{}
	for _, e := range expose {
		pr, err := network.ParsePortRange(e)
		if err != nil {
			return nil, nil, fmt.Errorf("expose %q: %w", e, err)
		}
		for p := range pr.All() {
			exposed[p] = struct{}{}
		}
	}
	for _, spec := range publish {
		rest, proto, _ := strings.Cut(spec, "/")
		var ip, host, ctr string
		if strings.HasPrefix(rest, "[") {
			addr, ports, ok := strings.Cut(rest[1:], "]:")
			if !ok {
				return nil, nil, fmt.Errorf("ports %q: unterminated IPv6 address", spec)
			}
			ip = addr
			if host, ctr, ok = strings.Cut(ports, ":"); !ok {
				return nil, nil, fmt.Errorf("ports %q: an address needs a host port and a container port", spec)
			}
		} else {
			switch parts := strings.Split(rest, ":"); len(parts) {
			case 1:
				ctr = parts[0]
			case 2:
				host, ctr = parts[0], parts[1]
			case 3:
				ip, host, ctr = parts[0], parts[1], parts[2]
			default:
				return nil, nil, fmt.Errorf("ports %q: want [ip:][hostPort:]containerPort[/protocol]", spec)
			}
		}
		ctrRange, err := network.ParsePortRange(ctr + "/" + proto)
		if err != nil {
			return nil, nil, fmt.Errorf("ports %q: %w", spec, err)
		}
		var hostIP netip.Addr
		if ip != "" {
			if hostIP, err = netip.ParseAddr(ip); err != nil {
				return nil, nil, fmt.Errorf("ports %q: %w", spec, err)
			}
		}
		var hostRange network.PortRange
		if host != "" {
			if hostRange, err = network.ParsePortRange(host); err != nil {
				return nil, nil, fmt.Errorf("ports %q: %w", spec, err)
			}
		}
		ctrCount := int(ctrRange.End()) - int(ctrRange.Start()) + 1
		hostCount := int(hostRange.End()) - int(hostRange.Start()) + 1
		if host != "" && hostCount != ctrCount && ctrCount != 1 {
			return nil, nil, fmt.Errorf("ports %q: the host and container port ranges differ in size", spec)
		}
		i := 0
		for p := range ctrRange.All() {
			hostPort := ""
			switch {
			case host == "":
			case ctrCount == 1:
				hostPort = host
			default:
				hostPort = strconv.Itoa(int(hostRange.Start()) + i)
			}
			exposed[p] = struct{}{}
			bindings[p] = append(bindings[p], network.PortBinding{HostIP: hostIP, HostPort: hostPort})
			i++
		}
	}
	return exposed, bindings, nil
}

// pullStepImage makes a cmd step's image available on the engine: pulled
// every time when the step asks to pull, otherwise only when the engine does
// not hold it for the run's platform.
func (x *acrTaskRun) pullStepImage(ctx context.Context, image string, always bool) error {
	platform := acrOCIPlatform(x.platform)
	if !always {
		if held, err := x.engine.ImageInspect(ctx, image); err == nil {
			if platform == nil || ((platform.Architecture == "" || platform.Architecture == held.Architecture) &&
				(platform.OS == "" || platform.OS == held.Os)) {
				return nil
			}
		} else if !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect image %s: %w", image, err)
		}
		_, _ = fmt.Fprintf(x.log, "Unable to find image '%s' locally\n", image)
	}
	opts := client.ImagePullOptions{RegistryAuth: x.credentialFor(image)}
	if platform != nil {
		opts.Platforms = []ocispec.Platform{*platform}
	}
	pull, err := x.engine.ImagePull(ctx, image, opts)
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	defer func() { _ = pull.Close() }()
	if err := x.writeProgress(pull.JSONMessages(ctx)); err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	return nil
}

func (x *acrTaskRun) runCmdStep(ctx context.Context, s *acrTaskStep) error {
	words, err := acrSplitArgs(s.Cmd)
	if err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	if len(words) == 0 || strings.HasPrefix(words[0], "-") {
		return fmt.Errorf("step %s: cmd must name the image to run first, then its arguments", s.ID)
	}
	image := words[0]
	name := x.runID + "_" + s.ID
	cfg := &container.Config{
		Image:        image,
		Env:          s.Env,
		User:         s.User,
		WorkingDir:   stepWorkingDirectory(s),
		AttachStdout: true,
		AttachStderr: true,
	}
	if len(words) > 1 {
		cfg.Cmd = words[1:]
	}
	if s.EntryPoint != "" {
		cfg.Entrypoint = []string{s.EntryPoint}
	}
	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(x.network),
		Binds:       []string{x.volume + ":" + acrTaskWorkspace},
		Privileged:  s.Privileged,
		Isolation:   container.Isolation(s.Isolation),
	}
	if cfg.ExposedPorts, hostCfg.PortBindings, err = acrStepPorts(s.Ports, s.Expose); err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	if s.CPUs != "" {
		cpus, ok := new(big.Rat).SetString(s.CPUs)
		if !ok || cpus.Sign() < 0 {
			return fmt.Errorf("step %s: cpus %q is not a number of CPUs", s.ID, s.CPUs)
		}
		nano := new(big.Rat).Mul(cpus, big.NewRat(1e9, 1))
		if !nano.IsInt() {
			return fmt.Errorf("step %s: cpus %q is more precise than a billionth of a CPU", s.ID, s.CPUs)
		}
		hostCfg.NanoCPUs = nano.Num().Int64()
	}

	x.logf("Launching container with name: %s", s.ID)
	if err := x.pullStepImage(ctx, image, s.Pull); err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	if _, err := x.engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       name,
		Config:     cfg,
		HostConfig: hostCfg,
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			x.network: {Aliases: []string{s.ID}},
		}},
		Platform: acrOCIPlatform(x.platform),
	}); err != nil {
		return fmt.Errorf("step %s: create its container: %w", s.ID, err)
	}
	x.mu.Lock()
	x.containers = append(x.containers, acrStepContainer{name: name, keep: s.Keep})
	x.mu.Unlock()
	if err := x.seedVolume(ctx, name); err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}

	if s.Detach {
		if _, err := x.engine.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("step %s: start its container: %w", s.ID, err)
		}
		return nil
	}
	executions := 0
	err = attempts(ctx, s, func() error {
		executions++
		return x.execute(ctx, s.ID, name, executions > 1)
	})
	if ctx.Err() != nil {
		_, _ = x.engine.ContainerKill(context.Background(), name, client.ContainerKillOptions{})
	}
	if err != nil {
		return err
	}
	return x.collectWorkspace(ctx, name)
}

// execute starts a step's container and streams its output to the run log
// until it exits. The log follows the container from its start, so output a
// short-lived container writes before a reader attaches is not lost; a
// re-execution reads only what it wrote itself. The wait for the exit is in
// place before the start, so an exit cannot slip past it.
func (x *acrTaskRun) execute(ctx context.Context, stepID, name string, again bool) error {
	waitCtx, stopWait := context.WithCancel(ctx)
	defer stopWait()
	exited := x.engine.ContainerWait(waitCtx, name, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	since := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := x.engine.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to run step ID: %s: %w", stepID, err)
	}
	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true}
	if again {
		opts.Since = since
	}
	logs, err := x.engine.ContainerLogs(ctx, name, opts)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return fmt.Errorf("failed to run step ID: %s: read its output: %w", stepID, err)
	}
	_, copyErr := stdcopy.StdCopy(x.log, x.log, logs)
	_ = logs.Close()
	if copyErr != nil && ctx.Err() == nil {
		return fmt.Errorf("failed to run step ID: %s: read its output: %w", stepID, copyErr)
	}
	select {
	case res := <-exited.Result:
		if res.Error != nil && res.Error.Message != "" {
			return fmt.Errorf("failed to run step ID: %s: %s", stepID, res.Error.Message)
		}
		if res.StatusCode != 0 {
			return fmt.Errorf("failed to run step ID: %s: exit status %d", stepID, res.StatusCode)
		}
		return nil
	case err := <-exited.Error:
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return fmt.Errorf("failed to run step ID: %s: %w", stepID, err)
	}
}

// seedVolume copies the run's source into the shared volume through the first
// container that mounts it, before that container starts.
func (x *acrTaskRun) seedVolume(ctx context.Context, container string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.volumeReady {
		return nil
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(acrTarDirectory(x.workDir, pw)) }()
	_, err := x.engine.CopyToContainer(ctx, container, client.CopyToContainerOptions{
		DestinationPath: acrTaskWorkspace,
		Content:         pr,
	})
	_ = pr.Close()
	if err != nil {
		return fmt.Errorf("copy the source into the run's volume: %w", err)
	}
	x.volumeReady = true
	return nil
}

// collectWorkspace reads the shared volume back after a cmd step, so the build
// steps that follow see what it wrote.
func (x *acrTaskRun) collectWorkspace(ctx context.Context, container string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	fresh, err := os.MkdirTemp("", "acr-run-workspace-*")
	if err != nil {
		return err
	}
	copied, err := x.engine.CopyFromContainer(ctx, container, client.CopyFromContainerOptions{SourcePath: acrTaskWorkspace + "/."})
	if err != nil {
		_ = os.RemoveAll(fresh)
		return fmt.Errorf("read the run's volume back: %w", err)
	}
	extractErr := acrExtractSource(copied.Content, fresh)
	_ = copied.Content.Close()
	if extractErr != nil {
		_ = os.RemoveAll(fresh)
		return fmt.Errorf("read the run's volume back: %w", extractErr)
	}
	_ = os.RemoveAll(x.workDir)
	x.workDir = fresh
	return nil
}

// buildStepDir maps a build step's working directory onto the host copy of
// the workspace; a build step's context is the run's source.
func (x *acrTaskRun) buildStepDir(s *acrTaskStep) (string, error) {
	wd := stepWorkingDirectory(s)
	if wd == "" {
		wd = acrTaskWorkspace
	}
	rel, ok := strings.CutPrefix(path.Clean(wd), acrTaskWorkspace)
	if !ok || (rel != "" && rel[0] != '/') {
		return "", fmt.Errorf("step %s: working directory %q is outside %s, where a build step's source is", s.ID, wd, acrTaskWorkspace)
	}
	return filepath.Join(x.workDir, filepath.FromSlash(strings.TrimPrefix(rel, "/"))), nil
}

func (x *acrTaskRun) runBuildStep(ctx context.Context, s *acrTaskStep) error {
	words, err := acrSplitArgs(s.Build)
	if err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	args := workload.DockerBuildInvocation(ctx, x.dockerEnv)
	if s.Cache == "disabled" {
		args = append(args, "--no-cache")
	}
	if s.Pull {
		args = append(args, "--pull")
	}
	if x.platform != "" {
		args = append(args, "--platform", x.platform)
	}
	args = append(args, words...)
	tags := acrBuildTags(words)
	return attempts(ctx, s, func() error {
		x.mu.Lock()
		dir, err := x.buildStepDir(s)
		x.mu.Unlock()
		if err != nil {
			return err
		}
		if err := acrDockerBuild(ctx, append(append([]string(nil), x.dockerEnv...), s.Env...), args, dir, nil, x.log); err != nil {
			return fmt.Errorf("failed to run step ID: %s: %w", s.ID, err)
		}
		x.mu.Lock()
		x.built = append(x.built, tags...)
		x.mu.Unlock()
		return nil
	})
}

// acrBuildTags reads the image tags a docker build command line names.
func acrBuildTags(words []string) []string {
	var tags []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case (w == "-t" || w == "--tag") && i+1 < len(words):
			tags = append(tags, words[i+1])
			i++
		case strings.HasPrefix(w, "--tag="):
			tags = append(tags, strings.TrimPrefix(w, "--tag="))
		case strings.HasPrefix(w, "-t") && len(w) > 2 && !strings.HasPrefix(w, "--"):
			tags = append(tags, strings.TrimPrefix(w[2:], "="))
		}
	}
	return tags
}

func (x *acrTaskRun) runPushStep(ctx context.Context, s *acrTaskStep) error {
	for _, img := range s.Push {
		x.logf("Pushing image: %s, attempt 1", img)
		push, err := x.engine.ImagePush(ctx, img, client.ImagePushOptions{RegistryAuth: x.credentialFor(img)})
		if err != nil {
			return fmt.Errorf("failed to push image %s: %w", img, err)
		}
		err = x.writeProgress(push.JSONMessages(ctx))
		_ = push.Close()
		if err != nil {
			return fmt.Errorf("failed to push image %s: %w", img, err)
		}
		x.mu.Lock()
		x.pushed = append(x.pushed, img)
		x.mu.Unlock()
	}
	return nil
}

// cleanup removes what the run left on the build host: the step containers it
// was not asked to keep, the images it built, its network and its volume, and
// the host copy of its workspace. The registry, not the build host, keeps a
// run's output.
func (x *acrTaskRun) cleanup() {
	ctx := context.Background()
	x.mu.Lock()
	defer x.mu.Unlock()
	kept := false
	for _, c := range x.containers {
		if c.keep {
			kept = true
			continue
		}
		if _, err := x.engine.ContainerRemove(ctx, c.name, client.ContainerRemoveOptions{Force: true}); err != nil {
			acrTasksLogger.Warn().Err(err).Str("container", c.name).Msg("could not remove an ACR Tasks step container")
		}
	}
	for _, img := range x.built {
		if _, err := x.engine.ImageRemove(ctx, img, client.ImageRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			acrTasksLogger.Warn().Err(err).Str("image", img).Msg("could not remove an image an ACR Tasks run built")
		}
	}
	if !kept {
		if _, err := x.engine.NetworkRemove(ctx, x.network, client.NetworkRemoveOptions{}); err != nil {
			acrTasksLogger.Warn().Err(err).Str("network", x.network).Msg("could not remove an ACR Tasks run network")
		}
		if _, err := x.engine.VolumeRemove(ctx, x.volume, client.VolumeRemoveOptions{Force: true}); err != nil {
			acrTasksLogger.Warn().Err(err).Str("volume", x.volume).Msg("could not remove an ACR Tasks run volume")
		}
	}
	_ = os.RemoveAll(x.workDir)
}
