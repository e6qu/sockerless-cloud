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

const (
	// cloudRunDefaultMaxInstances is the instance count a revision scales to
	// when neither it nor its service caps it.
	cloudRunDefaultMaxInstances = 100
	// cloudRunDefaultConcurrency is how many requests an instance of at least
	// one CPU serves at once when the revision sets no maximum.
	cloudRunDefaultConcurrency = 80
	// cloudRunMinPendingQueue is the shortest a request waits for an
	// instance when the revision runs as many as it may: "up to 3.5 times
	// the average startup time of container instances of this service, or 10
	// seconds, whichever is greater".
	cloudRunMinPendingQueue = 10 * time.Second
)

// errCloudRunNoInstance is what a request gets when every instance the
// revision may run is busy for longer than the pending queue holds it; the
// front end answers it with cloudRunNoInstanceMessage.
var errCloudRunNoInstance = errors.New("no available instance")

const cloudRunNoInstanceMessage = "The request was aborted because there was no available instance."

// cloudRunRevisionRun is the instances one service revision runs: as many as
// the requests in flight need, at most maxInstanceRequestConcurrency each,
// between the revision's minimum and maximum instance counts.
type cloudRunRevisionRun struct {
	set         *replicas.Set
	specSig     string
	min, max    int
	concurrency int

	mu        sync.Mutex
	instances map[string]*cloudRunServiceInstance
	inflight  map[string]int
	// starting counts the instances being started for requests that found
	// none free, and waiting the requests that wait for one of them.
	starting, waiting int
	// changed closes, and is replaced, when an instance starts or a request
	// finishes, which may free a place for a waiting request.
	changed  chan struct{}
	startups []time.Duration
}

var cloudRunRevisionRuns = struct {
	sync.Mutex
	byName map[string]*cloudRunRevisionRun
}{byName: map[string]*cloudRunRevisionRun{}}

// cloudRunRevisionScale is the revision's minimum and maximum instance counts
// and how many requests each instance serves at once.
func cloudRunRevisionScale(rev RevisionV2) (minimum, maximum, concurrency int) {
	maximum = cloudRunDefaultMaxInstances
	if rev.Scaling != nil {
		minimum = int(rev.Scaling.MinInstanceCount)
		if rev.Scaling.MaxInstanceCount > 0 {
			maximum = int(rev.Scaling.MaxInstanceCount)
		}
	}
	concurrency = int(rev.MaxInstanceRequestConcurrency)
	if concurrency <= 0 {
		var cpu float64
		for _, c := range rev.Containers {
			limit := ""
			if c.Resources != nil {
				limit = c.Resources.Limits["cpu"]
			}
			cpu += containerCPULoad(limit)
		}
		concurrency = cloudRunDefaultConcurrency
		if cpu < 1 {
			concurrency = 1
		}
	}
	return minimum, max(maximum, minimum, 1), concurrency
}

// cloudRunRevisionRunFor is the revision's run, started when it has none or
// its containers changed under the same name.
func cloudRunRevisionRunFor(rev RevisionV2) *cloudRunRevisionRun {
	specSig := serviceContainersSignature(rev.Containers, rev.Volumes)
	minimum, maximum, concurrency := cloudRunRevisionScale(rev)
	cloudRunRevisionRuns.Lock()
	run := cloudRunRevisionRuns.byName[rev.Name]
	var previous *cloudRunRevisionRun
	if run == nil || run.specSig != specSig {
		previous = run
		run = newCloudRunRevisionRun(rev, specSig, minimum, maximum, concurrency)
		cloudRunRevisionRuns.byName[rev.Name] = run
	}
	cloudRunRevisionRuns.Unlock()
	if previous != nil {
		bg.Go(func() { _ = previous.set.Stop(context.Background()) })
	}
	return run
}

func newCloudRunRevisionRun(rev RevisionV2, specSig string, minimum, maximum, concurrency int) *cloudRunRevisionRun {
	serviceName, _, _ := strings.Cut(rev.Name, "/revisions/")
	serviceID := serviceName[strings.LastIndex(serviceName, "/")+1:]
	run := &cloudRunRevisionRun{
		specSig: specSig, min: minimum, max: maximum, concurrency: concurrency,
		instances: map[string]*cloudRunServiceInstance{},
		inflight:  map[string]int{},
		changed:   make(chan struct{}),
	}
	run.set = replicas.New(replicas.Spec{
		NewID:   func() string { return sim.RandomHex(16) },
		Restart: replicas.Always,
		Backoff: cloudRunWorkerPoolRestartBackoff,
		Settled: 10 * time.Minute,
		Start: func(ctx context.Context, id string) (*workload.Group, func(), error) {
			began := time.Now()
			sink := cloudRunRevisionLogSink(rev.Name, id)
			inst, err := startCloudRunServiceInstance(ctx, rev.Name, serviceID, id, rev.VpcAccess, rev.Containers, rev.Volumes, sink)
			if err != nil {
				return nil, nil, err
			}
			if _, err := inst.awaitReady(ctx); err != nil {
				stopCloudRunServiceInstance(inst)
				sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("The instance failed to start: %v", err)})
				return nil, nil, err
			}
			run.mu.Lock()
			run.instances[id] = inst
			run.startups = append(run.startups, time.Since(began))
			run.mu.Unlock()
			return inst.group, func() {
				inst.stop()
				if inst.releaseMounts != nil {
					inst.releaseMounts()
				}
			}, nil
		},
	}, replicas.Hooks{
		Started: func(*replicas.Replica) { run.signal() },
		Exited: func(r *replicas.Replica, result sim.ProcessResult) {
			run.forget(r.ID)
			cloudRunRevisionLogSink(rev.Name, r.ID).WriteLog(sim.LogLine{Stream: "stderr",
				Text: fmt.Sprintf("Container called exit(%d).", result.ExitCode)})
		},
		Stopped: func(r *replicas.Replica) { run.forget(r.ID) },
	})
	return run
}

