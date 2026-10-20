package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// cloudRunContainerStartOrder orders the template's containers so each comes
// after every container its dependsOn names. It refuses a dependsOn that names
// no other container of the template and a cycle.
func cloudRunContainerStartOrder(containers []Container) ([]int, error) {
	members := make([]workload.Member, len(containers))
	for i, c := range containers {
		members[i] = workload.Member{Container: workload.Container{Name: c.Name}, DependsOn: c.DependsOn}
	}
	return workload.StartOrder(members)
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

// startCloudRunJobContainers starts a job task's containers as one Cloud Run
// container group, under names and labels that tie them to the execution.
func startCloudRunJobContainers(ctx context.Context, execID, execShort string, taskTmpl *TaskTemplate, timeout time.Duration, sink sim.LogSink) (*workload.Group, func(), error) {
	if taskTmpl == nil || len(taskTmpl.Containers) == 0 {
		return nil, nil, fmt.Errorf("execution has no containers")
	}
	return startCloudRunContainerGroup(ctx, cloudRunContainerGroup{
		project:    resourceProject(execID),
		region:     resourceLocation(execID),
		vpc:        taskTmpl.VpcAccess,
		containers: taskTmpl.Containers,
		volumes:    taskTmpl.Volumes,
		timeout:    timeout,
		name: func(i int) string {
			if i > 0 {
				return fmt.Sprintf("sockerless-sim-gcp-job-%s-sidecar-%d", execShort, i-1)
			}
			return fmt.Sprintf("sockerless-sim-gcp-job-%s", execShort)
		},
		labels: func(c Container) map[string]string {
			return map[string]string{
				cloudRunTaskExecutionLabel: execID,
				cloudRunTaskContainerLabel: c.Name,
			}
		},
	}, sink)
}

// cloudRunContainerGroup is one Cloud Run instance's containers that serve no
// requests: a job task, or a worker pool instance.
type cloudRunContainerGroup struct {
	project string
	region  string
	// vpc is the instance's VPC egress, which decides the network its
	// containers sit on.
	vpc        *VpcAccess
	containers []Container
	volumes    []Volume
	// timeout bounds the containers' run; zero leaves them running until
	// stopped.
	timeout time.Duration
	name    func(i int) string
	labels  func(c Container) map[string]string
}

// startCloudRunContainerGroup starts the group's containers in their dependsOn
// order: a container starts once every container it depends on has started
// and passed its startup probe, and every container that configures a startup
// probe must pass it before the group runs on. The containers share the
// network namespace of the first to start. The release it returns ends the
// ingestion of writes through their Cloud Storage volumes, once the containers
// have stopped.
func startCloudRunContainerGroup(ctx context.Context, g cloudRunContainerGroup, sink sim.LogSink) (*workload.Group, func(), error) {
	if len(g.containers) == 0 {
		return nil, nil, fmt.Errorf("the instance has no containers")
	}
	containers := g.containers
	if _, err := cloudRunContainerStartOrder(containers); err != nil {
		return nil, nil, err
	}

	volByName := make(map[string]Volume)
	for _, v := range g.volumes {
		volByName[v.Name] = v
	}
	project := g.project
	images := make([]string, len(containers))
	auths := make([]string, len(containers))
	for i, c := range containers {
		images[i] = sim.ResolveLocalImage(c.Image)
		registryAuth, err := workloadRegistryAuth(project, images[i])
		if err != nil {
			return nil, nil, err
		}
		auths[i] = registryAuth
		// Cloud Run resolves an execution's images before it places a task,
		// so a missing image is what an execution reports, whatever network
		// the task would join.
		if err := sim.PullImageWithCredential(ctx, images[i], "", registryAuth); err != nil {
			return nil, nil, err
		}
	}
	netw, err := gcpCloudRunNetwork(ctx, project, g.region, g.vpc, g.name(0))
	if err != nil {
		return nil, nil, err
	}
	members := make([]workload.Member, len(containers))
	var writable []string
	for i, c := range containers {
		binds, containerWritable, err := cloudRunGCSBinds(volByName, c)
		if err != nil {
			netw.done()
			return nil, nil, err
		}
		writable = append(writable, containerWritable...)
		cmdEnv := make(map[string]string, len(c.Env))
		for _, ev := range c.Env {
			cmdEnv[ev.Name] = ev.Value
		}
		members[i] = workload.Member{
			Container: workload.Container{Name: c.Name, Config: sim.ContainerConfig{
				CancelGracePeriod: cloudRunStopGrace,
				Image:             images[i],
				RegistryAuth:      auths[i],
				Command:           c.Command,
				Args:              c.Args,
				Env:               workloadhost.MergeEnv(cmdEnv, netw.env()),
				Timeout:           g.timeout,
				Name:              g.name(i),
				Labels:            g.labels(c),
				Binds:             binds,
				Sandbox:           SandboxCloudRun,
			}},
			DependsOn: c.DependsOn,
		}
		if c.StartupProbe != nil {
			members[i].Startup = cloudRunMemberStartup(c)
		}
	}
	mounts, err := gcsAcquireMounts(writable)
	if err != nil {
		netw.done()
		return nil, nil, err
	}
	releaseMounts := func() {
		mounts()
		netw.done()
	}
	backend, err := gcpPlacement.For(placementRun)
	if err != nil {
		releaseMounts()
		return nil, nil, err
	}
	// The first container to start owns the network namespace the others
	// join, and with it the hosts file the metadata server's name resolves in.
	group, err := workload.StartGroup(ctx, backend, members, workload.StartOptions{
		Sink: func(int) sim.LogSink { return sink },
		Owner: func(cfg *sim.ContainerConfig) {
			netw.apply(cfg)
			cfg.PublishPorts = cloudRunNamespacePorts(containers, false)
		},
	})
	if err != nil {
		// A container holds its address on the engine network until it is
		// removed; the lease goes back once every started container is gone,
		// or the next workload leases the address a stopping container still
		// holds.
		var startErr *workload.StartError
		if errors.As(err, &startErr) {
			go func() {
				<-startErr.Stopped()
				releaseMounts()
			}()
		} else {
			releaseMounts()
		}
		return nil, nil, err
	}
	return group, releaseMounts, nil
}

// cloudRunMemberStartup is the startup probe of container c, run through the
// address the namespace owner publishes the probed port at.
func cloudRunMemberStartup(c Container) func(context.Context, placement.Handle, placement.Handle) error {
	return func(ctx context.Context, member, owner placement.Handle) error {
		route := func(port int) (string, error) { return cloudRunContainerRoute(ctx, owner, port) }
		if err := runStartupProbe(ctx, cloudRunStartupProbe(c), route, cloudRunContainerPort(c), member.Exited()); err != nil {
			return fmt.Errorf("failed its startup probe: %w", err)
		}
		return nil
	}
}
