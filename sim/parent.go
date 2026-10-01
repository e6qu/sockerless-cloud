package sim

import (
	"fmt"
	"os"
	"strconv"
)

// ParentPIDVariable names the process this simulator must not outlive.
//
// A test harness starts a simulator as a child and stops it from its own
// cleanup, which covers every ordinary ending and not the one that matters: a
// `go test` killed outright — a timeout kill, a stopped run, an editor closing
// the process — never reaches its cleanup, and the simulator it started keeps
// running. The container reaper waits on the simulator, so the pair survives
// together, holding ports and memory until someone notices. Simulators aged
// two to twelve days were found that way, across all three clouds.
//
// A signal handler cannot close that gap, because the ending that matters is
// the one signal a process cannot trap. The watch has to run inside the child
// and look outward, which is what the reaper already does for the simulator;
// this is the same relationship one level up.
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
