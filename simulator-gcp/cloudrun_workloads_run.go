package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// cloudRunResourceLogSink writes a Cloud Run workload's output to Cloud
// Logging under the monitored resource gcloud's `logs read` and `logs tail`
// filter the workload's entries by.
type cloudRunResourceLogSink struct {
	project  string
	resource MonitoredResource
}

func (s *cloudRunResourceLogSink) WriteLog(line sim.LogLine) {
	stream := "stdout"
	if line.Stream == "stderr" {
		stream = "stderr"
	}
	resource := s.resource
	writeLogEntries(fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2F%s", s.project, stream), &resource,
		nil, []LogEntry{{TextPayload: line.Text}})
}

func cloudRunWorkerPoolLogSink(poolName, revision string) *cloudRunResourceLogSink {
	project, location, poolID := resourceProject(poolName), resourceLocation(poolName), poolName[strings.LastIndex(poolName, "/")+1:]
	return &cloudRunResourceLogSink{project: project, resource: MonitoredResource{Type: "cloud_run_worker_pool", Labels: map[string]string{
		"project_id":       project,
		"location":         location,
		"worker_pool_name": poolID,
		"revision_name":    revision,
	}}}
}

func cloudRunInstanceLogSink(instanceName string) *cloudRunResourceLogSink {
	project, location, instanceID := resourceProject(instanceName), resourceLocation(instanceName), instanceName[strings.LastIndex(instanceName, "/")+1:]
	return &cloudRunResourceLogSink{project: project, resource: MonitoredResource{Type: "cloud_run_instance", Labels: map[string]string{
		"project_id":    project,
		"location":      location,
		"instance_name": instanceID,
	}}}
}

// resourceLocation is the location segment of a
// projects/{project}/locations/{location}/... resource name.
func resourceLocation(name string) string {
	parts := strings.Split(name, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "locations" {
			return parts[i+1]
		}
	}
	return ""
}

// cloudRunRun is one background run: a worker pool instance for as long as it
// runs, or the start of a Cloud Run instance's containers.
type cloudRunRun struct {
	stop context.CancelFunc
	done chan struct{}
}

func (r *cloudRunRun) end() {
	r.stop()
	<-r.done
}

type cloudRunWorkerPoolRun struct {
	specSig   string
	instances []*cloudRunRun
}

var cloudRunWorkerPoolRuns = struct {
	sync.Mutex
	byName map[string]*cloudRunWorkerPoolRun
}{byName: map[string]*cloudRunWorkerPoolRun{}}

// cloudRunWorkerPoolInstanceCount is how many instances the pool's scaling
// asks for: the fixed count in manual scaling, the floor in automatic scaling.
func cloudRunWorkerPoolInstanceCount(pool WorkerPoolV2) int {
	if pool.Scaling == nil {
		return 0
	}
	if pool.Scaling.ScalingMode == "AUTOMATIC" {
		return int(pool.Scaling.MinInstanceCount)
	}
	return int(pool.Scaling.ManualInstanceCount)
}

// runCloudRunWorkerPool brings the pool's running instances in line with its
// template and scaling: a changed template replaces every instance, and a
// changed instance count starts or stops the difference. It returns once the
// instances it retires have stopped.
func runCloudRunWorkerPool(pool WorkerPoolV2) {
	var containers []Container
	var volumes []Volume
	if pool.Template != nil {
		containers, volumes = pool.Template.Containers, pool.Template.Volumes
	}
	want := cloudRunWorkerPoolInstanceCount(pool)
	if len(containers) == 0 {
		want = 0
	}
	revision := pool.LatestReadyRevision[strings.LastIndex(pool.LatestReadyRevision, "/")+1:]
	specSig := serviceContainersSignature(containers, volumes)

	cloudRunWorkerPoolRuns.Lock()
	run := cloudRunWorkerPoolRuns.byName[pool.Name]
	var retire []*cloudRunRun
	if run != nil && run.specSig != specSig {
		retire, run = run.instances, nil
	}
	if run == nil {
		run = &cloudRunWorkerPoolRun{specSig: specSig}
	}
	for len(run.instances) > want {
		last := len(run.instances) - 1
		retire = append(retire, run.instances[last])
		run.instances = run.instances[:last]
	}
	for len(run.instances) < want {
		run.instances = append(run.instances, startCloudRunWorkerPoolInstance(pool.Name, revision, containers, volumes))
	}
	if want == 0 {
		delete(cloudRunWorkerPoolRuns.byName, pool.Name)
	} else {
		cloudRunWorkerPoolRuns.byName[pool.Name] = run
	}
	cloudRunWorkerPoolRuns.Unlock()
	for _, inst := range retire {
		inst.end()
	}
}

