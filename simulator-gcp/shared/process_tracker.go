package simulator

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ProcessTracker persists running process PIDs to disk for recovery after restart.
type ProcessTracker struct {
	dir string // directory for PID files
}

// NewProcessTracker creates a tracker that stores PID files in the given directory.
// Returns nil if dir is empty (tracking disabled).
func NewProcessTracker(dir string) *ProcessTracker {
	if dir == "" {
		return nil
	}
	pidDir := filepath.Join(dir, "pids")
	_ = os.MkdirAll(pidDir, 0o755)
	return &ProcessTracker{dir: pidDir}
}

// Track records a process PID for later recovery.
func (t *ProcessTracker) Track(id string, pid int) {
	if t == nil {
		return
	}
	_ = os.WriteFile(filepath.Join(t.dir, id+".pid"), []byte(strconv.Itoa(pid)), 0o644)
}

// Untrack removes the PID file for a completed process.
func (t *ProcessTracker) Untrack(id string) {
	if t == nil {
		return
	}
	_ = os.Remove(filepath.Join(t.dir, id+".pid"))
}

// LiveProcesses scans the PID directory and returns IDs mapped to PIDs
// for processes that are still alive. Dead PID files are cleaned up.
func (t *ProcessTracker) LiveProcesses() map[string]int {
	if t == nil {
		return nil
	}
	result := make(map[string]int)
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".pid") {
			continue
		}
		id := strings.TrimSuffix(name, ".pid")
		data, err := os.ReadFile(filepath.Join(t.dir, name))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		if isAlive(pid) {
			result[id] = pid
		} else {
			// Dead process — clean up PID file
			_ = os.Remove(filepath.Join(t.dir, name))
		}
	}
	return result
}

// isAlive checks if a process is still running via signal 0.
func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// KillProcess sends SIGTERM to a tracked process.
func KillProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	return proc.Signal(syscall.SIGTERM)
}
