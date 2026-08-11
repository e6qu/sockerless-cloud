package simulator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ProcessConfig describes what to execute.
type ProcessConfig struct {
	Command []string          // entrypoint + args (e.g. ["echo", "hello"])
	Env     map[string]string // environment variables
	Dir     string            // working directory (optional)
	Timeout time.Duration     // max execution time (0 = no timeout)
}

// LogLine is a single line of captured output.
type LogLine struct {
	Stream    string // "stdout" or "stderr"
	Text      string
	Timestamp time.Time
}

// LogSink receives log lines as they are produced.
// Each cloud implements its own sink (CloudWatch, Cloud Logging, Log Analytics).
type LogSink interface {
	WriteLog(line LogLine)
}

// ProcessResult is returned when the process completes.
type ProcessResult struct {
	ExitCode  int
	StartedAt time.Time
	StoppedAt time.Time
	Error     error // non-nil if process failed to start
}

// ProcessHandle allows waiting on or cancellation of a running process.
type ProcessHandle struct {
	cancel context.CancelFunc
	done   <-chan ProcessResult
	pid    int // OS process ID (0 if failed to start)
}

// Pid returns the OS process ID.
func (h *ProcessHandle) Pid() int { return h.pid }

// Wait blocks until the process completes.
func (h *ProcessHandle) Wait() ProcessResult { return <-h.done }

// Cancel kills the process.
func (h *ProcessHandle) Cancel() { h.cancel() }

// NoopSink discards all log output.
type NoopSink struct{}

func (NoopSink) WriteLog(LogLine) {}

// FuncSink wraps a function as a LogSink.
type FuncSink func(LogLine)

func (f FuncSink) WriteLog(line LogLine) { f(line) }

// StartTrackedProcess launches a process and tracks its PID for recovery.
// The tracker may be nil (no persistence), in which case this is equivalent to StartProcess.
func StartTrackedProcess(id string, cfg ProcessConfig, sink LogSink, tracker *ProcessTracker) *ProcessHandle {
	h := StartProcess(cfg, sink)
	if tracker != nil && h.pid > 0 {
		tracker.Track(id, h.pid)
		// Untrack when process completes
		origDone := h.done
		wrappedDone := make(chan ProcessResult, 1)
		go func() {
			result := <-origDone
			tracker.Untrack(id)
			wrappedDone <- result
		}()
		h.done = wrappedDone
	}
	return h
}

// StartProcess launches a command and streams output to the sink.
// Returns a handle for waiting/cancellation. Non-blocking.
func StartProcess(cfg ProcessConfig, sink LogSink) *ProcessHandle {
	resultCh := make(chan ProcessResult, 1)

	ctx, cancel := context.WithCancel(context.Background())
	if cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), cfg.Timeout)
	}

	binary := cfg.Command[0]
	if filepath.IsAbs(binary) {
		if _, err := os.Stat(binary); os.IsNotExist(err) {
			if resolved, lookErr := exec.LookPath(filepath.Base(binary)); lookErr == nil {
				binary = resolved
			}
		}
	}
	cmd := exec.CommandContext(ctx, binary, cfg.Command[1:]...)

	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}

	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	startedAt := time.Now()

	// Set up pipes for stdout and stderr. A pipe-creation failure must fail
	// the launch — a nil reader would panic the scan goroutine
	// (bufio.NewScanner(nil).Scan()) and silently lose all process output.
	stdoutPipe, outErr := cmd.StdoutPipe()
	stderrPipe, errErr := cmd.StderrPipe()
	if outErr != nil || errErr != nil {
		cancel()
		err := outErr
		if err == nil {
			err = errErr
		}
		resultCh <- ProcessResult{
			ExitCode:  -1,
			StartedAt: startedAt,
			StoppedAt: time.Now(),
			Error:     fmt.Errorf("create process pipes: %w", err),
		}
		return &ProcessHandle{cancel: func() {}, done: resultCh}
	}

	if err := cmd.Start(); err != nil {
		cancel()
		resultCh <- ProcessResult{
			ExitCode:  -1,
			StartedAt: startedAt,
			StoppedAt: time.Now(),
			Error:     err,
		}
		return &ProcessHandle{cancel: func() {}, done: resultCh}
	}

	// Scan stdout and stderr in separate goroutines
	scanDone := make(chan struct{}, 2)
	scanStream := func(reader io.Reader, stream string) {
		defer func() { scanDone <- struct{}{} }()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			sink.WriteLog(LogLine{
				Stream:    stream,
				Text:      scanner.Text(),
				Timestamp: time.Now(),
			})
		}
	}

	go scanStream(stdoutPipe, "stdout")
	go scanStream(stderrPipe, "stderr")

	go func() {
		// Wait for both scanners to finish before calling cmd.Wait
		<-scanDone
		<-scanDone

		err := cmd.Wait()
		exitCode := 0
		var waitErr error
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				// Killed by signal/context, or an I/O error on the pipes — not a
				// clean process exit. Surface it so the caller can distinguish
				// "exited -1" from "failed to run / was killed".
				exitCode = -1
				waitErr = err
			}
		}
		cancel()
		resultCh <- ProcessResult{
			ExitCode:  exitCode,
			StartedAt: startedAt,
			StoppedAt: time.Now(),
			Error:     waitErr,
		}
	}()

	return &ProcessHandle{cancel: cancel, done: resultCh, pid: cmd.Process.Pid}
}
