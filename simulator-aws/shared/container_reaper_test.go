package simulator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
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
	// The child is itself a `go test` process, so anything that goes wrong in
	// it — a refused image pull, a container create the engine rejects — lands
	// on its stdout as test output rather than as the one JSON line this test
	// is waiting for. Read line by line and keep the transcript, so a child
	// that failed reports why instead of surfacing as an opaque decode error.
	var resources containerReaperTestResources
	var transcript []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		transcript = append(transcript, line)
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &resources); err == nil && resources.RunID != "" {
			break
		}
	}
	if resources.RunID == "" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("simulator child did not report the resources it created (scan error: %v):\n%s",
			scanner.Err(), strings.Join(transcript, "\n"))
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill simulator child: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILLed simulator child unexpectedly exited successfully")
	}

	dockerClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerClient.Close()
	// Poll until the reaper has removed both resources. A single query that
	// does not come back inside its own budget is a busy engine, not a verdict
	// on the reaper — the 20-second deadline is what decides this test, so an
	// attempt that errors is retried and only the last error is reported if the
	// deadline runs out.
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		containers, containerErr := dockerClient.ContainerList(ctx, client.ContainerListOptions{
			All:     true,
			Filters: client.Filters{}.Add("label", "sockerless-sim-run="+resources.RunID),
		})
		networks, networkErr := dockerClient.NetworkList(ctx, client.NetworkListOptions{
			Filters: client.Filters{}.Add("label", "sockerless-sim-run="+resources.RunID),
		})
		cancel()
		lastErr = errors.Join(containerErr, networkErr)
		if lastErr == nil && len(containers.Items) == 0 && len(networks.Items) == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("could not confirm the detached reaper removed container %s and network %s: %v",
			resources.ContainerID, resources.NetworkID, lastErr)
	}
	t.Fatalf("detached reaper did not remove container %s and network %s after SIGKILL", resources.ContainerID, resources.NetworkID)
}

// The reaper test pulls a real image, so it depends on a registry being
// reachable from the runner. It reads from the Amazon ECR Public Gallery
// rather than Docker Hub, which is what the rest of the repository does: an
// unauthenticated Hub pull is rate-limited per source address, and a runner
// that has exhausted that budget — or cannot reach Hub at all — fails this
// test for a reason that has nothing to do with the reaper. Both this test and
// the startup sweep lost a CI run to
// `Get "https://registry-1.docker.io/v2/": context deadline exceeded`.
const containerReaperTestImage = "public.ecr.aws/docker/library/alpine:3.22"

func runContainerReaperTestChild(t *testing.T) {
	if err := startContainerReaper("aws"); err != nil {
		t.Fatal(err)
	}
	dockerClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerClient.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pull, err := dockerClient.ImagePull(ctx, containerReaperTestImage, client.ImagePullOptions{})
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
	createdNetwork, err := dockerClient.NetworkCreate(ctx, "sockerless-reaper-test-"+simulatorRunID, client.NetworkCreateOptions{
		Labels: labels,
	})
	if err != nil {
		t.Fatalf("create reaper test network: %v", err)
	}
	createdContainer, err := dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  containerReaperTestImage,
			Cmd:    []string{"sleep", "300"},
			Labels: labels,
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(createdNetwork.ID)},
		Name:       "sockerless-reaper-test-" + simulatorRunID,
	})
	if err != nil {
		t.Fatalf("create reaper test container: %v", err)
	}
	if _, err := dockerClient.ContainerStart(ctx, createdContainer.ID, client.ContainerStartOptions{}); err != nil {
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
