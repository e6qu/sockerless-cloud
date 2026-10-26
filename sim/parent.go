package sim

import (
	"fmt"
	"os"
	"strconv"
)

// ParentPIDVariable names the process this simulator must not outlive.
//
// A test harness stops the simulator it started from its own cleanup, which
// a `go test` killed outright — a timeout kill, a stopped run, an editor
// closing the process — never reaches; the simulator and the container
// reaper waiting on it then keep running, holding ports and memory. A signal
// handler cannot close that gap, since the ending that matters is the one
// signal a process cannot trap, so the child watches its parent, as the
// reaper watches the simulator.
const ParentPIDVariable = "SOCKERLESS_PARENT_PID"

// ExitWithParent ends this process once the process named by
// SOCKERLESS_PARENT_PID has exited. It returns immediately, and does nothing
// when the variable is unset or unusable: a simulator run by hand, by a service
// manager or by a container runtime has no parent it should die with, and
// inferring one from os.Getppid() would end a `nohup`ed run the moment its
// shell closed.
func ExitWithParent() {
	watchParent(os.Getenv(ParentPIDVariable), func() { os.Exit(0) })
}

// watchParent is ExitWithParent's testable core: it reads the pid, and when
// that names a process other than this one, starts a watch calling onExit once
// the process is gone. It reports whether a watch was started.
func watchParent(value string, onExit func()) bool {
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	wait, err := processExit(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sockerless: cannot watch parent process %d: %v\n", pid, err)
		return false
	}
	go func() {
		wait()
		onExit()
	}()
	return true
}
