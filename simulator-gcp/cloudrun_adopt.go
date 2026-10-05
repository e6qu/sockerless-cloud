package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Cloud Run's control plane never restarts the instances it runs, so a
// simulator restarted on its state directory adopts the containers of every
// worker pool instance and Cloud Run instance the earlier process left
// running, rather than starting new ones beside them. Each container of an
// instance carries the instance's group label, and the signature of the
// template it runs, so the adoption finds whole instances of the current
// template and removes whatever else the earlier process left.
const (
	cloudRunGroupLabel     = "sockerless-sim-cloudrun-group"
	cloudRunSpecLabel      = "sockerless-sim-cloudrun-spec"
	cloudRunResourceLabel  = "sockerless-sim-cloudrun-resource"
	cloudRunContainerLabel = "sockerless-sim-cloudrun-container"
)

// cloudRunSpecDigest is the label value naming a template's containers and
// volumes.
func cloudRunSpecDigest(specSig string) string {
	sum := sha256.Sum256([]byte(specSig))
	return hex.EncodeToString(sum[:16])
}

// cloudRunAdoptedLogSink drops the lines an adopted container wrote by the
// resource's newest log entry, which the earlier process already wrote to
// Cloud Logging, since an adopted container's log stream replays its whole
// output. Entries carry the millisecond they were written, never earlier than
// the line.
type cloudRunAdoptedLogSink struct {
	sink  *cloudRunResourceLogSink
	after time.Time
}

func (s cloudRunAdoptedLogSink) WriteLog(line sim.LogLine) {
	if !line.Timestamp.IsZero() && !line.Timestamp.Truncate(time.Millisecond).After(s.after) {
		return
	}
	s.sink.WriteLog(line)
}

