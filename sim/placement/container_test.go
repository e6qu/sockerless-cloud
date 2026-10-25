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
	if containerRunning(t, id) {
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

// A workload placed with a forward reaches an endpoint of its network on its
// own loopback from its first instruction: the forwarder owns its namespace
// and listens before the workload starts. A member placed beside it reaches
// the same loopback, the workload's address is the forwarder's, and the
// forwarder goes with the workload, also when a later process adopted it.
func TestContainerBackendPlacesAWorkloadBehindALoopbackForwarder(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), testImage, platform); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Format("150405.000000")
	network := "sockerless-sim-placement-forward-" + strings.ReplaceAll(stamp, ".", "-")
	if _, err := sim.EnsureDockerNetwork(network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.RemoveDockerNetwork(network) })
	sandbox := sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true}
	backend := ContainerBackend{}

	upstream, err := backend.Place(t.Context(), Workload{
		Image:        testImage,
		Architecture: platform,
		Command:      []string{"sh", "-c", "mkdir -p /tmp/www && echo from-upstream > /tmp/www/credentials && exec httpd -f -p 8080 -h /tmp/www"},
		Name:         "sockerless-sim-placement-upstream-" + stamp,
		Network:      network,
		Sandbox:      sandbox,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upstream.Cancel)
	upstreamAddr, err := upstream.Address(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	forwards := []Forward{{Listen: "127.0.0.1:51679", Upstream: upstreamAddr + ":8080"}}

	var mu sync.Mutex
	var lines []string
	sink := sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		lines = append(lines, line.Text)
		mu.Unlock()
	})
	once, err := backend.PlaceForwarded(t.Context(), Workload{
		Image:        testImage,
		Architecture: platform,
		Command:      []string{"wget", "-qO-", "http://127.0.0.1:51679/credentials"},
		Name:         "sockerless-sim-placement-forwarded-" + stamp,
		Network:      network,
		Sandbox:      sandbox,
	}, forwards, sink)
	if err != nil {
		t.Fatal(err)
	}
	forwarderID, err := forwarderOf(t.Context(), once.ID())
	if err != nil || forwarderID == "" {
		t.Fatalf("the workload names no forwarder: %q, %v", forwarderID, err)
	}
	if result := once.Wait(); result.ExitCode != 0 {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the workload's first read of its loopback failed (%d):\n%s", result.ExitCode, strings.Join(lines, "\n"))
	}
	mu.Lock()
	out := strings.Join(lines, "\n")
	mu.Unlock()
	if strings.TrimSpace(out) != "from-upstream" {
		t.Fatalf("the workload read %q through its loopback", out)
	}
	if containerRunning(t, forwarderID) {
		t.Fatal("the forwarder outlived the workload")
	}

	long, err := backend.PlaceForwarded(t.Context(), Workload{
		Image:             testImage,
		Architecture:      platform,
		Command:           []string{"sleep", "300"},
		Name:              "sockerless-sim-placement-forwarded-long-" + stamp,
		Network:           network,
		PublishPorts:      []int{8080},
		Sandbox:           sandbox,
		CancelGracePeriod: time.Second,
	}, forwards, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(long.Cancel)
	forwarderID, err = forwarderOf(t.Context(), long.ID())
	if err != nil || forwarderID == "" {
		t.Fatalf("the workload names no forwarder: %q, %v", forwarderID, err)
	}
	address, err := long.Address(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	forwarderAddress, err := ContainerAddress(t.Context(), forwarderID)
	if err != nil || address != forwarderAddress {
		t.Fatalf("the workload's address %q is not its forwarder's %q (%v)", address, forwarderAddress, err)
	}
	hostPort, err := long.PublishedPort(t.Context(), 8080)
	if err != nil {
		t.Fatalf("the workload's published port: %v", err)
	}
	existing, err := backend.Find(t.Context(), map[string]string{forwardedLabel: forwarderID})
	if err != nil || len(existing) != 1 || existing[0].ID != long.ID() {
		t.Fatalf("Find returned %v, %v; want the workload alone", existing, err)
	}
	found, err := sim.FindExistingContainers(map[string]string{forwardedLabel: forwarderID})
	if err != nil || len(found) != 1 || hostPort == 0 || found[0].PublishedPorts[8080] != hostPort {
		t.Fatalf("the workload lists published ports %v (%v), want 8080 on %d", found, err, hostPort)
	}
	member, err := backend.PlaceMember(t.Context(), long.ID(), Workload{
		Image:        testImage,
		Architecture: platform,
		Command:      []string{"wget", "-qO-", "http://127.0.0.1:51679/credentials"},
		Name:         "sockerless-sim-placement-forwarded-member-" + stamp,
		Sandbox:      sandbox,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	if result := member.Wait(); result.ExitCode != 0 {
		t.Fatalf("the member's read of the shared loopback exited %d", result.ExitCode)
	}
	if !containerRunning(t, forwarderID) {
		t.Fatal("the member's exit took the workload's forwarder")
	}

	adopted, err := backend.Adopt(t.Context(), long.ID(), Workload{CancelGracePeriod: time.Second}, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	adopted.Cancel()
	<-adopted.Exited()
	if containerRunning(t, forwarderID) {
		t.Fatal("the forwarder outlived the adopted workload")
	}
}

// containerRunning reports whether the engine holds the container and has it
// running.
func containerRunning(t *testing.T, id string) bool {
	t.Helper()
	insp, err := sim.DockerClient().ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	if err != nil || insp.Container.State == nil {
		return false
	}
	return insp.Container.State.Running
}
