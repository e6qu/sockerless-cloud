package main

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// On macOS there is no cgroup; the machine's memory and CPUs are the ceiling.
func ecsHostCeiling() ecsTaskSize {
	memBytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		panic(fmt.Sprintf("ecs placement: cannot size the host: sysctl hw.memsize: %v", err))
	}
	return ecsTaskSize{MemoryMiB: int(memBytes / (1024 * 1024)), CPUUnits: runtime.NumCPU() * 1024}
}
