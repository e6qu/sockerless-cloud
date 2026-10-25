package sim

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestCreateReclaimsANameADeadSimulatorLeft(t *testing.T) {
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
	name := "sockerless-sim-reclaim-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	left, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: containerReaperTestImage,
			Cmd:   []string{"sleep", "300"},
			Labels: map[string]string{
				"sockerless-sim":      "true",
				"sockerless-sim-run":  "an-earlier-run",
				"sockerless-sim-host": simulatorOwnerHost,
				"sockerless-sim-pid":  strconv.Itoa(gone.Process.Pid),
			},
		},
		Name: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), name, client.ContainerRemoveOptions{Force: true})
	})

	id, _, err := createAndStartContainer(ctx, cli, ContainerConfig{
		Name:         name,
		Image:        containerReaperTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Args:         []string{"sleep", "300"},
	})
	if err != nil {
		t.Fatalf("a name a dead simulator's container held was not reclaimed: %v", err)
	}
	if id == left.ID {
		t.Fatal("the create returned the container the dead simulator left")
	}

	_, _, err = createAndStartContainer(ctx, cli, ContainerConfig{
		Name:         name,
		Image:        containerReaperTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Args:         []string{"sleep", "300"},
	})
	if err == nil {
		t.Fatal("a name this run's own container holds was taken from it")
	}
}
