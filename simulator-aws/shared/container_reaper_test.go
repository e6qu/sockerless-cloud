package simulator

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

const containerReaperTestChild = "SOCKERLESS_CONTAINER_REAPER_TEST_CHILD"

func TestMain(m *testing.M) {
	if RunContainerReaper() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type containerReaperTestResources struct {
	RunID       string `json:"runId"`
	ContainerID string `json:"containerId"`
	NetworkID   string `json:"networkId"`
}

func TestContainerReaperAbnormalExit(t *testing.T) {
	if os.Getenv(containerReaperTestChild) == "1" {
		runContainerReaperTestChild(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestContainerReaperAbnormalExit$")
	command.Env = append(os.Environ(), containerReaperTestChild+"=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var resources containerReaperTestResources
	if err := json.NewDecoder(bufio.NewReader(stdout)).Decode(&resources); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("read child resources: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill simulator child: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILLed simulator child unexpectedly exited successfully")
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer dockerClient.Close()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		containers, containerErr := dockerClient.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters.NewArgs(filters.Arg("label", "sockerless-sim-run="+resources.RunID)),
		})
		networks, networkErr := dockerClient.NetworkList(ctx, network.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", "sockerless-sim-run="+resources.RunID)),
		})
		cancel()
		if containerErr != nil || networkErr != nil {
			t.Fatalf("query reaper resources: containers=%v networks=%v", containerErr, networkErr)
		}
		if len(containers) == 0 && len(networks) == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("detached reaper did not remove container %s and network %s after SIGKILL", resources.ContainerID, resources.NetworkID)
}

func runContainerReaperTestChild(t *testing.T) {
	if err := startContainerReaper("aws"); err != nil {
		t.Fatal(err)
	}
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer dockerClient.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pull, err := dockerClient.ImagePull(ctx, "docker.io/library/alpine:3.22", image.PullOptions{})
	if err != nil {
		t.Fatalf("pull reaper test image: %v", err)
	}
	if _, err := io.Copy(io.Discard, pull); err != nil {
		_ = pull.Close()
		t.Fatalf("read reaper test image pull: %v", err)
	}
	if err := pull.Close(); err != nil {
		t.Fatalf("close reaper test image pull: %v", err)
	}
	labels := simulatorLabels(map[string]string{"sockerless-reaper-test": "true"})
	createdNetwork, err := dockerClient.NetworkCreate(ctx, "sockerless-reaper-test-"+simulatorRunID, network.CreateOptions{
		Labels: labels,
	})
	if err != nil {
		t.Fatalf("create reaper test network: %v", err)
	}
	createdContainer, err := dockerClient.ContainerCreate(ctx,
		&container.Config{
			Image:  "docker.io/library/alpine:3.22",
			Cmd:    []string{"sleep", "300"},
			Labels: labels,
		},
		&container.HostConfig{NetworkMode: container.NetworkMode(createdNetwork.ID)},
		nil,
		nil,
		"sockerless-reaper-test-"+simulatorRunID,
	)
	if err != nil {
		t.Fatalf("create reaper test container: %v", err)
	}
	if err := dockerClient.ContainerStart(ctx, createdContainer.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start reaper test container: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(containerReaperTestResources{
		RunID:       simulatorRunID,
		ContainerID: createdContainer.ID,
		NetworkID:   createdNetwork.ID,
	}); err != nil {
		t.Fatalf("report child resources: %v", err)
	}
	if err := os.Stdout.Sync(); err != nil {
		t.Fatalf("flush child resources: %v", err)
	}
	select {}
}
