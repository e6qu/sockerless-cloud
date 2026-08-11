package simulator

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

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

const containerReaperArgument = "--sockerless-container-reaper"

var (
	simulatorRunID    string
	simulatorProvider string
	simulatorStateID  string
)

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
	simulatorProvider = provider
	runID, err := newSimulatorRunID()
	if err != nil {
		return fmt.Errorf("allocate simulator run identifier: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve simulator executable: %w", err)
	}
	simulatorRunID = runID
	command := exec.Command(executable, containerReaperArgument, provider, runID, strconv.Itoa(os.Getpid()))
	realexec.DetachCommand(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start workload cleanup reaper: %w", err)
	}
	return command.Process.Release()
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

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize container reaper runtime: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = dockerClient.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	labelFilters := filters.NewArgs(
		filters.Arg("label", "sockerless-sim-provider="+provider),
		filters.Arg("label", "sockerless-sim-run="+runID),
	)
	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true, Filters: labelFilters})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list orphaned simulator workloads: %v\n", err)
		os.Exit(1)
	}
	for _, workload := range containers {
		timeout := 5
		_ = dockerClient.ContainerStop(ctx, workload.ID, container.StopOptions{Timeout: &timeout})
		if err := dockerClient.ContainerRemove(ctx, workload.ID, container.RemoveOptions{Force: true}); err != nil {
			fmt.Fprintf(os.Stderr, "remove orphaned simulator workload %s: %v\n", workload.ID, err)
			os.Exit(1)
		}
	}
	networks, err := dockerClient.NetworkList(ctx, network.ListOptions{Filters: labelFilters})
	if err != nil {
		fmt.Fprintf(os.Stderr, "list orphaned simulator networks: %v\n", err)
		os.Exit(1)
	}
	for _, workloadNetwork := range networks {
		if err := dockerClient.NetworkRemove(ctx, workloadNetwork.ID); err != nil {
			fmt.Fprintf(os.Stderr, "remove orphaned simulator network %s: %v\n", workloadNetwork.ID, err)
			os.Exit(1)
		}
	}
	return true
}
