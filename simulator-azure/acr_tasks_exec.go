package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/workload"
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
	dockerEnv []string
	platform  string
	network   string
	volume    string

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

func (x *acrTaskRun) docker(ctx context.Context, args ...string) *exec.Cmd {
	return workload.DockerCommand(ctx, x.dockerEnv, args...)
}

// dockerQuiet runs a docker command whose output belongs to the run's
// housekeeping rather than to a step.
func (x *acrTaskRun) dockerQuiet(ctx context.Context, args ...string) error {
	out, err := x.docker(ctx, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// acrRunTaskFile runs every step of task once its `when` dependencies have
// succeeded. A step that fails, unless it ignores errors, fails the run and
// stops the steps still running.
func acrRunTaskFile(ctx context.Context, task *acrTaskFile, x *acrTaskRun) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	if err := x.dockerQuiet(ctx, "network", "create", x.network); err != nil {
		return err
	}
	if err := x.dockerQuiet(ctx, "volume", "create", x.volume); err != nil {
		_ = x.dockerQuiet(context.Background(), "network", "rm", x.network)
		return err
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

func (x *acrTaskRun) runCmdStep(ctx context.Context, s *acrTaskStep) error {
	words, err := acrSplitArgs(s.Cmd)
	if err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}
	name := x.runID + "_" + s.ID
	args := []string{"create", "--name", name,
		"--network", x.network, "--network-alias", s.ID,
		"--volume", x.volume + ":" + acrTaskWorkspace}
	if wd := stepWorkingDirectory(s); wd != "" {
		args = append(args, "--workdir", wd)
	}
	for _, e := range s.Env {
		args = append(args, "--env", e)
	}
	if s.EntryPoint != "" {
		args = append(args, "--entrypoint", s.EntryPoint)
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.Privileged {
		args = append(args, "--privileged")
	}
	for _, p := range s.Ports {
		args = append(args, "--publish", p)
	}
	for _, p := range s.Expose {
		args = append(args, "--expose", p)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	if s.Isolation != "" {
		args = append(args, "--isolation", s.Isolation)
	}
	if s.Pull {
		args = append(args, "--pull", "always")
	}
	if x.platform != "" {
		args = append(args, "--platform", x.platform)
	}
	args = append(args, words...)

	x.logf("Launching container with name: %s", s.ID)
	create := x.docker(ctx, args...)
	create.Stdout = io.Discard
	create.Stderr = x.log
	if err := create.Run(); err != nil {
		return fmt.Errorf("step %s: create its container: %w", s.ID, err)
	}
	x.mu.Lock()
	x.containers = append(x.containers, acrStepContainer{name: name, keep: s.Keep})
	x.mu.Unlock()
	if err := x.seedVolume(ctx, name); err != nil {
		return fmt.Errorf("step %s: %w", s.ID, err)
	}

	if s.Detach {
		return x.dockerQuiet(ctx, "start", name)
	}
	executions := 0
	err = attempts(ctx, s, func() error {
		executions++
		return x.execute(ctx, s.ID, name, executions > 1)
	})
	if ctx.Err() != nil {
		_ = x.dockerQuiet(context.Background(), "kill", name)
	}
	if err != nil {
		return err
	}
	return x.collectWorkspace(ctx, name)
}

// execute starts a step's container and streams its output to the run log
// until it exits. The log follows the container from its start, so output a
// short-lived container writes before a reader attaches is not lost; a
// re-execution reads only what it wrote itself.
func (x *acrTaskRun) execute(ctx context.Context, stepID, name string, again bool) error {
	since := time.Now().UTC().Format(time.RFC3339Nano)
	if err := x.dockerQuiet(ctx, "start", name); err != nil {
		return fmt.Errorf("failed to run step ID: %s: %w", stepID, err)
	}
	args := []string{"logs", "--follow"}
	if again {
		args = append(args, "--since", since)
	}
	logs := x.docker(ctx, append(args, name)...)
	logs.Stdout = x.log
	logs.Stderr = x.log
	if err := logs.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("failed to run step ID: %s: read its output: %w", stepID, err)
	}
	out, err := x.docker(ctx, "wait", name).Output()
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return fmt.Errorf("failed to run step ID: %s: %w", stepID, err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("failed to run step ID: %s: read its exit code %q: %w", stepID, out, err)
	}
	if code != 0 {
		return fmt.Errorf("failed to run step ID: %s: exit status %d", stepID, code)
	}
	return nil
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
	cp := x.docker(ctx, "cp", "-", container+":"+acrTaskWorkspace)
	cp.Stdin = pr
	out, err := cp.CombinedOutput()
	_ = pr.Close()
	if err != nil {
		return fmt.Errorf("copy the source into the run's volume: %w: %s", err, strings.TrimSpace(string(out)))
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
	cp := x.docker(ctx, "cp", container+":"+acrTaskWorkspace+"/.", "-")
	stdout, err := cp.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(fresh)
		return err
	}
	var stderr strings.Builder
	cp.Stderr = &stderr
	if err := cp.Start(); err != nil {
		_ = os.RemoveAll(fresh)
		return err
	}
	extractErr := acrExtractSource(stdout, fresh)
	_, _ = io.Copy(io.Discard, stdout)
	if err := cp.Wait(); err != nil || extractErr != nil {
		_ = os.RemoveAll(fresh)
		return fmt.Errorf("read the run's volume back: %v %v: %s", err, extractErr, strings.TrimSpace(stderr.String()))
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
		build := x.docker(ctx, args...)
		build.Dir = dir
		build.Env = append(append([]string(nil), x.dockerEnv...), s.Env...)
		build.Stdout = x.log
		build.Stderr = x.log
		if err := build.Run(); err != nil {
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
		push := x.docker(ctx, "push", img)
		push.Env = append(append([]string(nil), x.dockerEnv...), s.Env...)
		push.Stdout = x.log
		push.Stderr = x.log
		if err := push.Run(); err != nil {
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
		if err := x.dockerQuiet(ctx, "rm", "--force", c.name); err != nil {
			acrTasksLogger.Warn().Err(err).Str("container", c.name).Msg("could not remove an ACR Tasks step container")
		}
	}
	for _, img := range x.built {
		_ = x.dockerQuiet(ctx, "rmi", "--force", img)
	}
	if !kept {
		if err := x.dockerQuiet(ctx, "network", "rm", x.network); err != nil {
			acrTasksLogger.Warn().Err(err).Str("network", x.network).Msg("could not remove an ACR Tasks run network")
		}
		if err := x.dockerQuiet(ctx, "volume", "rm", "--force", x.volume); err != nil {
			acrTasksLogger.Warn().Err(err).Str("volume", x.volume).Msg("could not remove an ACR Tasks run volume")
		}
	}
	_ = os.RemoveAll(x.workDir)
}
