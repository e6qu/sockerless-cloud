package sim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/moby/moby/client"
)

const containerReaperArgument = "--sockerless-container-reaper"

var (
	simulatorRunID    string
	simulatorProvider string
	simulatorStateID  string
)

// configureSimulatorIdentity records what identifies this simulator to the
// workload sweep. The state directory is the identity that matters: two
// simulators over one state directory are not two runs, so a run may collect
// what an earlier run over the same state left behind, and may never touch a
// concurrent suite's workloads.
func configureSimulatorIdentity(provider, stateDir string) {
	simulatorProvider = provider
	if stateDir == "" {
		simulatorStateID = ""
		return
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + stateDir))
	simulatorStateID = hex.EncodeToString(digest[:])
}

func newSimulatorRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func simulatorLabels(extra map[string]string) map[string]string {
	labels := map[string]string{
		"sockerless-sim":          "true",
		"sockerless-sim-provider": simulatorProvider,
		"sockerless-sim-run":      simulatorRunID,
	}
	if simulatorStateID != "" {
		labels["sockerless-sim-state"] = simulatorStateID
	}
	for key, value := range extra {
		labels[key] = value
	}
	return labels
}

func startContainerReaper(provider string) error {
	runID, err := newSimulatorRunID()
	if err != nil {
		return fmt.Errorf("allocate simulator run identifier: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve simulator executable: %w", err)
	}
	simulatorProvider = provider
	simulatorRunID = runID
	command := exec.Command(executable, containerReaperArgument, provider, runID, strconv.Itoa(os.Getpid()))
	realexec.DetachCommand(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start workload cleanup reaper: %w", err)
	}
	return command.Process.Release()
}

// A run's workloads are collectable from their labels alone.
//
// The reaper above is a detached child that waits for its parent and then
// collects that run. It is the fast path and it is not enough: the child dies
// with the harness container it lives in, and it dies with the machine. Runs
// were observed leaking workloads for two days — twenty-two containers, five of
// them still running between two and twenty-five hours after the runs that made
// them ended — which consumes the host continuously and accumulates into the
// engine state that makes `ContainerList(All: true)` fail outright.
//
// So the next simulator over the same state collects what the last one left. A
// simulator's state directory is the one thing that cannot be shared: two
// simulators over one state directory are not two runs, they are a mistake.
// Sweeping by that identity is therefore precise — it never touches a
// concurrent suite's workloads, which is what a sweep by provider alone would
// do, and CI runs the SDK, CLI and Terraform suites of one cloud at once.
//
// A run with no state directory is not swept, because nothing identifies its
// successor; its detached reaper stays its only collector.
func sweepOrphanedWorkloads(engine *client.Client) {
	if engine == nil || simulatorStateID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	filters := client.Filters{}.Add("label", "sockerless-sim-state="+simulatorStateID)
	containers, err := engine.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweep workloads left by an earlier run: %v\n", err)
		return
	}
	for _, workload := range containers.Items {
		if workload.Labels["sockerless-sim-run"] == simulatorRunID {
			continue
		}
		timeout := 5
		_, _ = engine.ContainerStop(ctx, workload.ID, client.ContainerStopOptions{Timeout: &timeout})
		if _, err := engine.ContainerRemove(ctx, workload.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			// One container that will not go is no reason to leave the rest.
			fmt.Fprintf(os.Stderr, "remove workload %s left by run %s: %v\n",
				workload.ID, workload.Labels["sockerless-sim-run"], err)
		}
	}
	networks, err := engine.NetworkList(ctx, client.NetworkListOptions{Filters: filters})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweep networks left by an earlier run: %v\n", err)
		return
	}
	for _, workloadNetwork := range networks.Items {
		if workloadNetwork.Labels["sockerless-sim-run"] == simulatorRunID {
			continue
		}
		if _, err := engine.NetworkRemove(ctx, workloadNetwork.ID, client.NetworkRemoveOptions{}); err != nil {
			fmt.Fprintf(os.Stderr, "remove network %s left by run %s: %v\n",
				workloadNetwork.ID, workloadNetwork.Labels["sockerless-sim-run"], err)
		}
	}
}

// RunContainerReaper handles the detached cleanup mode before a simulator
// initializes its API server. It returns false for an ordinary invocation.
func RunContainerReaper() bool {
	if len(os.Args) < 2 || os.Args[1] != containerReaperArgument {
		return false
	}
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "invalid container reaper arguments")
		os.Exit(2)
	}
	provider, runID := os.Args[2], os.Args[3]
	parentPID, err := strconv.Atoi(os.Args[4])
	if err != nil || parentPID <= 0 || len(runID) != 32 || strings.Trim(runID, "0123456789abcdef") != "" {
		fmt.Fprintln(os.Stderr, "invalid container reaper identity")
		os.Exit(2)
	}
	for realexec.ProcessAlive(parentPID) {
		time.Sleep(250 * time.Millisecond)
	}

	dockerClient, err := client.New(client.FromEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize container reaper runtime: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = dockerClient.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	labelFilters := client.Filters{}.
		Add("label", "sockerless-sim-provider="+provider).
		Add("label", "sockerless-sim-run="+runID)
	containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: labelFilters})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list orphaned simulator workloads: %v\n", err)
		os.Exit(1)
	}
	for _, workload := range containers.Items {
		timeout := 5
		_, _ = dockerClient.ContainerStop(ctx, workload.ID, client.ContainerStopOptions{Timeout: &timeout})
		if _, err := dockerClient.ContainerRemove(ctx, workload.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			fmt.Fprintf(os.Stderr, "remove orphaned simulator workload %s: %v\n", workload.ID, err)
			os.Exit(1)
		}
	}
	networks, err := dockerClient.NetworkList(ctx, client.NetworkListOptions{Filters: labelFilters})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list orphaned simulator networks: %v\n", err)
		os.Exit(1)
	}
	for _, workloadNetwork := range networks.Items {
		if _, err := dockerClient.NetworkRemove(ctx, workloadNetwork.ID, client.NetworkRemoveOptions{}); err != nil {
			fmt.Fprintf(os.Stderr, "remove orphaned simulator network %s: %v\n", workloadNetwork.ID, err)
			os.Exit(1)
		}
	}
	return true
}
