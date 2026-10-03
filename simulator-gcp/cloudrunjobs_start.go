package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// cloudRunContainerStartOrder orders the template's containers so each comes
// after every container its dependsOn names, keeping the declared order among
// containers free to start. It refuses a dependsOn that names no other
// container of the template and a cycle.
func cloudRunContainerStartOrder(containers []Container) ([]int, error) {
	index := make(map[string]int, len(containers))
	for i, c := range containers {
		if c.Name != "" {
			index[c.Name] = i
		}
	}
	waiting := make([]int, len(containers))
	dependents := make([][]int, len(containers))
	for i, c := range containers {
		for _, dep := range c.DependsOn {
			j, ok := index[dep]
			if !ok || j == i {
				return nil, fmt.Errorf("containers[%d].dependsOn names %q, which is not another container of the template", i, dep)
			}
			waiting[i]++
			dependents[j] = append(dependents[j], i)
		}
	}
	order := make([]int, 0, len(containers))
	started := make([]bool, len(containers))
	for len(order) < len(containers) {
		next := -1
		for i := range containers {
			if !started[i] && waiting[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			return nil, fmt.Errorf("the containers' dependsOn form a cycle")
		}
		started[next] = true
		order = append(order, next)
		for _, d := range dependents[next] {
			waiting[d]--
		}
	}
	return order, nil
}

// cloudRunJobTemplateValid answers INVALID_ARGUMENT for a job template whose
// containers' dependencies name no container of the template or form a cycle.
// A Knative body carries the dependencies in an annotation, which knative
// moves onto the containers first.
func cloudRunJobTemplateValid(w http.ResponseWriter, template *ExecutionTemplate, knative bool) bool {
	if template == nil {
		return true
	}
	if knative {
		if err := cloudRunV2ContainerDependencies(template); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return false
		}
	}
	if template.Template == nil {
		return true
	}
	if _, err := cloudRunContainerStartOrder(template.Template.Containers); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return false
	}
	return true
}

// crjStartups holds, per execution, the cancel of the start of its containers
// while dependencies' startup probes still hold some of them back.
var crjStartups sync.Map // map[execName]context.CancelFunc

// stopCloudRunExecutionRun stops whatever an execution still runs: the start
// of containers waiting on a dependency, and the containers already running.
func stopCloudRunExecutionRun(execName string) {
	if v, ok := crjStartups.Load(execName); ok {
		if cancel, ok := v.(context.CancelFunc); ok {
			cancel()
		}
	}
	if v, ok := crjProcessHandles.Load(execName); ok {
		if group, ok := v.(*workload.Group); ok {
			stopCloudRunExecutionWorkload(group)
		}
	}
}

