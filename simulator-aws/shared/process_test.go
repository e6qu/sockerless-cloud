package simulator

import (
	"sync"
	"testing"
	"time"
)

// collectSink collects log lines for test assertions.
type collectSink struct {
	mu    sync.Mutex
	lines []LogLine
}

func (s *collectSink) WriteLog(line LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

func (s *collectSink) Lines() []LogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]LogLine, len(s.lines))
	copy(cp, s.lines)
	return cp
}

func TestStartProcess_CapturesOutput(t *testing.T) {
	sink := &collectSink{}
	h := StartProcess(ProcessConfig{
		Command: []string{"echo", "hello"},
	}, sink)
	result := h.Wait()
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	lines := sink.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}
	if lines[0].Stream != "stdout" {
		t.Errorf("expected stream stdout, got %s", lines[0].Stream)
	}
	if lines[0].Text != "hello" {
		t.Errorf("expected text 'hello', got %q", lines[0].Text)
	}
}

func TestStartProcess_ExitCode(t *testing.T) {
	sink := &collectSink{}
	h := StartProcess(ProcessConfig{
		Command: []string{"sh", "-c", "exit 42"},
	}, sink)
	result := h.Wait()
	if result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", result.ExitCode)
	}
}

func TestStartProcess_Timeout(t *testing.T) {
	sink := &collectSink{}
	start := time.Now()
	h := StartProcess(ProcessConfig{
		Command: []string{"sleep", "10"},
		Timeout: 100 * time.Millisecond,
	}, sink)
	result := h.Wait()
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("timeout did not kill process in time: elapsed %v", elapsed)
	}
	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code after timeout")
	}
}

func TestStartProcess_Cancel(t *testing.T) {
	sink := &collectSink{}
	h := StartProcess(ProcessConfig{
		Command: []string{"sleep", "10"},
	}, sink)

	start := time.Now()
	h.Cancel()
	result := h.Wait()
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("cancel did not kill process in time: elapsed %v", elapsed)
	}
	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code after cancel")
	}
}

func TestStartProcess_Env(t *testing.T) {
	sink := &collectSink{}
	h := StartProcess(ProcessConfig{
		Command: []string{"sh", "-c", "echo $FOO"},
		Env:     map[string]string{"FOO": "bar"},
	}, sink)
	result := h.Wait()
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	lines := sink.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}
	if lines[0].Text != "bar" {
		t.Errorf("expected text 'bar', got %q", lines[0].Text)
	}
}