// signal wakes the requests waiting for a place.
func (run *cloudRunRevisionRun) signal() {
	run.mu.Lock()
	close(run.changed)
	run.changed = make(chan struct{})
	run.mu.Unlock()
}

func (run *cloudRunRevisionRun) forget(id string) {
	run.mu.Lock()
	delete(run.instances, id)
	delete(run.inflight, id)
	close(run.changed)
	run.changed = make(chan struct{})
	run.mu.Unlock()
}

// pendingQueue is how long a request waits for an instance once the revision
// runs as many as it may.
func (run *cloudRunRevisionRun) pendingQueue() time.Duration {
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.startups) == 0 {
		return cloudRunMinPendingQueue
	}
	var total time.Duration
	for _, d := range run.startups {
		total += d
	}
	return max(cloudRunMinPendingQueue, total*7/time.Duration(2*len(run.startups)))
}

// acquire takes a place on an instance with room for one more request,
// starting an instance when none has room and the revision may run another,
// and otherwise waiting for one to free; release gives the place back.
func (run *cloudRunRevisionRun) acquire(ctx context.Context) (inst *cloudRunServiceInstance, release func(), err error) {
	var queued <-chan time.Time
	for {
		run.mu.Lock()
		r, _, ok := run.set.Pick(func(r *replicas.Replica) bool {
			return run.instances[r.ID] != nil && run.inflight[r.ID] < run.concurrency
		})
		if ok {
			run.inflight[r.ID]++
			inst := run.instances[r.ID]
			run.mu.Unlock()
			return inst, func() { run.done(r.ID) }, nil
		}
		changed := run.changed
		// An instance already starting serves this request when it has room
		// for every request waiting on it.
		join := run.waiting < run.starting*run.concurrency
		if join {
			run.waiting++
		} else {
			run.starting++
		}
		run.mu.Unlock()

		if join {
			select {
			case <-changed:
			case <-ctx.Done():
			}
			run.mu.Lock()
			run.waiting--
			run.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			continue
		}
		started, err := run.set.Add(context.Background(), run.max)
		run.mu.Lock()
		run.starting--
		run.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		if started != nil {
			continue
		}
		if queued == nil {
			queued = time.After(run.pendingQueue())
		}
		select {
		case <-changed:
		case <-queued:
			return nil, nil, errCloudRunNoInstance
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// done gives back a request's place on instance id.
func (run *cloudRunRevisionRun) done(id string) {
	run.mu.Lock()
	if run.inflight[id] > 0 {
		run.inflight[id]--
	}
	close(run.changed)
	run.changed = make(chan struct{})
	run.mu.Unlock()
}

// keepCloudRunRevisionsWarm starts the minimum instances of every revision
// the service's traffic reaches.
func keepCloudRunRevisionsWarm(svc ServiceV2) {
	if crv2Revisions == nil {
		return
	}
	for name := range cloudRunRoutedRevisions(svc) {
		rev, ok := crv2Revisions.Get(name)
		if !ok || len(rev.Containers) == 0 || rev.Containers[0].Image == "" {
			continue
		}
		if minimum, _, _ := cloudRunRevisionScale(rev); minimum > 0 {
			run := cloudRunRevisionRunFor(rev)
			bg.Go(func() { _ = run.set.Raise(context.Background(), minimum) })
		}
	}
}

// stopCloudRunRevisionInstances stops the instances of every revision of the
// service that keep does not name.
func stopCloudRunRevisionInstances(serviceName string, keep map[string]bool) {
	prefix := serviceName + "/revisions/"
	var stopped []*cloudRunRevisionRun
	cloudRunRevisionRuns.Lock()
	for name, run := range cloudRunRevisionRuns.byName {
		if strings.HasPrefix(name, prefix) && !keep[name] {
			delete(cloudRunRevisionRuns.byName, name)
			stopped = append(stopped, run)
		}
	}
	cloudRunRevisionRuns.Unlock()
	for _, run := range stopped {
		bg.Go(func() { _ = run.set.Stop(context.Background()) })
	}
}

// cloudRunRevisionLogSink writes a service instance's output to Cloud Logging
// under its revision, labelled with the instance that wrote it.
func cloudRunRevisionLogSink(revisionName, instanceID string) *cloudRunRevisionLog {
	serviceName, revisionID, _ := strings.Cut(revisionName, "/revisions/")
	project, location := resourceProject(serviceName), resourceLocation(serviceName)
	serviceID := serviceName[strings.LastIndex(serviceName, "/")+1:]
	return &cloudRunRevisionLog{project: project, instanceID: instanceID, resource: MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{
		"project_id":         project,
		"location":           location,
		"service_name":       serviceID,
		"revision_name":      revisionID,
		"configuration_name": serviceID,
	}}}
}

type cloudRunRevisionLog struct {
	project    string
	instanceID string
	resource   MonitoredResource
}

func (s *cloudRunRevisionLog) WriteLog(line sim.LogLine) {
	stream := "stdout"
	if line.Stream == "stderr" {
		stream = "stderr"
	}
	resource := s.resource
	if err := writeLogEntries(fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2F%s", s.project, stream), &resource,
		nil, []LogEntry{{TextPayload: line.Text, Labels: map[string]string{"instanceId": s.instanceID}}}); err != nil {
		log.Printf("cloud run logs: %v", err)
	}
}
