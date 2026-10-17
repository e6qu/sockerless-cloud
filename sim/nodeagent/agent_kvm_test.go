//go:build kvm

package nodeagent_test

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const guestImage = "mirror.gcr.io/library/golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d"

// The agent's proof, on a host with /dev/kvm: the agent starts as a container
// on an engine network, a guest built from an OCI image boots on that network,
// builds and runs the repository's arithmetic program as its first process,
// answers a command over the agent, and leaves no guest, tap or disk behind.
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
	handle, err := backend.Place(t.Context(), placement.Workload{
		Image:        guestImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-arithmetic",
		Hostname:     "arithmetic",
		Network:      networkName,
		IPv4Address:  "10.79.0.10",
		Command:      []string{"/bin/sh", "-c"},
		Args:         []string{"cd /work && go mod init arithmetic >/dev/null && go build -o eval . && ./eval '3 + 4 * 2' && hostname && sleep 30"},
		Env:          map[string]string{"GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
		Seed:         []sim.SeedFile{{Path: "/work/main.go", Data: source}},
		MemoryBytes:  1024 << 20,
		NanoCPU:      2_000_000_000,
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		guestLog = append(guestLog, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("place: %v\nagent log:\n%s", err, strings.Join(agentLog, "\n"))
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
	handle.Cancel()
	result := handle.Wait()
	mu.Lock()
	all := strings.Join(guestLog, "\n")
	mu.Unlock()
	if !strings.Contains(all, "11") {
		t.Fatalf("the guest did not print the arithmetic result; exit %d, log:\n%s", result.ExitCode, all)
	}
	guests, err := agent.Client().List(t.Context())
	if err != nil || len(guests) != 0 {
		t.Fatalf("after the guest ended the agent still lists %d guests (%v)", len(guests), err)
	}
	links, err := placement.ExecInContainer(t.Context(), agent.ContainerID(), []string{"ip", "-o", "link"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(links.Stdout, "tap") {
		t.Fatalf("a tap outlived its guest:\n%s", links.Stdout)
	}
}
