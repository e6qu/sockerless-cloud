package simulator

import (
	"time"
)

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

// ProcessResult is returned when a workload completes.
type ProcessResult struct {
	ExitCode  int
	StartedAt time.Time
	StoppedAt time.Time
	Error     error // non-nil if the workload failed to start
}

// FuncSink wraps a function as a LogSink.
type FuncSink func(LogLine)

func (f FuncSink) WriteLog(line LogLine) { f(line) }
