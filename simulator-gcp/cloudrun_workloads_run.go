package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/replicas"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// cloudRunResourceLogSink writes a Cloud Run workload's output to Cloud
// Logging under the monitored resource gcloud's `logs read` and `logs tail`
// filter the workload's entries by.
type cloudRunResourceLogSink struct {
	project  string
	resource MonitoredResource
}

func (s *cloudRunResourceLogSink) WriteLog(line sim.LogLine) {
	resource := s.resource
	if err := writeLogEntries(cloudRunLogName(s.project, line.Stream), &resource, nil, []LogEntry{cloudRunLogEntry(line)}); err != nil {
		log.Printf("cloud run logs: %v", err)
	}
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
// runs, or a Cloud Run instance's containers across their restarts. ready
// closes once the run's containers have started and passed their startup
// probes, or failed to; startErr is written before it closes.
type cloudRunRun struct {
	stop     context.CancelFunc
	done     chan struct{}
	ready    chan struct{}
	startErr error
}

func newCloudRunRun() (*cloudRunRun, context.Context) {
	ctx, stop := context.WithCancel(context.Background())
	return &cloudRunRun{stop: stop, done: make(chan struct{}), ready: make(chan struct{})}, ctx
}

func (r *cloudRunRun) end() {
	r.stop()
	<-r.done
}

// started records the outcome of the run's start; only the first call counts.
func (r *cloudRunRun) started(err error) {
	select {
	case <-r.ready:
	default:
		r.startErr = err
		close(r.ready)
	}
}

// cloudRunWorkerPoolRun is the instances a worker pool runs of one
// revision's template.
type cloudRunWorkerPoolRun struct {
	specSig string
	set     *replicas.Set
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

// cloudRunWorkerPoolRestartBackoff is how long a worker pool waits before it
// replaces an instance that exited, doubling from 10 seconds to 5 minutes
// with the instances in a row that exited within 10 minutes of starting.
func cloudRunWorkerPoolRestartBackoff(consecutive int) time.Duration {
	wait := 10 * time.Second
	for range consecutive - 1 {
		wait *= 2
		if wait >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return wait
}

// newCloudRunWorkerPoolRun is the instance set of a pool revision's template:
// every instance runs its containers as one container group until the main
// container exits, and the pool replaces an instance that exited.
func newCloudRunWorkerPoolRun(poolName, revision string, vpc *VpcAccess, containers []Container, volumes []Volume) *cloudRunWorkerPoolRun {
	poolID := poolName[strings.LastIndex(poolName, "/")+1:]
	specSig := serviceContainersSignature(containers, volumes)
	spec := cloudRunSpecDigest(specSig)
	sink := cloudRunWorkerPoolLogSink(poolName, revision)
	set := replicas.New(replicas.Spec{
		NewID:   func() string { return sim.RandomHex(8) },
		Restart: replicas.Always,
		Backoff: cloudRunWorkerPoolRestartBackoff,
		Settled: 10 * time.Minute,
		Start: func(ctx context.Context, instanceID string) (*workload.Group, func(), error) {
			group, release, err := startCloudRunContainerGroup(ctx, cloudRunContainerGroup{
				project:    resourceProject(poolName),
				region:     resourceLocation(poolName),
				vpc:        vpc,
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
						cloudRunWorkerPoolLabel:                poolName,
						"sockerless-sim-worker-pool-container": c.Name,
						cloudRunGroupLabel:                     instanceID,
						cloudRunSpecLabel:                      spec,
					}
				},
			}, sink)
			if err != nil && ctx.Err() == nil {
				sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("The instance failed to start: %v", err)})
			}
			return group, release, err
		},
	}, replicas.Hooks{
		Exited: func(_ *replicas.Replica, result sim.ProcessResult) {
			sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("Container called exit(%d).", result.ExitCode)})
		},
	})
	return &cloudRunWorkerPoolRun{specSig: specSig, set: set}
}

// runCloudRunWorkerPool brings the pool's running instances in line with its
// template and scaling. A changed template starts the new revision's
// instances and retires the previous revision's once they run; when they do
// not, the previous revision keeps running. A changed instance count starts
// or stops the difference. The pool's reconciliation settles once every
// instance it runs has started, or one failed to.
func runCloudRunWorkerPool(pool WorkerPoolV2) {
	var containers []Container
	var volumes []Volume
	var vpc *VpcAccess
	if pool.Template != nil {
		containers, volumes, vpc = pool.Template.Containers, pool.Template.Volumes, pool.Template.VpcAccess
	}
	want := cloudRunWorkerPoolInstanceCount(pool)
	if len(containers) == 0 {
		want = 0
	}
	revision := pool.LatestCreatedRevision[strings.LastIndex(pool.LatestCreatedRevision, "/")+1:]
	specSig := serviceContainersSignature(containers, volumes)
	if want == 0 {
		// A pool that runs no instance settles at once.
		cloudRunWorkerPoolRuns.Lock()
		previous := cloudRunWorkerPoolRuns.byName[pool.Name]
		delete(cloudRunWorkerPoolRuns.byName, pool.Name)
		cloudRunWorkerPoolRuns.Unlock()
		if previous != nil {
			bg.Go(func() { _ = previous.set.Stop(context.Background()) })
		}
		settleCloudRunWorkerPool(pool.Name, pool.Generation, nil)
		return
	}

	cloudRunWorkerPoolRuns.Lock()
	run := cloudRunWorkerPoolRuns.byName[pool.Name]
	var previous *cloudRunWorkerPoolRun
	if run == nil || run.specSig != specSig {
		previous = run
		run = newCloudRunWorkerPoolRun(pool.Name, revision, vpc, containers, volumes)
		cloudRunWorkerPoolRuns.byName[pool.Name] = run
	}
	cloudRunWorkerPoolRuns.Unlock()

	bg.Go(func() {
		err := run.set.Scale(context.Background(), want)
		if err != nil && previous != nil {
			// The new revision's instances did not start: the pool keeps
			// running the previous revision's.
			_ = run.set.Stop(context.Background())
			cloudRunWorkerPoolRuns.Lock()
			if cloudRunWorkerPoolRuns.byName[pool.Name] == run {
				cloudRunWorkerPoolRuns.byName[pool.Name] = previous
			}
			cloudRunWorkerPoolRuns.Unlock()
		} else if previous != nil {
			_ = previous.set.Stop(context.Background())
		}
		settleCloudRunWorkerPool(pool.Name, pool.Generation, err)
	})
}

