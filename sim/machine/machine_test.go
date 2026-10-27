package machine

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

const testImage = "mirror.gcr.io/library/busybox:latest"

// harness is a container network and a manager whose hooks record what the
// machines did on their own account.
type harness struct {
	t       *testing.T
	network string
	next    netip.Addr
	manager *Manager

	mu     sync.Mutex
	events []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sim.PullImage(ctx, testImage, "linux/"+runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-machines-" + sim.RandomHex(4)
	netID, cidr, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{}) })
	h := &harness{t: t, network: network, next: cidr.Masked().Addr()}
	for range 10 {
		h.next = h.next.Next()
	}
	h.manager = New(placement.ContainerBackend{}, h.hooks())
	return h
}

func (h *harness) hooks() Hooks {
	record := func(event string) func(string) {
		return func(key string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.events = append(h.events, event+" "+key)
		}
	}
	return Hooks{Booting: record("booting"), Ready: record("ready"), Down: record("down"),
		GuestShutdown: record("shutdown"), Forgotten: record("forgotten")}
}

func (h *harness) saw(event string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.events {
		if e == event {
			return true
		}
	}
	return false
}

// workload is a busybox machine running script at every boot.
func (h *harness) workload(key, script string) placement.Workload {
	h.t.Helper()
	h.next = h.next.Next()
	w, err := vminstance.Workload(vminstance.Spec{
		Name:      "sockerless-sim-test-machine-" + sim.RandomHex(4),
		ID:        key,
		Hostname:  key,
		Image:     vminstance.Image{OCI: testImage, Architecture: "linux/" + runtime.GOARCH},
		Network:   h.network,
		Address:   h.next.String(),
		MAC:       ipam.DeriveMAC("02:0a:ec", key),
		Startup:   &vminstance.Startup{Path: "/etc/startup.sh", Script: []byte("#!/bin/sh\n" + script + "\n")},
		VCPUs:     1,
		MemoryMiB: 64,
		StopGrace: time.Second,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = h.manager.Discard(context.Background(), key) })
	return w
}

func (h *harness) exec(key, command string) string {
	h.t.Helper()
	handle, ok := h.manager.Handle(key)
	if !ok {
		h.t.Fatalf("%s runs no machine", key)
	}
	result, err := handle.Exec(context.Background(), []string{"/bin/sh", "-c", command})
	if err != nil {
		h.t.Fatal(err)
	}
	return result.Stdout
}

// A machine boots ready, stops keeping its disk, boots the same disk again,
// keeps one console across both boots, and is gone once discarded.
func TestBootStopStartKeepsTheDiskAndTheConsole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	w := h.workload("m-cycle", "echo boot >> /var/boots; echo \"booted $(wc -l < /var/boots)\"")
	if err := h.manager.Boot(ctx, "m-cycle", w); err != nil {
		t.Fatal(err)
	}
	if !h.saw("booting m-cycle") || !h.saw("ready m-cycle") {
		t.Fatalf("hooks %v", h.events)
	}
	if running := h.manager.Running(); len(running) != 1 || running[0].Key != "m-cycle" {
		t.Fatalf("running %v", running)
	}
	if err := h.manager.Halt(ctx, "m-cycle"); err != nil {
		t.Fatal(err)
	}
	if _, running := h.manager.Handle("m-cycle"); running {
		t.Fatal("a stopped machine still runs")
	}
	if _, kept := h.manager.DiskID("m-cycle"); !kept {
		t.Fatal("a stopped machine's disk was not kept")
	}
	if h.saw("shutdown m-cycle") {
		t.Fatal("a requested stop was taken for the guest shutting down")
	}
	if err := h.manager.Boot(ctx, "m-cycle", w); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(h.exec("m-cycle", "wc -l < /var/boots")); got != "2" {
		t.Fatalf("the restarted disk holds %s boots, want 2", got)
	}
	if got, want := strings.TrimSpace(h.exec("m-cycle", "cat /sys/class/net/eth0/address")), ipam.DeriveMAC("02:0a:ec", "m-cycle"); got != want {
		t.Fatalf("the interface's MAC is %s, want the assigned %s", got, want)
	}
	console, err := h.manager.Console(ctx, "m-cycle")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(console, "booted 1") || !strings.Contains(console, "booted 2") {
		t.Fatalf("console %q", console)
	}
	if err := h.manager.Discard(ctx, "m-cycle"); err != nil {
		t.Fatal(err)
	}
	if _, kept := h.manager.DiskID("m-cycle"); kept || !h.saw("forgotten m-cycle") {
		t.Fatal("a discarded machine is still held")
	}
}

