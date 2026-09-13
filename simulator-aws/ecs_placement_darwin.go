package main

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// ecsHostCeiling on macOS (a developer running the simulator natively, with
// no cgroup of its own) is the machine: physical memory and every CPU.
func ecsHostCeiling() ecsTaskSize {
	memBytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		panic(fmt.Sprintf("ecs placement: cannot size the host: sysctl hw.memsize: %v", err))
	}
	return ecsTaskSize{MemoryMiB: int(memBytes / (1024 * 1024)), CPUUnits: runtime.NumCPU() * 1024}
}
