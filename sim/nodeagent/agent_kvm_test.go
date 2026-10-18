//go:build kvm

package nodeagent_test

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const guestImage = "mirror.gcr.io/library/golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d"

// The agent's proof, on a host with /dev/kvm: the agent starts as a container
// on an engine network, a guest built from an OCI image boots on that network
// with the hosts entries it was given, builds and runs the repository's
// arithmetic program as its first process, answers a command over the agent,
// boots again on its kept disk with its console continuing, has that disk
// captured as an image, and leaves no guest, tap or disk behind.
func TestNodeAgentBootsAGuestFromAnOCIImage(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), guestImage, platform); err != nil {
		t.Fatal(err)
	}
	networkName := fmt.Sprintf("sockerless-sim-kvm-%d", time.Now().UnixNano())
	if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: networkName, CIDR: netip.MustParsePrefix("10.79.0.0/24")}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sim.RemoveDockerNetworkContext(context.Background(), networkName) }()

	var agentLog []string
	var mu sync.Mutex
	agent, err := nodeagent.Start(t.Context(), nodeagent.Options{
		Name:    networkName + "-agent",
		Network: networkName,
		Address: netip.MustParseAddr("10.79.0.3"),
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		agentLog = append(agentLog, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Stop()

	root, _ := filepath.Abs("../..")
	source, err := os.ReadFile(filepath.Join(root, "testdata", "eval-arithmetic", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	backend := placement.NewMicroVMBackend(agent, "")
	var guestLog []string
	printed := make(chan struct{})
	var printedOnce sync.Once
	workload := placement.Workload{
		Image:        guestImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-arithmetic",
		Hostname:     "arithmetic",
		Network:      networkName,
		IPv4Address:  "10.79.0.10",
		ExtraHosts:   []string{"metadata.example.internal:10.79.0.3", "host.docker.internal:host-gateway"},
		Command:      []string{"/bin/sh", "-c"},
		Args:         []string{"cd /work && go mod init arithmetic >/dev/null; go build -o eval . && ./eval '3 + 4 * 2' && hostname && sleep 30"},
		Env:          map[string]string{"GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
		Seed:         []sim.SeedFile{{Path: "/work/main.go", Data: source}},
		MemoryBytes:  1024 << 20,
		NanoCPU:      2_000_000_000,
		KeepOnExit:   true,
	}
	handle, err := backend.Place(t.Context(), workload, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		guestLog = append(guestLog, line.Text)
		mu.Unlock()
		if strings.TrimSpace(line.Text) == "11" {
			printedOnce.Do(func() { close(printed) })
		}
	}))
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("place: %v\nagent log:\n%s", err, strings.Join(agentLog, "\n"))
	}
	// The guest builds and runs the program before it sleeps; the exec and
	// the stop follow its result rather than race it.
	select {
	case <-printed:
	case <-time.After(5 * time.Minute):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the guest did not print the arithmetic result; log:\n%s", strings.Join(guestLog, "\n"))
	}

	execCtx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	out, err := handle.Exec(execCtx, []string{"cat", "/etc/hostname"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.TrimSpace(out.Stdout) != "arithmetic" || out.ExitCode != 0 {
		t.Fatalf("exec answered %+v", out)
	}
	hosts, err := handle.Exec(execCtx, []string{"cat", "/etc/hosts"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, want := range []string{"10.79.0.3\tmetadata.example.internal", "10.79.0.1\thost.docker.internal", "10.79.0.10\tarithmetic"} {
		if !strings.Contains(hosts.Stdout, want) {
			t.Fatalf("the guest's hosts file lacks %q:\n%s", want, hosts.Stdout)
		}
	}
	handle.Cancel()
	result := handle.Wait()
	mu.Lock()
	all := strings.Join(guestLog, "\n")
	mu.Unlock()
	if !strings.Contains(all, "11") {
		t.Fatalf("the guest did not print the arithmetic result; exit %d, log:\n%s", result.ExitCode, all)
	}

	// The kept guest boots again on its disk: the program it built is there
	// to run, the console carries both boots, and the disk captures as an
	// image the engine holds.
	id := handle.ID()
	again, err := backend.Restart(t.Context(), id, workload, sim.NoopSink{})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	for i := 0; i < 600; i++ {
		console, err := backend.Console(t.Context(), id)
		if err != nil {
			t.Fatalf("console: %v", err)
		}
		if strings.Count(console, "\n11\n")+boolToInt(strings.HasPrefix(console, "11\n")) >= 2 {
			break
		}
		if i == 599 {
			t.Fatalf("the second boot did not print the arithmetic result; console:\n%s", console)
		}
		time.Sleep(500 * time.Millisecond)
	}
	reference := "sockerless-sim-kvm-capture:" + sim.RandomHex(4)
	if err := backend.Capture(t.Context(), id, reference); err != nil {
		t.Fatalf("capture: %v", err)
	}
	defer func() {
		_, _ = sim.DockerClient().ImageRemove(context.Background(), reference, client.ImageRemoveOptions{Force: true})
	}()
	var capturedLog []string
	captured, err := placement.ContainerBackend{}.Place(t.Context(), placement.Workload{
		Image:        reference,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-captured-" + sim.RandomHex(4),
		Command:      []string{"/work/eval", "5 * 5"},
		Timeout:      time.Minute,
		Sandbox:      sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		capturedLog = append(capturedLog, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatalf("run the captured image: %v", err)
	}
	if result := captured.Wait(); result.ExitCode != 0 {
		t.Fatalf("the captured image's program exited %d", result.ExitCode)
	}
	// The program explains itself on standard error and answers on standard
	// output, and the two streams arrive in no fixed order.
	mu.Lock()
	answered := slices.Contains(capturedLog, "25")
	answer := strings.Join(capturedLog, "\n")
	mu.Unlock()
	if !answered {
		t.Fatalf("the captured image's program answered %q", answer)
	}
	again.Cancel()
	again.Wait()
	if err := backend.Remove(t.Context(), id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	guests, err := agent.Client().List(t.Context())
	if err != nil || len(guests) != 0 {
		t.Fatalf("after the guest was removed the agent still lists %d guests (%v)", len(guests), err)
	}
	links, err := placement.ExecInContainer(t.Context(), agent.ContainerID(), []string{"ip", "-o", "link"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(links.Stdout, "tap") {
		t.Fatalf("a tap outlived its guest:\n%s", links.Stdout)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
