package simulator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/moby/moby/client"
)

// Persistent-workload helpers: reclaiming containers that outlived a
// control-plane process, and the named-volume lifecycle those workloads
// keep their data on. Service slices use the same cloud-resource labels
// they supplied at creation to find their workloads again after restart.

// RequireContainerRuntime reports why an operation that runs a container
// cannot run in this process. Startup refuses to serve when a
// workload-executing mode cannot reach an engine, so a missing client here
// means only one thing: the simulator was deliberately started API-only.
// Naming that says which of the two very different situations the caller is
// in — an engine that went away, or a process that never had one — instead
// of leaving a nil variable to be reported as if it were an engine fault.
func RequireContainerRuntime(operation string) error {
	if dockerClient != nil {
		return nil
	}
	return fmt.Errorf("%s requires a container runtime: this simulator was started API-only (SIM_RUNTIME=process) and holds no container engine client",
		operation)
}

// ExistingContainer describes a workload container that outlived a persistent
// simulator control-plane process.
type ExistingContainer struct {
	ID             string
	Running        bool
	PublishedPorts map[int]int
	Labels         map[string]string
}

// FindExistingContainers returns every container carrying all requested
// labels, including exited containers whose terminal result has not yet been
// reconciled into the durable cloud resource.
func FindExistingContainers(labels map[string]string) ([]ExistingContainer, error) {
	if err := RequireContainerRuntime("listing existing containers"); err != nil {
		return nil, err
	}
	labelFilters := client.Filters{}
	if simulatorStateID != "" {
		labelFilters.Add("label", "sockerless-sim-state="+simulatorStateID)
	}
	for key, value := range labels {
		if value == "" {
			labelFilters.Add("label", key)
		} else {
			labelFilters.Add("label", key+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	summaries, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilters,
	})
	if err != nil {
		return nil, err
	}
	found := make([]ExistingContainer, 0, len(summaries.Items))
	for _, summary := range summaries.Items {
		inspected, err := dockerClient.ContainerInspect(ctx, summary.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		inspection := inspected.Container
		entry := ExistingContainer{
			ID:             summary.ID,
			Running:        inspection.State != nil && inspection.State.Running,
			PublishedPorts: map[int]int{},
			Labels:         map[string]string{},
		}
		if inspection.Config != nil {
			for key, value := range inspection.Config.Labels {
				entry.Labels[key] = value
			}
		}
		if inspection.NetworkSettings != nil {
			for containerPort, bindings := range inspection.NetworkSettings.Ports {
				if len(bindings) == 0 {
					continue
				}
				publicPort, err := strconv.Atoi(bindings[0].HostPort)
				if err == nil {
					entry.PublishedPorts[int(containerPort.Num())] = publicPort
				}
			}
		}
		found = append(found, entry)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, nil
}

// StartExistingContainer resumes an exited persistent workload container
// without replacing it, preserving its filesystem, mounts, identity, and
// port bindings.
func StartExistingContainer(containerID string) error {
	if err := RequireContainerRuntime("starting an existing container"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{})
	return err
}

// AdoptContainer attaches lifecycle observation to a container created by an
// earlier persistent simulator process. It never restarts the workload;
// callers decide whether an exited cloud workload should remain terminal or
// resume.
func AdoptContainer(containerID string, cfg ContainerConfig, sink LogSink) (*ContainerHandle, error) {
	if err := RequireContainerRuntime("adopting a running container"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan ProcessResult, 1)
	managedContainers.Store(containerID, true)
	go func() {
		result := waitAndCaptureLogs(ctx, dockerClient, containerID, cfg, sink)
		managedContainers.Delete(containerID)
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		_, _ = dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true})
		resultCh <- result
	}()
	return &ContainerHandle{
		ContainerID: containerID,
		cancel:      cancel,
		done:        resultCh,
		cli:         dockerClient,
	}, nil
}

// RemoveVolume removes one explicitly named simulator-managed volume.
// Callers own the lifecycle decision; this helper never enumerates or prunes
// unrelated volumes.
func RemoveVolume(name string) error {
	if err := RequireContainerRuntime("removing a volume"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	return err
}

// VolumeExists reports whether a named volume exists on the engine. Callers
// on the modeled tier (no engine) get false, which is the truth there.
func VolumeExists(name string) bool {
	if dockerClient == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dockerClient.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	return err == nil
}
