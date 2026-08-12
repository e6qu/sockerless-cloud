package simulator

import (
	"context"
	"crypto/rand"
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
)

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
