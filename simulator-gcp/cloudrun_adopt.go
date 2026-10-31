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
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
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
	cloudRunServiceLabel   = "sockerless-sim-service"

	cloudRunTaskExecutionLabel = "sockerless-sim-execution"
	cloudRunTaskContainerLabel = "sockerless-sim-execution-container"
	cloudRunTaskIndexLabel     = "sockerless-sim-task-index"
	cloudRunTaskAttemptLabel   = "sockerless-sim-task-attempt"

	cloudRunWorkerPoolLabel = "sockerless-sim-worker-pool"
)

// cloudRunSpecDigest is the label value naming a template's containers and
// volumes.
func cloudRunSpecDigest(specSig string) string {
	sum := sha256.Sum256([]byte(specSig))
	return hex.EncodeToString(sum[:16])
}

// cloudRunAdoptedLogSink drops the lines an adopted container wrote by the
// time the resource's newest log entry was received, which the earlier process
// already wrote to Cloud Logging, since an adopted container's log stream
// replays its whole output. An entry is received at the millisecond it was
// written, never earlier than the line; its own timestamp is the line's, or
// whatever time a structured line states.
type cloudRunAdoptedLogSink struct {
	sink  sim.LogSink
	after time.Time
}

func (s cloudRunAdoptedLogSink) WriteLog(line sim.LogLine) {
	if !line.Timestamp.IsZero() && !line.Timestamp.Truncate(time.Millisecond).After(s.after) {
		return
	}
	s.sink.WriteLog(line)
}

// newCloudRunAdoptedLogSink wraps sink, which writes the entries of resource
// in project, for a container an earlier process started.
func newCloudRunAdoptedLogSink(sink sim.LogSink, project string, resource MonitoredResource, entryLabels map[string]string) cloudRunAdoptedLogSink {
	adopted := cloudRunAdoptedLogSink{sink: sink}
	if logEntries == nil {
		return adopted
	}
	for _, stream := range []string{"stdout", "stderr"} {
		for _, stored := range logEntries.Values(fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2F%s", project, stream)) {
			entry := stored.Entry
			if entry.Resource == nil || entry.Resource.Type != resource.Type || !cloudRunSameResource(entry.Resource.Labels, resource.Labels) {
				continue
			}
			if !cloudRunEntryLabeled(entry.Labels, entryLabels) {
				continue
			}
			if at, err := time.Parse(time.RFC3339Nano, entry.ReceiveTimestamp); err == nil && at.After(adopted.after) {
				adopted.after = at
			}
		}
	}
	return adopted
}

func newCloudRunAdoptedResourceLogSink(sink *cloudRunResourceLogSink) cloudRunAdoptedLogSink {
	return newCloudRunAdoptedLogSink(sink, sink.project, sink.resource, nil)
}

