//go:build windows

package realexec

import (
	"os/exec"
	"syscall"
)

const processQueryLimitedInformation = 0x1000

// DetachCommand starts the cleanup helper in a separate process group.
func DetachCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// ProcessAlive reports whether pid can still be opened as a process.
func ProcessAlive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	return syscall.CloseHandle(handle) == nil
}
