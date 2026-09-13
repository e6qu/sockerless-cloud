package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// ecsHostCeiling is what this host can commit to ECS tasks: the simulator's
// own cgroup limits when it runs bounded (the usual deployment — a container
// with a memory limit and a CPU quota), and otherwise the machine's memory
// and CPUs. A ceiling it cannot determine is a bug in this function, not
// something to paper over with "unlimited": it panics with what it read.
func ecsHostCeiling() ecsTaskSize {
	memBytes, err := ecsCgroupMemoryMax()
	if err != nil {
		panic(fmt.Sprintf("ecs placement: cannot size the host: %v", err))
	}
	cpuUnits, err := ecsCgroupCPUUnits()
	if err != nil {
		panic(fmt.Sprintf("ecs placement: cannot size the host: %v", err))
	}
	return ecsTaskSize{MemoryMiB: int(memBytes / (1024 * 1024)), CPUUnits: cpuUnits}
}

// ecsCgroupMemoryMax reads the cgroup v2 memory.max of the simulator's own
// cgroup; "max" (unbounded) and a host without cgroup v2 mean the machine's
// MemTotal.
func ecsCgroupMemoryMax() (int64, error) {
	raw, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err == nil {
		value := strings.TrimSpace(string(raw))
		if value != "max" {
			return strconv.ParseInt(value, 10, 64)
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	return ecsProcMemTotal()
}

func ecsProcMemTotal() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("/proc/meminfo MemTotal %q: %w", fields[1], err)
			}
			return kb * 1024, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("/proc/meminfo has no MemTotal line")
}

// ecsCgroupCPUUnits reads cpu.max ("<quota> <period>", or "max <period>") and
// converts the quota to ECS CPU units; unbounded means every CPU the machine
// has.
func ecsCgroupCPUUnits() (int, error) {
	raw, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		if os.IsNotExist(err) {
			return runtime.NumCPU() * 1024, nil
		}
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, fmt.Errorf("/sys/fs/cgroup/cpu.max %q: expected \"<quota> <period>\"", strings.TrimSpace(string(raw)))
	}
	if fields[0] == "max" {
		return runtime.NumCPU() * 1024, nil
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("/sys/fs/cgroup/cpu.max quota %q: %w", fields[0], err)
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period <= 0 {
		return 0, fmt.Errorf("/sys/fs/cgroup/cpu.max period %q: invalid", fields[1])
	}
	return int(quota * 1024 / period), nil
}
