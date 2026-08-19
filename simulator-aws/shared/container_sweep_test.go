package simulator

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The detached reaper is the fast path, and it is not the only one.
//
// TestContainerReaperAbnormalExit proves the reaper collects its run when the
// simulator is SIGKILLed. What it cannot prove is the case that actually leaked:
// the reaper dying too. It lives inside the harness container the simulator runs
// in, so a harness that removes that container removes the reaper with it, and
// the run's workloads then belong to nobody. Twenty-two of them were found on
// one development host, five still running between two and twenty-five hours
// after the runs that made them had ended.
//
// So the next simulator over the same state directory collects what the last
// one left. This drives that path with a real engine: a process creates a
// workload under a state directory with no reaper behind it, is SIGKILLed, and
// the next run over that state directory removes what it left — while a
// workload under a different state directory is untouched, which is what keeps
// three suites of one cloud running at once from collecting each other.

const containerSweepTestChild = "SOCKERLESS_CONTAINER_SWEEP_TEST_CHILD"
const containerSweepTestStateDir = "SOCKERLESS_CONTAINER_SWEEP_TEST_STATE_DIR"

type containerSweepTestResources struct {
	RunID       string `json:"runId"`
	ContainerID string `json:"containerId"`
	NetworkID   string `json:"networkId"`
}

func TestStartupSweepCollectsAKilledRunsWorkloads(t *testing.T) {
	if os.Getenv(containerSweepTestChild) == "1" {
		runContainerSweepTestChild(t)
		return
	}

	// The identity is process-wide, and this test takes it over to become the
	// run that sweeps.
	runID, stateID, provider := simulatorRunID, simulatorStateID, simulatorProvider
	t.Cleanup(func() { simulatorRunID, simulatorStateID, simulatorProvider = runID, stateID, provider })

	stateDir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestStartupSweepCollectsAKilledRunsWorkloads$")
	command.Env = append(os.Environ(),
		containerSweepTestChild+"=1",
		containerSweepTestStateDir+"="+stateDir)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var abandoned containerSweepTestResources
	var transcript []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		transcript = append(transcript, line)
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &abandoned); err == nil && abandoned.RunID != "" {
			break
		}
	}
	if abandoned.RunID == "" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("the abandoned run did not report what it created (scan error: %v):\n%s",
			scanner.Err(), strings.Join(transcript, "\n"))
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill the run that leaves the workload behind: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("the SIGKILLed run unexpectedly exited successfully")
	}

	engine, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// A workload of another simulator, over a different state directory. The
	// sweep must leave it alone: on CI the SDK, CLI and Terraform suites of one
	// cloud run at the same time.
	configureSimulatorIdentity("aws", t.TempDir())
	simulatorRunID = "00000000000000000000000000000000"
	foreign := createSweepTestContainer(t, ctx, engine, "sockerless-sweep-test-bystander-"+abandoned.RunID)
	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer removeCancel()
		_, _ = engine.ContainerRemove(removeCtx, foreign, client.ContainerRemoveOptions{Force: true})
	})

	// This process becomes the next run over the abandoned run's state.
	configureSimulatorIdentity("aws", stateDir)
	nextRun, err := newSimulatorRunID()
	if err != nil {
		t.Fatal(err)
	}
	simulatorRunID = nextRun
	sweepOrphanedWorkloads(engine)

	remaining, err := engine.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "sockerless-sim-run="+abandoned.RunID),
	})
	if err != nil {
		t.Fatalf("list the abandoned run's workloads: %v", err)
	}
	if len(remaining.Items) != 0 {
		t.Errorf("the next run over the same state left %d workload(s) of the killed run behind (container %s)",
			len(remaining.Items), abandoned.ContainerID)
	}
	networks, err := engine.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "sockerless-sim-run="+abandoned.RunID),
	})
	if err != nil {
		t.Fatalf("list the abandoned run's networks: %v", err)
	}
	if len(networks.Items) != 0 {
		t.Errorf("the next run left network %s of the killed run behind", abandoned.NetworkID)
	}

	if _, err := engine.ContainerInspect(ctx, foreign, client.ContainerInspectOptions{}); err != nil {
		t.Errorf("the sweep removed a workload belonging to a simulator over a different state directory: %v", err)
	}
}

// createSweepTestContainer creates one stopped container carrying whatever
// identity is configured now, and returns its id.
func createSweepTestContainer(t *testing.T, ctx context.Context, engine *client.Client, name string) string {
	t.Helper()
	created, err := engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  containerSweepTestImage,
			Cmd:    []string{"sleep", "300"},
			Labels: simulatorLabels(map[string]string{"sockerless-sweep-test": "true"}),
		},
		Name: name,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return created.ID
}

const containerSweepTestImage = "docker.io/library/alpine:3.22"

// runContainerSweepTestChild is a run with no reaper behind it — the state a
// harness leaves when it removes the container the simulator and its reaper
// both lived in. It creates a workload and a network under the state directory
// it was given and then waits to be killed.
func runContainerSweepTestChild(t *testing.T) {
	stateDir := os.Getenv(containerSweepTestStateDir)
	if stateDir == "" {
		t.Fatal("the abandoned run was given no state directory")
	}
	configureSimulatorIdentity("aws", stateDir)
	runID, err := newSimulatorRunID()
	if err != nil {
		t.Fatal(err)
	}
	simulatorRunID = runID

	engine, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pull, err := engine.ImagePull(ctx, containerSweepTestImage, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull the sweep test image: %v", err)
	}
	if _, err := io.Copy(io.Discard, pull); err != nil {
		_ = pull.Close()
		t.Fatalf("read the sweep test image pull: %v", err)
	}
	if err := pull.Close(); err != nil {
		t.Fatalf("close the sweep test image pull: %v", err)
	}

	labels := simulatorLabels(map[string]string{"sockerless-sweep-test": "true"})
	createdNetwork, err := engine.NetworkCreate(ctx, "sockerless-sweep-test-"+simulatorRunID,
		client.NetworkCreateOptions{Labels: labels})
	if err != nil {
		t.Fatalf("create the sweep test network: %v", err)
	}
	createdContainer, err := engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  containerSweepTestImage,
			Cmd:    []string{"sleep", "300"},
			Labels: labels,
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(createdNetwork.ID)},
		Name:       "sockerless-sweep-test-" + simulatorRunID,
	})
	if err != nil {
		t.Fatalf("create the sweep test container: %v", err)
	}
	if _, err := engine.ContainerStart(ctx, createdContainer.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start the sweep test container: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(containerSweepTestResources{
		RunID:       simulatorRunID,
		ContainerID: createdContainer.ID,
		NetworkID:   createdNetwork.ID,
	}); err != nil {
		t.Fatalf("report what the abandoned run created: %v", err)
	}
	if err := os.Stdout.Sync(); err != nil {
		t.Fatalf("flush what the abandoned run created: %v", err)
	}
	select {}
}
