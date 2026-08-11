//go:build !windows

package realexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// DetachCommand puts a helper in its own session so a test harness terminating
// the simulator's process group does not also kill its cleanup reaper.
func DetachCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// ProcessAlive reports whether pid still identifies a running process.
func ProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
