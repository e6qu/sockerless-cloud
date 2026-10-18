package placement

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

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

// The backend keeps a workload's whole console across its restarts and
// captures its filesystem as an image another workload runs from.
func TestContainerBackendConsoleAndCapture(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), testImage, platform); err != nil {
		t.Fatal(err)
	}
	w := Workload{
		Image:        testImage,
		Architecture: platform,
		Command:      []string{"sh", "-c", "echo boot >> /var/boots; echo booted $(wc -l < /var/boots)"},
		Name:         "sockerless-sim-placement-console-" + time.Now().Format("150405.000000"),
		KeepOnExit:   true,
		Timeout:      time.Minute,
		Sandbox:      sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true},
	}
	backend := ContainerBackend{}
	handle, err := backend.Place(t.Context(), w, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	id := handle.ID()
	t.Cleanup(func() { _ = backend.Remove(t.Context(), id) })
	handle.Wait()
	again, err := backend.Restart(t.Context(), id, w, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	again.Wait()
	console, err := backend.Console(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(console, "booted 1\n") || !strings.Contains(console, "booted 2\n") {
		t.Fatalf("the console does not carry both boots:\n%s", console)
	}
	reference := "sockerless-sim-placement-capture:" + time.Now().Format("150405.000000")
	if err := backend.Capture(t.Context(), id, reference); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sim.DockerClient().ImageRemove(context.Background(), reference, client.ImageRemoveOptions{Force: true})
	})
	var mu sync.Mutex
	var lines []string
	clone, err := backend.Place(t.Context(), Workload{
		Image:        reference,
		Architecture: platform,
		Command:      []string{"sh", "-c", "wc -l < /var/boots"},
		Name:         w.Name + "-clone",
		Timeout:      time.Minute,
		Sandbox:      w.Sandbox,
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		lines = append(lines, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	clone.Wait()
	mu.Lock()
	out := strings.Join(lines, "\n")
	mu.Unlock()
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("the captured image does not carry the two boots: %q", out)
	}
}
