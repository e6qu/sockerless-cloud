package placement

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const testImage = "mirror.gcr.io/library/busybox:latest"

// A kept workload exits and stays: Restart boots it again on the same
// filesystem, so what it wrote the first time is there, and Remove discards
// it.
func TestContainerBackendKeepsRestartsAndRemovesAWorkload(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), testImage, platform); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	sink := sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		lines = append(lines, line.Text)
		mu.Unlock()
	})
	w := Workload{
		Image:        testImage,
		Architecture: platform,
		Command:      []string{"sh", "-c", "echo boot >> /var/boots; wc -l < /var/boots"},
		Name:         "sockerless-sim-placement-keep-" + time.Now().Format("150405.000000"),
		KeepOnExit:   true,
		Timeout:      time.Minute,
		Sandbox:      sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true},
	}
	backend := ContainerBackend{}
	handle, err := backend.Place(t.Context(), w, sink)
	if err != nil {
		t.Fatal(err)
	}
	id := handle.ID()
	if result := handle.Wait(); result.ExitCode != 0 {
		t.Fatalf("first run exited %d", result.ExitCode)
	}
	again, err := backend.Restart(t.Context(), id, w, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result := again.Wait(); result.ExitCode != 0 {
		t.Fatalf("second run exited %d", result.ExitCode)
	}
	mu.Lock()
	out := strings.Join(lines, "\n")
	mu.Unlock()
	if !strings.Contains(out, "2") {
		t.Fatalf("the restarted workload did not see its earlier write:\n%s", out)
	}
	if err := backend.Remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if sim.ContainerRunning(id) {
		t.Fatal("the workload still runs after Remove")
	}
	if err := backend.Remove(t.Context(), id); err != nil {
		t.Fatalf("a second Remove is not idempotent: %v", err)
	}
}
