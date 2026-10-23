package sidecar

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// NamespacePID is the id a process has inside the pid namespace it runs in,
// read from its host entry's NSpid line, which any process on the host may
// read. hostPID is the id the engine's process table reports.
func NamespacePID(hostPID int) (int, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", hostPID))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "NSpid:" {
			return strconv.Atoi(fields[len(fields)-1])
		}
	}
	if scanner.Err() != nil {
		return 0, scanner.Err()
	}
	return 0, fmt.Errorf("process %d: /proc status carries no NSpid line", hostPID)
}

// ReadProcess reads a workload process's mapping table (what "maps") or an
// ELF core of it (what "core"). The simulator's own host does not let an
// unprivileged process read another identity's /proc, and a core needs
// permission to trace, which no workload holds; so the sidecar program joins
// the container's pid namespace as a platform helper with that one permission,
// writes what it read into a directory only the simulator mounted, and ends.
// pid is the process's id inside the container.
func ReadProcess(ctx context.Context, containerID string, pid int, what string) ([]byte, error) {
	if what != "maps" && what != "core" {
		return nil, fmt.Errorf("read %q of a process: only maps and core can be read", what)
	}
	if err := sim.RequireContainerRuntime("reading a workload's process"); err != nil {
		return nil, err
	}
	cli := sim.DockerClient()
	image, platform, err := EnsureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	parent := sim.ScopedDataDir("", "process-reads", "sockerless-process-reads")
	if err := sim.EnsureWritableDir(parent); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, "read-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o777); err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var stderr []string
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         "sockerless-sim-process-read-" + sim.RandomHex(6),
		Command:      []string{"/sidecar", what, strconv.Itoa(pid)},
		Binds:        []string{dir + ":/out"},
		NetworkMode:  "none",
		PidMode:      "container:" + containerID,
		Labels:       map[string]string{"sockerless-sim-type": "process-read"},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"SYS_PTRACE"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
			ReadonlyRootfs:   true,
		},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		stderr = append(stderr, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		return nil, fmt.Errorf("read %s of process %d: %w", what, pid, err)
	}
	result := handle.Wait()
	if result.Error != nil || result.ExitCode != 0 {
		mu.Lock()
		defer mu.Unlock()
		return nil, fmt.Errorf("read %s of process %d: exit %d: %s", what, pid, result.ExitCode,
			errors.Join(result.Error, errors.New(strings.Join(stderr, "\n"))))
	}
	return os.ReadFile(filepath.Join(dir, what))
}