// stopCloudRunWorkerPool stops every instance of the pool and returns once
// their containers have stopped.
func stopCloudRunWorkerPool(name string) {
	cloudRunWorkerPoolRuns.Lock()
	run := cloudRunWorkerPoolRuns.byName[name]
	delete(cloudRunWorkerPoolRuns.byName, name)
	cloudRunWorkerPoolRuns.Unlock()
	if run == nil {
		return
	}
	for _, inst := range run.instances {
		inst.end()
	}
}

// startCloudRunWorkerPoolInstance runs one worker pool instance: its
// containers start as one container group and run until the main container
// exits or the instance is retired, which sends the containers the stop
// signal with Cloud Run's grace.
func startCloudRunWorkerPoolInstance(poolName, revision string, containers []Container, volumes []Volume) *cloudRunRun {
	ctx, stop := context.WithCancel(context.Background())
	run := &cloudRunRun{stop: stop, done: make(chan struct{})}
	poolID := poolName[strings.LastIndex(poolName, "/")+1:]
	instanceID := sim.RandomHex(8)
	sink := cloudRunWorkerPoolLogSink(poolName, revision)
	bg.Go(func() {
		defer close(run.done)
		group, releaseMounts, err := startCloudRunContainerGroup(ctx, cloudRunContainerGroup{
			project:    resourceProject(poolName),
			containers: containers,
			volumes:    volumes,
			name: func(i int) string {
				if i > 0 {
					return fmt.Sprintf("sockerless-sim-cloudrun-wp-%s-sidecar-%d-%s", poolID, i-1, instanceID)
				}
				return fmt.Sprintf("sockerless-sim-cloudrun-wp-%s-%s", poolID, instanceID)
			},
			labels: func(c Container) map[string]string {
				return map[string]string{
					"sockerless-sim-worker-pool":           poolName,
					"sockerless-sim-worker-pool-container": c.Name,
				}
			},
		}, sink)
		if err != nil {
			if ctx.Err() == nil {
				sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("The instance failed to start: %v", err)})
			}
			return
		}
		exited := make(chan sim.ProcessResult, 1)
		go func() { exited <- group.Main.Wait() }()
		select {
		case <-ctx.Done():
			sim.StopContainer(group.Main.ContainerID, cloudRunStopGrace)
			group.Main.Cancel()
			<-exited
		case result := <-exited:
			sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("Container called exit(%d).", result.ExitCode)})
		}
		for _, h := range group.Sidecars {
			h.Cancel()
			h.Wait()
		}
		releaseMounts()
	})
	return run
}

var cloudRunInstanceRuns = struct {
	sync.Mutex
	byName map[string]*cloudRunRun
}{byName: map[string]*cloudRunRun{}}

// runCloudRunInstance starts a Cloud Run instance's containers, keeping those
// it already runs when its containers and volumes are unchanged. Its ingress
// container answers on its port as a service instance's does, and every
// container starts in dependsOn order behind its startup probe.
func runCloudRunInstance(inst InstanceV2) {
	ctx, stop := context.WithCancel(context.Background())
	run := &cloudRunRun{stop: stop, done: make(chan struct{})}
	cloudRunInstanceRuns.Lock()
	previous := cloudRunInstanceRuns.byName[inst.Name]
	cloudRunInstanceRuns.byName[inst.Name] = run
	cloudRunInstanceRuns.Unlock()
	if previous != nil {
		previous.end()
	}
	if len(inst.Containers) == 0 {
		close(run.done)
		deleteCloudRunServiceInstance(inst.Name)
		return
	}
	instanceID := inst.Name[strings.LastIndex(inst.Name, "/")+1:]
	sink := cloudRunInstanceLogSink(inst.Name)
	bg.Go(func() {
		defer close(run.done)
		running, err := ensureCloudRunServiceInstance(ctx, inst.Name, instanceID, inst.Containers, inst.Volumes, sink)
		if err != nil {
			if ctx.Err() == nil {
				sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("The instance failed to start: %v", err)})
			}
			return
		}
		if _, err := running.awaitReady(ctx); err != nil {
			deleteCloudRunServiceInstanceIf(inst.Name, running)
			if ctx.Err() == nil {
				sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("The instance failed to start: %v", err)})
			}
		}
	})
}

// stopCloudRunInstance stops a Cloud Run instance's containers and returns
// once they have stopped.
func stopCloudRunInstance(name string) {
	cloudRunInstanceRuns.Lock()
	run := cloudRunInstanceRuns.byName[name]
	delete(cloudRunInstanceRuns.byName, name)
	cloudRunInstanceRuns.Unlock()
	if run != nil {
		run.end()
	}
	deleteCloudRunServiceInstance(name)
}
