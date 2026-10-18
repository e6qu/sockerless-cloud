package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// A site container that stops before the start reads its published port has
// no address to ping: the start reports that it exited, with its exit status,
// rather than that it publishes no port.
func TestSiteContainerThatExitsBeforeItsPortIsReadReportsItsExit(t *testing.T) {
	if _, err := sim.InitDocker("azure", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	const image = "mirror.gcr.io/library/busybox:latest"
	platform, err := workload.LocalImagePlatform(t.Context(), image, "")
	if err != nil {
		t.Fatal(err)
	}
	network := fmt.Sprintf("sockerless-sim-azure-site-exit-%d", time.Now().UnixNano())
	if _, err := sim.EnsureDockerNetwork(network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.RemoveDockerNetwork(network) })
	handle, err := placement.ContainerBackend{}.Place(t.Context(), placement.Workload{
		Image:        image,
		Architecture: platform,
		Command:      []string{"sh", "-c", "exit 3"},
		Name:         fmt.Sprintf("sockerless-sim-azure-site-exit-%d", time.Now().UnixNano()),
		Network:      network,
		PublishPorts: []int{80},
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(handle.Cancel)
	exit := newContainerExit(handle)
	<-exit.done

	candidates, err := siteContainerCandidates(t.Context(), handle.ID(), network, 80)
	if err != nil {
		t.Fatalf("an exited container's candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("an exited container has candidates %v", candidates)
	}
	want := `site "exit-site" container exited before it answered on port 80: exit code 3`
	if got := exit.exitedBeforeAnswering("exit-site", 80).Error(); got != want {
		t.Fatalf("start failure %q, want %q", got, want)
	}
}