// stopCloudRunWorkerPool stops every instance of the pool and returns once
// their containers have stopped.
func stopCloudRunWorkerPool(name string) {
	cloudRunWorkerPoolRuns.Lock()
	run := cloudRunWorkerPoolRuns.byName[name]
	delete(cloudRunWorkerPoolRuns.byName, name)
	cloudRunWorkerPoolRuns.Unlock()
	if run != nil {
		_ = run.set.Stop(context.Background())
	}
}

var cloudRunInstanceRuns = struct {
	sync.Mutex
	byName map[string]*cloudRunRun
}{byName: map[string]*cloudRunRun{}}

// cloudRunInstanceRestartLimit is how many times in a row Cloud Run restarts
// an instance whose restart policy restarts it: "Cloud Run attempts to restart
// a failing instance up to 3 times sequentially. If the instance continues to
// fail, it transitions to the FAILED status."
const cloudRunInstanceRestartLimit = 3

// runCloudRunInstance starts a Cloud Run instance's containers, keeping those
// it already runs when its containers and volumes are unchanged. Its ingress
// container answers on its port as a service instance's does, and every
// container starts in dependsOn order behind its startup probe. Once they have
// all started, or one failed to, the instance's reconciliation settles; after
// that, a container that exits restarts the instance as its restart policy
// says.
func runCloudRunInstance(inst InstanceV2) {
	run, ctx := newCloudRunRun()
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
		run.started(nil)
		settleCloudRunInstance(inst.Name, run, nil)
		return
	}
	instanceID := inst.Name[strings.LastIndex(inst.Name, "/")+1:]
	sink := cloudRunInstanceLogSink(inst.Name)
	bg.Go(func() {
		defer close(run.done)
		restarts := 0
		for {
			running, err := ensureCloudRunServiceInstance(ctx, inst.Name, instanceID, inst.VpcAccess, inst.Containers, inst.Volumes, sink)
			// A container that exits while the instance starts is an exit of
			// the instance's, which the restart policy answers like any other.
			exitedWhileStarting := false
			var exitCode int64
			if err == nil {
				_, err = running.awaitReady(ctx)
				if err != nil {
					code, exited := running.exited()
					// The engine can report the container stopped before its
					// exit reaches the handle; that exit follows at once.
					if !exited && errors.Is(err, sim.ErrNotRunning) {
						code, exited = running.awaitExit(ctx), true
					}
					if exited && restarts > 0 {
						err, exitedWhileStarting, exitCode = nil, true, code
					}
					deleteCloudRunServiceInstanceIf(inst.Name, running)
				}
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("The instance failed to start: %v", err)})
				if restarts == 0 {
					run.started(err)
					settleCloudRunInstance(inst.Name, run, err)
					return
				}
				exitCode = -1
			} else {
				if restarts == 0 {
					run.started(nil)
					settleCloudRunInstance(inst.Name, run, nil)
				}
				if !exitedWhileStarting {
					exitCode = running.awaitExit(ctx)
					if ctx.Err() != nil {
						return
					}
					deleteCloudRunServiceInstanceIf(inst.Name, running)
				}
			}
			// The instance's record settles before its exit is logged, so a
			// reader of the log finds the outcome already recorded.
			logExit := func() {
				if exitCode >= 0 {
					sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("Container called exit(%d).", exitCode)})
				}
			}
			switch {
			case !cloudRunInstanceRestarts(inst, exitCode):
				endCloudRunInstance(inst.Name, run, exitCode, "")
				logExit()
				return
			case restarts == cloudRunInstanceRestartLimit:
				endCloudRunInstance(inst.Name, run, exitCode,
					fmt.Sprintf("The instance failed after Cloud Run restarted it %d times.", restarts))
				logExit()
				return
			}
			logExit()
			restarts++
		}
	})
}

// cloudRunInstanceRestarts reports whether the instance's restart policy
// restarts it after a container exited with exitCode, -1 standing for a
// restart that failed to start. ON_FAILURE is the default; under ALWAYS an
// instance of several containers stops rather than restarts when one exits
// cleanly.
func cloudRunInstanceRestarts(inst InstanceV2, exitCode int64) bool {
	switch inst.RestartPolicy {
	case "NEVER":
		return false
	case "ALWAYS":
		return exitCode != 0 || len(inst.Containers) == 1
	default:
		return exitCode != 0
	}
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
