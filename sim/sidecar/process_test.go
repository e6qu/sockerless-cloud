package sidecar

import (
	"bytes"
	"context"
	"debug/elf"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/procimage"
	"github.com/moby/moby/client"
)

// The helper reads a workload process the simulator's own host would not let
// it read: the mapping table names the program, and the core is an ELF core
// whose segments the standard reader opens.
func TestReadProcessReadsAWorkloadFromItsOwnNamespace(t *testing.T) {
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const image = "mirror.gcr.io/library/alpine:3.21"
	if err := sim.PullImage(ctx, image, ""); err != nil {
		t.Fatal(err)
	}
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"/bin/sleep", "300"},
		Name:         "sockerless-sim-process-read-subject-" + sim.RandomHex(4),
	}, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handle.Cancel)

	top, err := sim.DockerClient().ContainerTop(ctx, handle.ContainerID, client.ContainerTopOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hostPID := 0
	for i, title := range top.Titles {
		if title == "PID" && len(top.Processes) > 0 {
			hostPID, _ = strconv.Atoi(top.Processes[0][i])
		}
	}
	if hostPID == 0 {
		t.Fatalf("the engine's process table names no process: %+v", top)
	}
	pid, err := NamespacePID(hostPID)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 1 {
		t.Fatalf("the container's only process has pid %d inside it, want 1", pid)
	}

	maps, err := ReadProcess(ctx, handle.ContainerID, pid, "maps")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(maps), "/bin/busybox") {
		t.Fatalf("the mapping table does not name the program:\n%s", maps)
	}
	core, err := ReadProcess(ctx, handle.ContainerID, pid, "core")
	if err != nil {
		t.Fatal(err)
	}
	file, err := elf.NewFile(bytes.NewReader(core))
	if err != nil {
		t.Fatalf("the core is not an ELF file a reader opens: %v", err)
	}
	if file.Type != elf.ET_CORE || file.Machine != procimage.Machine() {
		t.Fatalf("core is %v for %v", file.Type, file.Machine)
	}
	loads := 0
	for _, p := range file.Progs {
		if p.Type == elf.PT_LOAD {
			loads++
		}
	}
	if loads == 0 {
		t.Fatal("the core carries no loadable segment")
	}
}
