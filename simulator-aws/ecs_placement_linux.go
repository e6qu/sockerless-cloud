package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// The simulator's cgroup limits when bounded, else the machine's memory and
// CPUs. An undeterminable ceiling panics rather than assuming unlimited.
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

// "max" or no cgroup v2 means the machine's MemTotal.
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

// cpu.max is "<quota> <period>" or "max <period>"; unbounded means every CPU.
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