func newCloudRunAdoptedLogSink(sink *cloudRunResourceLogSink) cloudRunAdoptedLogSink {
	adopted := cloudRunAdoptedLogSink{sink: sink}
	if logEntries == nil {
		return adopted
	}
	for _, stream := range []string{"stdout", "stderr"} {
		entries, _ := logEntries.Get(fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2F%s", sink.project, stream))
		for _, entry := range entries {
			if entry.Resource == nil || entry.Resource.Type != sink.resource.Type || !cloudRunSameResource(entry.Resource.Labels, sink.resource.Labels) {
				continue
			}
			if at, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil && at.After(adopted.after) {
				adopted.after = at
			}
		}
	}
	return adopted
}

// cloudRunSameResource reports whether a log entry's resource labels name the
// sink's resource; a worker pool's entries match across its revisions.
func cloudRunSameResource(entry, sink map[string]string) bool {
	for key, value := range sink {
		if key != "revision_name" && entry[key] != value {
			return false
		}
	}
	return true
}

// cloudRunExistingGroups sorts the containers an earlier process left into
// the instances they belong to.
func cloudRunExistingGroups(labels map[string]string) (map[string][]sim.ExistingContainer, []string, error) {
	if !sim.HasPersistentWorkloadIdentity() || sim.RequireContainerRuntime("adopting Cloud Run instances") != nil {
		return nil, nil, nil
	}
	found, err := sim.FindExistingContainers(labels)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string][]sim.ExistingContainer{}
	for _, c := range found {
		groups[c.Labels[cloudRunGroupLabel]] = append(groups[c.Labels[cloudRunGroupLabel]], c)
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return groups, ids, nil
}

// cloudRunAdoptable indexes an instance's containers by the template
// container each runs, when the instance runs the template whole.
func cloudRunAdoptable(group []sim.ExistingContainer, groupID, specSig, containerLabel string, containers []Container) (map[string]sim.ExistingContainer, bool) {
	if groupID == "" || len(group) != len(containers) {
		return nil, false
	}
	byName := make(map[string]sim.ExistingContainer, len(group))
	for _, c := range group {
		if !c.Running || c.Labels[cloudRunSpecLabel] != cloudRunSpecDigest(specSig) {
			return nil, false
		}
		byName[c.Labels[containerLabel]] = c
	}
	for _, c := range containers {
		if _, ok := byName[c.Name]; !ok {
			return nil, false
		}
	}
	return byName, true
}

func removeCloudRunContainers(owner string, group []sim.ExistingContainer) {
	for _, c := range group {
		if err := sim.RemoveExistingContainer(c.ID); err != nil {
			log.Printf("Cloud Run %s: remove container %s an earlier process left: %v", owner, c.ID, err)
		}
	}
}

// cloudRunWritableBuckets is the buckets a template's containers mount
// writable.
func cloudRunWritableBuckets(containers []Container, volumes []Volume) ([]string, error) {
	bindsFor := serviceBindsFor(volumes)
	var writable []string
	for _, c := range containers {
		_, containerWritable, err := bindsFor(c)
		if err != nil {
			return nil, err
		}
		writable = append(writable, containerWritable...)
	}
	return writable, nil
}

// adoptCloudRunWorkerPoolInstances takes over the pool's instances an earlier
// process left running, up to the count its scaling asks for, so the next
// runCloudRunWorkerPool keeps them and starts only the difference. A pool that
// is not meant to run adopts none.
func adoptCloudRunWorkerPoolInstances(pool WorkerPoolV2, run bool) {
	groups, ids, err := cloudRunExistingGroups(map[string]string{"sockerless-sim-worker-pool": pool.Name})
	if err != nil {
		log.Printf("Cloud Run worker pool %s: list the instances an earlier process left: %v", pool.Name, err)
		return
	}
	var containers []Container
	var volumes []Volume
	if pool.Template != nil {
		containers, volumes = pool.Template.Containers, pool.Template.Volumes
	}
	want := 0
	if run && len(containers) > 0 {
		want = cloudRunWorkerPoolInstanceCount(pool)
	}
	specSig := serviceContainersSignature(containers, volumes)
	revision := pool.LatestCreatedRevision[strings.LastIndex(pool.LatestCreatedRevision, "/")+1:]
	var adopted []*cloudRunRun
	for _, id := range ids {
		group := groups[id]
		if len(adopted) < want {
			if byName, ok := cloudRunAdoptable(group, id, specSig, "sockerless-sim-worker-pool-container", containers); ok {
				instance, err := adoptCloudRunWorkerPoolInstance(pool.Name, revision, containers, volumes, byName)
				if err == nil {
					adopted = append(adopted, instance)
					continue
				}
				log.Printf("Cloud Run worker pool %s: adopt instance %s: %v", pool.Name, id, err)
			}
		}
		removeCloudRunContainers(pool.Name, group)
	}
	if len(adopted) == 0 {
		return
	}
	cloudRunWorkerPoolRuns.Lock()
	cloudRunWorkerPoolRuns.byName[pool.Name] = &cloudRunWorkerPoolRun{specSig: specSig, instances: adopted}
	cloudRunWorkerPoolRuns.Unlock()
}

// adoptCloudRunWorkerPoolInstance runs a worker pool instance on the
// containers an earlier process started, as startCloudRunWorkerPoolInstance
// runs one it starts.
func adoptCloudRunWorkerPoolInstance(poolName, revision string, containers []Container, volumes []Volume, byName map[string]sim.ExistingContainer) (*cloudRunRun, error) {
	writable, err := cloudRunWritableBuckets(containers, volumes)
	if err != nil {
		return nil, err
	}
	releaseMounts, err := gcsAcquireMounts(writable)
	if err != nil {
		return nil, err
	}
	sink := newCloudRunAdoptedLogSink(cloudRunWorkerPoolLogSink(poolName, revision))
	handles := make([]*sim.ContainerHandle, 0, len(containers))
	for _, c := range containers {
		handle, err := sim.AdoptContainer(byName[c.Name].ID, sim.ContainerConfig{CancelGracePeriod: cloudRunStopGrace}, sink)
		if err != nil {
			for _, h := range handles {
				h.Cancel()
			}
			releaseMounts()
			return nil, err
		}
		handles = append(handles, handle)
	}
	run, ctx := newCloudRunRun()
	run.started(nil)
	bg.Go(func() {
		defer close(run.done)
		main := handles[0]
		exited := make(chan sim.ProcessResult, 1)
		go func() { exited <- main.Wait() }()
		select {
		case <-ctx.Done():
			sim.StopContainer(main.ContainerID, cloudRunStopGrace)
			main.Cancel()
			<-exited
		case result := <-exited:
			sink.WriteLog(sim.LogLine{Stream: "stderr", Text: fmt.Sprintf("Container called exit(%d).", result.ExitCode)})
		}
		for _, h := range handles[1:] {
			h.Cancel()
			h.Wait()
		}
		releaseMounts()
	})
	return run, nil
}

// adoptCloudRunInstance takes over the containers of a Cloud Run instance an
// earlier process left running, so the next runCloudRunInstance finds them
// serving rather than starting new ones. An instance that is not meant to
// run adopts none.
func adoptCloudRunInstance(inst InstanceV2, run bool) {
	groups, ids, err := cloudRunExistingGroups(map[string]string{cloudRunResourceLabel: inst.Name})
	if err != nil {
		log.Printf("Cloud Run instance %s: list the containers an earlier process left: %v", inst.Name, err)
		return
	}
	specSig := serviceContainersSignature(inst.Containers, inst.Volumes)
	adopted := false
	for _, id := range ids {
		group := groups[id]
		if run && !adopted && len(inst.Containers) > 0 {
			if byName, ok := cloudRunAdoptable(group, id, specSig, cloudRunContainerLabel, inst.Containers); ok {
				if err := adoptCloudRunServiceInstance(inst.Name, specSig, inst.Containers, inst.Volumes, byName, newCloudRunAdoptedLogSink(cloudRunInstanceLogSink(inst.Name))); err == nil {
					adopted = true
					continue
				}
				log.Printf("Cloud Run instance %s: adopt its containers: %v", inst.Name, err)
			}
		}
		removeCloudRunContainers(inst.Name, group)
	}
}

// adoptCloudRunServiceInstance records the running containers byName holds as
// name's instance, serving already: they started and passed their startup
// probes under the earlier process.
func adoptCloudRunServiceInstance(name, specSig string, containers []Container, volumes []Volume, byName map[string]sim.ExistingContainer, sink sim.LogSink) error {
	order, err := cloudRunContainerStartOrder(containers)
	if err != nil {
		return err
	}
	ownerID := byName[containers[order[0]].Name].ID
	address, err := cloudRunContainerRoute(context.Background(), ownerID, cloudRunContainerPort(containers[0]))
	if err != nil {
		return err
	}
	writable, err := cloudRunWritableBuckets(containers, volumes)
	if err != nil {
		return err
	}
	releaseMounts, err := gcsAcquireMounts(writable)
	if err != nil {
		return err
	}
	var handles []*sim.ContainerHandle
	for _, i := range order[1:] {
		handle, err := sim.AdoptContainer(byName[containers[i].Name].ID, sim.ContainerConfig{CancelGracePeriod: cloudRunStopGrace}, sink)
		if err != nil {
			for _, h := range handles {
				h.Cancel()
			}
			releaseMounts()
			return err
		}
		handles = append(handles, handle)
	}
	lifeCtx, stop := context.WithCancel(context.Background())
	inst := &cloudRunServiceInstance{
		ownerID:       ownerID,
		specSig:       specSig,
		stop:          stop,
		logsDone:      streamCloudRunOwnerLogs(lifeCtx, ownerID, sink),
		ready:         make(chan struct{}),
		address:       address,
		releaseMounts: releaseMounts,
		ingressID:     byName[containers[0].Name].ID,
		handles:       handles,
	}
	close(inst.ready)
	cloudRunServiceInstances.Lock()
	previous := cloudRunServiceInstances.byName[name]
	cloudRunServiceInstances.byName[name] = inst
	cloudRunServiceInstances.Unlock()
	stopCloudRunServiceInstance(previous)
	return nil
}
