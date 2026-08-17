package simulator

import "time"

// The log stream and the terminal result of a workload the simulator runs.
//
// The simulator executes every workload as a container (see container.go);
// there is no host-process execution path, and SIM_RUNTIME=process means
// API-only — serving the API surface without a container engine, not running
// workloads as processes. The types here are the container path's log sink and
// its result.

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

// NoopSink discards all log output.
type NoopSink struct{}

func (NoopSink) WriteLog(LogLine) {}

// FuncSink wraps a function as a LogSink.
type FuncSink func(LogLine)

func (f FuncSink) WriteLog(line LogLine) { f(line) }

// ProcessResult is the terminal state of a workload: the exit code its main
// process returned, when it ran, and a non-nil Error when it never started or
// was killed rather than exiting cleanly.
type ProcessResult struct {
	ExitCode  int
	StartedAt time.Time
	StoppedAt time.Time
	Error     error
}
