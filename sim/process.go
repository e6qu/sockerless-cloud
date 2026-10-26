package sim

import "time"

// SIM_RUNTIME=process means API-only — serving the API without a container
// engine — and never running workloads as host processes: every workload runs
// as a container (container.go), and these are its log sink and result.

// LogLine is a single line of captured output.
type LogLine struct {
	Stream    string // "stdout" or "stderr"
	Text      string
	Timestamp time.Time
}

// LogSink receives log lines as the workload writes them. Each cloud
// implements its own sink (Amazon CloudWatch Logs, Cloud Logging, Azure
// Monitor Logs).
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