// A guest that shuts itself down is reported through GuestShutdown.
func TestAGuestThatShutsDownIsReported(t *testing.T) {
	h := newHarness(t)
	w := h.workload("m-shutdown", "(sleep 1; kill -TERM 1) &")
	if err := h.manager.Boot(context.Background(), "m-shutdown", w); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !h.saw("shutdown m-shutdown") {
		if time.Now().After(deadline) {
			t.Fatalf("no guest shutdown reported: %v", h.events)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, kept := h.manager.DiskID("m-shutdown"); !kept {
		t.Fatal("the shut-down machine's disk was not kept")
	}
}

// A stop while the machine boots ends the boot with ErrHalted at once.
func TestAStopDuringABootWins(t *testing.T) {
	h := newHarness(t)
	w := h.workload("m-halt", "sleep 300")
	booted := make(chan error, 1)
	go func() { booted <- h.manager.Boot(context.Background(), "m-halt", w) }()
	for !h.saw("booting m-halt") {
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.manager.Halt(context.Background(), "m-halt"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-booted:
		if !errors.Is(err, ErrHalted) {
			t.Fatalf("boot returned %v, want ErrHalted", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the stop waited the boot out")
	}
	if _, running := h.manager.Handle("m-halt"); running {
		t.Fatal("the stopped machine runs")
	}
}

// A machine that is not ready within the boot's deadline stops with its disk
// kept.
func TestABootThatNeverBecomesReadyKeepsTheDisk(t *testing.T) {
	h := newHarness(t)
	w := h.workload("m-slow", "sleep 300")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := h.manager.Boot(ctx, "m-slow", w)
	var notReady *NotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("boot returned %v, want a NotReadyError", err)
	}
	if _, running := h.manager.Handle("m-slow"); running {
		t.Fatal("the machine that never became ready still runs")
	}
	if _, kept := h.manager.DiskID("m-slow"); !kept {
		t.Fatal("the disk was not kept")
	}
	if h.saw("shutdown m-slow") {
		t.Fatal("a machine stopped for never becoming ready was taken for its guest shutting down")
	}
}

// A machine discarded while its boot still places it leaves nothing behind.
func TestADiscardDuringABootLeavesNoMachine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	w := h.workload("m-discard", "sleep 300")
	booted := make(chan error, 1)
	go func() { booted <- h.manager.Boot(ctx, "m-discard", w) }()
	for !h.saw("booting m-discard") {
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.manager.Discard(ctx, "m-discard"); err != nil {
		t.Fatal(err)
	}
	if err := <-booted; !errors.Is(err, ErrHalted) {
		t.Fatalf("boot returned %v, want ErrHalted", err)
	}
	left, err := h.manager.Find(ctx, map[string]string{"sockerless-sim-instance": "m-discard"})
	if err != nil || len(left) != 0 {
		t.Fatalf("the discarded machine is still held: %v %v", left, err)
	}
}

// A new manager, as a restarted simulator holds, finds the running machine
// by its label, takes it over and sees it ready; capture stores its disk.
func TestANewManagerAdoptsARunningMachineAndCapturesIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	w := h.workload("m-adopt", "echo adopted > /var/marker")
	if err := h.manager.Boot(ctx, "m-adopt", w); err != nil {
		t.Fatal(err)
	}
	restarted := New(placement.ContainerBackend{}, h.hooks())
	found, err := restarted.Find(ctx, map[string]string{"sockerless-sim-instance": "m-adopt"})
	if err != nil || len(found) != 1 {
		t.Fatalf("find: %v %v", found, err)
	}
	state, ready, err := restarted.Adopt(ctx, "m-adopt", &found[0], w)
	if err != nil || state != Up {
		t.Fatalf("adopt: %v %v", state, err)
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	h.manager = restarted
	if got := strings.TrimSpace(h.exec("m-adopt", "cat /var/marker")); got != "adopted" {
		t.Fatalf("marker %q", got)
	}
	reference := "sockerless-sim-test-capture:" + sim.RandomHex(4)
	if err := restarted.Capture(ctx, "m-adopt", reference); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().ImageRemove(ctx, reference, client.ImageRemoveOptions{Force: true}) })
	if _, err := sim.DockerClient().ImageInspect(ctx, reference); err != nil {
		t.Fatalf("the captured image is missing: %v", err)
	}
	if found, _, err := restarted.Adopt(ctx, "m-gone", nil, w); err != nil || found != Gone {
		t.Fatalf("adopting nothing: %v %v", found, err)
	}
}