// startCloudRunJobContainers starts the task's containers in their dependsOn
// order: a container starts once every container it depends on has started
// and passed its startup probe, and every container that configures a startup
// probe must pass it before the task runs on. The containers share the network
// namespace of the first to start. The release it returns ends the ingestion
// of writes through their Cloud Storage volumes, once the containers have
// stopped.
func startCloudRunJobContainers(ctx context.Context, execID, execShort string, taskTmpl *TaskTemplate, timeout time.Duration, sink sim.LogSink) (*workload.Group, func(), error) {
	if taskTmpl == nil || len(taskTmpl.Containers) == 0 {
		return nil, nil, fmt.Errorf("execution has no containers")
	}
	containers := taskTmpl.Containers
	order, err := cloudRunContainerStartOrder(containers)
	if err != nil {
		return nil, nil, err
	}

	volByName := make(map[string]Volume)
	for _, v := range taskTmpl.Volumes {
		volByName[v.Name] = v
	}
	project := resourceProject(execID)
	metadataEnv, err := hostMetadataEnv()
	if err != nil {
		return nil, nil, err
	}
	extraHosts, err := hostMetadataExtraHosts()
	if err != nil {
		return nil, nil, err
	}
	members := make([]workload.Container, len(containers))
	var writable []string
	for i, c := range containers {
		image := sim.ResolveLocalImage(c.Image)
		registryAuth, err := workloadRegistryAuth(project, image)
		if err != nil {
			return nil, nil, err
		}
		binds, containerWritable, err := cloudRunGCSBinds(volByName, c)
		if err != nil {
			return nil, nil, err
		}
		writable = append(writable, containerWritable...)
		cmdEnv := make(map[string]string, len(c.Env))
		for _, ev := range c.Env {
			cmdEnv[ev.Name] = ev.Value
		}
		name := fmt.Sprintf("sockerless-sim-gcp-job-%s", execShort)
		if i > 0 {
			name = fmt.Sprintf("sockerless-sim-gcp-job-%s-sidecar-%d", execShort, i-1)
		}
		members[i] = workload.Container{Name: c.Name, Config: sim.ContainerConfig{
			CancelGracePeriod: cloudRunStopGrace,
			Image:             image,
			RegistryAuth:      registryAuth,
			Command:           c.Command,
			Args:              c.Args,
			Env:               workloadhost.MergeEnv(cmdEnv, metadataEnv),
			Timeout:           timeout,
			Name:              name,
			Labels: map[string]string{
				"sockerless-sim-execution":           execID,
				"sockerless-sim-execution-container": c.Name,
			},
			Binds:   binds,
			Sandbox: SandboxCloudRun,
		}}
	}
	releaseMounts, err := gcsAcquireMounts(writable)
	if err != nil {
		return nil, nil, err
	}

	handles := make([]*sim.ContainerHandle, len(containers))
	var mu sync.Mutex
	stopStarted := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, h := range handles {
			if h != nil {
				h.Cancel()
			}
		}
	}
	fail := func(err error) (*workload.Group, func(), error) {
		stopStarted()
		releaseMounts()
		return nil, nil, err
	}

	// The first container to start owns the network namespace the others
	// join, and with it the hosts file the metadata server's name resolves in.
	owner := order[0]
	members[owner].Config.ExtraHosts = extraHosts
	ownerGroup, err := workload.StartGroup(ctx, members[owner], nil, sink)
	if err != nil {
		return fail(err)
	}
	handles[owner] = ownerGroup.Main
	ownerID := ownerGroup.Main.ContainerID

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make([]chan struct{}, len(containers))
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	errs := make(chan error, len(containers))
	index := make(map[string]int, len(containers))
	for i, c := range containers {
		index[c.Name] = i
	}
	probe := func(i int) error {
		c := containers[i]
		if c.StartupProbe == nil {
			return nil
		}
		mu.Lock()
		containerID := handles[i].ContainerID
		mu.Unlock()
		exited, releaseWait := watchCloudRunContainerExit(containerID)
		defer releaseWait()
		port := cloudRunContainerPort(c)
		route, err := cloudRunContainerRoute(ctx, ownerID, port)
		if err == nil {
			err = runStartupProbe(ctx, cloudRunStartupProbe(c), route, port, port, exited)
		}
		if err != nil {
			return fmt.Errorf("container %q failed its startup probe: %w", c.Name, err)
		}
		return nil
	}
	var wg sync.WaitGroup
	for _, i := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, dep := range containers[i].DependsOn {
				select {
				case <-ready[index[dep]]:
				case <-ctx.Done():
					return
				}
			}
			if i != owner {
				started, err := workload.StartSidecars(ctx, ownerID, []workload.Container{members[i]}, sink)
				if err != nil {
					errs <- err
					cancel()
					return
				}
				mu.Lock()
				handles[i] = started[0]
				mu.Unlock()
			}
			if err := probe(i); err != nil {
				errs <- err
				cancel()
				return
			}
			close(ready[i])
		}()
	}
	wg.Wait()
	select {
	case err := <-errs:
		return fail(err)
	default:
	}
	if ctx.Err() != nil {
		return fail(fmt.Errorf("the execution stopped while its containers started: %w", ctx.Err()))
	}
	return &workload.Group{Main: handles[0], Sidecars: handles[1:]}, releaseMounts, nil
}