// cloudRunEntryLabeled reports whether a log entry carries every label want
// names, with its value.
func cloudRunEntryLabeled(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
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
func cloudRunExistingGroups(labels map[string]string) (map[string][]placement.Existing, []string, error) {
	if !sim.HasPersistentWorkloadIdentity() || sim.RequireContainerRuntime("adopting Cloud Run instances") != nil {
		return nil, nil, nil
	}
	backend, err := gcpPlacement.For(placementRun)
	if err != nil {
		return nil, nil, err
	}
	found, err := backend.Find(context.Background(), labels)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string][]placement.Existing{}
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
func cloudRunAdoptable(group []placement.Existing, groupID, specSig, containerLabel string, containers []Container) (map[string]placement.Existing, bool) {
	if groupID == "" || len(group) != len(containers) {
		return nil, false
	}
	byName := make(map[string]placement.Existing, len(group))
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

func removeCloudRunContainers(owner string, group []placement.Existing) {
	backend, err := gcpPlacement.For(placementRun)
	if err != nil {
		log.Printf("Cloud Run %s: remove the containers an earlier process left: %v", owner, err)
		return
	}
	for _, c := range group {
		if err := backend.Remove(context.Background(), c.ID); err != nil {
			log.Printf("Cloud Run %s: remove container %s an earlier process left: %v", owner, c.ID, err)
		}
	}
}

// adoptCloudRunContainer takes over one container an earlier process left.
func adoptCloudRunContainer(id string, config sim.ContainerConfig, sink sim.LogSink) (placement.Handle, error) {
	backend, err := gcpPlacement.For(placementRun)
	if err != nil {
		return nil, err
	}
	return backend.Adopt(context.Background(), id, config, sink)
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
	groups, ids, err := cloudRunExistingGroups(map[string]string{cloudRunWorkerPoolLabel: pool.Name})
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
	var vpc *VpcAccess
	if pool.Template != nil {
		vpc = pool.Template.VpcAccess
	}
	poolRun := newCloudRunWorkerPoolRun(pool.Name, revision, vpc, containers, volumes)
	adopted := 0
	for _, id := range ids {
		group := groups[id]
		if adopted < want {
			if byName, ok := cloudRunAdoptable(group, id, specSig, "sockerless-sim-worker-pool-container", containers); ok {
				instance, release, err := adoptCloudRunWorkerPoolInstance(pool.Name, revision, vpc, containers, volumes, byName)
				if err == nil {
					poolRun.set.Adopt(id, instance, time.Now(), release)
					adopted++
					continue
				}
				log.Printf("Cloud Run worker pool %s: adopt instance %s: %v", pool.Name, id, err)
			}
		}
		removeCloudRunContainers(pool.Name, group)
	}
	if adopted == 0 {
		return
	}
	cloudRunWorkerPoolRuns.Lock()
	cloudRunWorkerPoolRuns.byName[pool.Name] = poolRun
	cloudRunWorkerPoolRuns.Unlock()
}

// adoptCloudRunWorkerPoolInstance takes over the containers of a worker pool
// instance an earlier process started, with what to release once they have
// exited.
func adoptCloudRunWorkerPoolInstance(poolName, revision string, vpc *VpcAccess, containers []Container, volumes []Volume, byName map[string]placement.Existing) (*workload.Group, func(), error) {
	writable, err := cloudRunWritableBuckets(containers, volumes)
	if err != nil {
		return nil, nil, err
	}
	releaseMounts, err := gcsAcquireMounts(writable)
	if err != nil {
		return nil, nil, err
	}
	sink := newCloudRunAdoptedResourceLogSink(cloudRunWorkerPoolLogSink(poolName, revision))
	handles := make([]placement.Handle, 0, len(containers))
	for _, c := range containers {
		handle, err := adoptCloudRunContainer(byName[c.Name].ID, sim.ContainerConfig{CancelGracePeriod: cloudRunStopGrace}, sink)
		if err != nil {
			for _, h := range handles {
				h.Cancel()
			}
			releaseMounts()
			return nil, nil, err
		}
		handles = append(handles, handle)
	}
	releaseAddress, err := gcpReserveAdoptedAddress(context.Background(), handles[0], poolName, vpc)
	if err != nil {
		for _, h := range handles {
			h.Cancel()
		}
		releaseMounts()
		return nil, nil, err
	}
	cpu := cloudRunAdoptCPU(resourceProject(poolName), resourceLocation(poolName), containers)
	release := func() {
		releaseAddress()
		releaseMounts()
		cpu.release()
	}
	return &workload.Group{Main: handles[0], Sidecars: handles[1:]}, release, nil
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
				if err := adoptCloudRunServiceInstance(inst.Name, specSig, inst.Containers, inst.Volumes, inst.VpcAccess, byName, newCloudRunAdoptedResourceLogSink(cloudRunInstanceLogSink(inst.Name))); err == nil {
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
// the Cloud Run instance name, serving already.
func adoptCloudRunServiceInstance(name, specSig string, containers []Container, volumes []Volume, vpc *VpcAccess, byName map[string]placement.Existing, sink sim.LogSink) error {
	inst, err := adoptCloudRunInstanceContainers(name, specSig, containers, volumes, vpc, byName, sink)
	if err != nil {
		return err
	}
	cloudRunServiceInstances.Lock()
	previous := cloudRunServiceInstances.byName[name]
	cloudRunServiceInstances.byName[name] = inst
	cloudRunServiceInstances.Unlock()
	stopCloudRunServiceInstance(previous)
	return nil
}

// adoptCloudRunInstanceContainers takes over the running containers byName
// holds as one instance of name: they started and passed their startup probes
// under the earlier process.
func adoptCloudRunInstanceContainers(name, specSig string, containers []Container, volumes []Volume, vpc *VpcAccess, byName map[string]placement.Existing, sink sim.LogSink) (*cloudRunServiceInstance, error) {
	order, err := cloudRunContainerStartOrder(containers)
	if err != nil {
		return nil, err
	}
	writable, err := cloudRunWritableBuckets(containers, volumes)
	if err != nil {
		return nil, err
	}
	releaseMounts, err := gcsAcquireMounts(writable)
	if err != nil {
		return nil, err
	}
	var handles []placement.Handle
	byIndex := make([]placement.Handle, len(containers))
	for _, i := range order {
		handle, err := adoptCloudRunContainer(byName[containers[i].Name].ID, sim.ContainerConfig{CancelGracePeriod: cloudRunStopGrace}, sink)
		if err != nil {
			for _, h := range handles {
				h.Cancel()
			}
			releaseMounts()
			return nil, err
		}
		handles = append(handles, handle)
		byIndex[i] = handle
	}
	owner := byIndex[order[0]]
	address, err := cloudRunContainerRoute(context.Background(), owner, cloudRunContainerPort(containers[0]))
	if err == nil {
		var releaseAddress func()
		if releaseAddress, err = gcpReserveAdoptedAddress(context.Background(), owner, name, vpc); err == nil {
			mounts := releaseMounts
			releaseMounts = func() {
				mounts()
				releaseAddress()
			}
		}
	}
	if err != nil {
		for _, h := range handles {
			h.Cancel()
		}
		releaseMounts()
		return nil, err
	}
	_, stop := context.WithCancel(context.Background())
	cpu := cloudRunAdoptCPU(resourceProject(name), resourceLocation(name), containers)
	mounts := releaseMounts
	inst := &cloudRunServiceInstance{
		specSig: specSig,
		stop:    stop,
		ready:   make(chan struct{}),
		address: address,
		releaseMounts: func() {
			mounts()
			cpu.release()
		},
		cpu:     cpu,
		ingress: byIndex[0],
		handles: handles,
		group:   &workload.Group{Main: byIndex[0], Sidecars: byIndex[1:]},
	}
	close(inst.ready)
	return inst, nil
}

// adoptCloudRunServiceInstances takes over, for every revision a stored
// service's traffic reaches, the instance an earlier process left serving it,
// so the next request reaches it rather than starting another, and removes
// every other service container that process left: instances of revisions no
// longer reached and of services since deleted.
func adoptCloudRunServiceInstances() {
	groups, ids, err := cloudRunExistingGroups(map[string]string{cloudRunServiceLabel: ""})
	if err != nil {
		log.Printf("Cloud Run services: list the instances an earlier process left: %v", err)
		return
	}
	for _, id := range ids {
		group := groups[id]
		name := group[0].Labels[cloudRunResourceLabel]
		if !strings.Contains(name, "/services/") {
			// A Cloud Run instance runs on the same containers; adoptCloudRunInstance owns them.
			continue
		}
		if !adoptCloudRunServiceInstanceGroup(name, id, group) {
			removeCloudRunContainers(name, group)
		}
	}
	if crv2Services != nil {
		for _, svc := range crv2Services.List() {
			keepCloudRunRevisionsWarm(svc)
		}
	}
}

// adoptCloudRunServiceInstanceGroup adopts group as an instance of the
// revision name when the service's traffic still reaches it and the group runs
// its template whole.
func adoptCloudRunServiceInstanceGroup(name, groupID string, group []placement.Existing) bool {
	if crv2Services == nil || crv2Revisions == nil {
		return false
	}
	serviceName, _, isRevision := strings.Cut(name, "/revisions/")
	if !isRevision {
		return false
	}
	svc, ok := crv2Services.Get(serviceName)
	if !ok || !cloudRunRoutedRevisions(svc)[name] {
		return false
	}
	rev, ok := crv2Revisions.Get(name)
	if !ok || len(rev.Containers) == 0 {
		return false
	}
	containers, volumes := rev.Containers, rev.Volumes
	specSig := serviceContainersSignature(containers, volumes)
	byName, ok := cloudRunAdoptable(group, groupID, specSig, cloudRunContainerLabel, containers)
	if !ok {
		return false
	}
	project := resourceProject(name)
	revisionSink := cloudRunRevisionLogSink(name, groupID)
	sink := newCloudRunAdoptedLogSink(revisionSink, project, revisionSink.resource, map[string]string{"instanceId": groupID})
	run := cloudRunRevisionRunFor(svc, rev)
	if len(run.set.Replicas()) >= run.max {
		return false
	}
	inst, err := adoptCloudRunInstanceContainers(name, specSig, containers, volumes, rev.VpcAccess, byName, sink)
	if err != nil {
		log.Printf("Cloud Run service %s: adopt instance %s: %v", name, groupID, err)
		return false
	}
	run.mu.Lock()
	run.instances[groupID] = inst
	run.holds[groupID] = inst.cpu
	run.mu.Unlock()
	run.set.Adopt(groupID, inst.group, time.Now(), func() {
		inst.stop()
		inst.releaseMounts()
	})
	return true
}
