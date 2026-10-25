package sim

import (
	"context"
	"net/netip"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// A dead run's network that its own leftover container still joins blocks
// the range as surely as an empty one, and is reclaimed with that container;
// a network a live container joins is kept.
func TestReclaimOrphanedRangeRemovesADeadRunsAttachedContainers(t *testing.T) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := pullImage(ctx, cli, containerReaperTestImage, "", ""); err != nil {
		t.Fatal(err)
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	deadOwner := map[string]string{
		"sockerless-sim":      "true",
		"sockerless-sim-run":  "an-earlier-run",
		"sockerless-sim-host": simulatorOwnerHost,
		"sockerless-sim-pid":  strconv.Itoa(gone.Process.Pid),
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	place := func(cidr netip.Prefix, containerLabels map[string]string) (string, string) {
		t.Helper()
		created, err := cli.NetworkCreate(ctx, "sockerless-sim-reclaim-"+cidr.Addr().String()+"-"+suffix, client.NetworkCreateOptions{
			Driver: "bridge",
			Labels: deadOwner,
			IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: cidr}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), created.ID, client.NetworkRemoveOptions{}) })
		attached, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config:     &container.Config{Image: containerReaperTestImage, Cmd: []string{"sleep", "300"}, Labels: containerLabels},
			HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(created.ID)},
			Name:       "sockerless-sim-reclaim-" + cidr.Addr().String() + "-" + suffix,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = cli.ContainerRemove(context.Background(), attached.ID, client.ContainerRemoveOptions{Force: true})
		})
		if _, err := cli.ContainerStart(ctx, attached.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		return created.ID, attached.ID
	}

	orphanRange := netip.MustParsePrefix("10.251.17.0/29")
	orphanNet, orphanContainer := place(orphanRange, deadOwner)
	liveRange := netip.MustParsePrefix("10.251.18.0/29")
	liveNet, _ := place(liveRange, map[string]string{"owned-by": "someone else"})

	if !reclaimOrphanedRange(ctx, cli, orphanRange) {
		t.Fatal("the dead run's network was not reclaimed")
	}
	if _, err := cli.NetworkInspect(ctx, orphanNet, client.NetworkInspectOptions{}); err == nil {
		t.Error("the dead run's network is still there")
	}
	if _, err := cli.ContainerInspect(ctx, orphanContainer, client.ContainerInspectOptions{}); err == nil {
		t.Error("the dead run's container is still there")
	}

	if reclaimOrphanedRange(ctx, cli, liveRange) {
		t.Fatal("a network another container still joins was reclaimed")
	}
	if _, err := cli.NetworkInspect(ctx, liveNet, client.NetworkInspectOptions{}); err != nil {
		t.Errorf("the joined network is gone: %v", err)
	}
}
