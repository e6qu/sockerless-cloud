package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/replicas"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// A Container Apps revision runs its replicas as one replica set: each replica
// is a pod of the revision's containers and the Dapr sidecar, in one network
// namespace, ready once its main container listens on the ingress target
// port. A replica whose main container exits restarts in place under its
// name and address, backing off as Kubernetes does for a container that
// keeps exiting.

const (
	acaRevisionLabel  = "sockerless-aca-revision"
	acaReplicaLabel   = "sockerless-aca-replica"
	acaContainerLabel = "sockerless-aca-container"
)

// acaRevisionRun is a revision's running replicas.
type acaRevisionRun struct {
	set    *replicas.Set
	scaler *acaScaler
	grace  time.Duration

	mu       sync.Mutex
	replicas map[string]*acaReplica
}

var acaRevisionRuns sync.Map // map[revisionID]*acaRevisionRun

func acaRevisionRunOf(revisionID string) (*acaRevisionRun, bool) {
	v, ok := acaRevisionRuns.Load(revisionID)
	if !ok {
		return nil, false
	}
	run, ok := v.(*acaRevisionRun)
	return run, ok
}

func (run *acaRevisionRun) add(replica *acaReplica) {
	run.mu.Lock()
	run.replicas[replica.name] = replica
	run.mu.Unlock()
}

func (run *acaRevisionRun) replica(name string) (*acaReplica, bool) {
	run.mu.Lock()
	defer run.mu.Unlock()
	replica, ok := run.replicas[name]
	return replica, ok
}

// remove forgets a replica that ended and releases its pod's address.
func (run *acaRevisionRun) remove(name string) {
	run.mu.Lock()
	replica, ok := run.replicas[name]
	delete(run.replicas, name)
	run.mu.Unlock()
	if ok && replica.release != nil {
		replica.release()
	}
}

// acaCurrentReplicas lists a revision's running replicas, oldest first.
func acaCurrentReplicas(revisionID string) []*acaReplica {
	run, ok := acaRevisionRunOf(revisionID)
	if !ok {
		return nil
	}
	run.mu.Lock()
	out := make([]*acaReplica, 0, len(run.replicas))
	for _, replica := range run.replicas {
		out = append(out, replica)
	}
	run.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].created.Before(out[j].created) })
	return out
}

// acaReplicaRestartBackoff is how long a revision waits before it replaces a
// replica that exited: Kubernetes' crash-loop back-off, doubling from 10
// seconds to 5 minutes with the replicas in a row that exited within 10
// minutes of starting.
func acaReplicaRestartBackoff(consecutive int) time.Duration {
	wait := 10 * time.Second
	for range consecutive - 1 {
		wait *= 2
		if wait >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return wait
}

// acaRevisionWorkload is what a revision's replicas start with: the network
// and the configuration they run under.
type acaRevisionWorkload struct {
	app         ContainerApp
	rev         acaRevision
	envID       string
	network     azureWorkloadNetwork
	subnetID    string
	netAliases  []string
	metadataEnv map[string]string
	secrets     map[string]string
}

func newACARevisionWorkload(ctx context.Context, app ContainerApp, rev acaRevision) (acaRevisionWorkload, error) {
	app.Properties.Template = rev.Template
	envID := acaAppEnvironmentID(app)
	// The environment's internal DNS resolves an app by its name and FQDNs,
	// which the replica's network aliases answer on the environment's network.
	network, subnetID, err := acaWorkloadNetwork(ctx, envID)
	if err != nil {
		return acaRevisionWorkload{}, err
	}
	netAliases := []string{app.Name}
	if appFqdn := acaAppFqdn(app); appFqdn != "" {
		netAliases = append(netAliases, appFqdn)
	}
	if rev.Fqdn != "" {
		netAliases = append(netAliases, rev.Fqdn)
	}
	metadataEnv, err := workloadMetadataEnv(app.ID, app.Identity, network.DNS)
	if err != nil {
		return acaRevisionWorkload{}, err
	}
	secrets, err := acaResolveSecrets(app.Identity, acaAppSecrets(app))
	if err != nil {
		return acaRevisionWorkload{}, &acaRevisionRequestError{err}
	}
	return acaRevisionWorkload{app: app, rev: rev, envID: envID, network: network, subnetID: subnetID,
		netAliases: netAliases, metadataEnv: metadataEnv, secrets: secrets}, nil
}

// members are the containers of one replica, the app's own and the Dapr
// sidecar, labelled with the revision and the replica they belong to. Each of
// the app's containers starts once its startup and readiness probes pass.
func (wl acaRevisionWorkload) members(replica *acaReplica) []workload.Member {
	containers := wl.app.Properties.Template.Containers
	members := make([]workload.Member, 0, len(containers)+1)
	for i, c := range containers {
		container := acaAppContainer(wl.app, c, replica.name, wl.envID, wl.metadataEnv, wl.secrets)
		container.Config.Labels[acaRevisionLabel] = wl.rev.ID
		container.Config.Labels[acaReplicaLabel] = replica.name
		container.Config.Labels[acaContainerLabel] = c.Name
		record, probes := replica.containers[i], acaContainerProbes(wl.app, c, i == 0)
		members = append(members, workload.Member{Container: container,
			Startup: func(ctx context.Context, member, owner placement.Handle) error {
				return record.startProbes(ctx, probes, member, owner, func() { owner.Stop() })
			}})
	}
	if d := containerAppDaprSpec(wl.app); d != nil {
		dapr := acaAppDaprContainer(wl.app, d, replica.name)
		dapr.Config.Labels[acaRevisionLabel] = wl.rev.ID
		dapr.Config.Labels[acaReplicaLabel] = replica.name
		dapr.Config.Labels[acaContainerLabel] = "daprd"
		members = append(members, workload.Member{Container: dapr})
	}
	return members
}

// newACARevisionRun is the replica set of a revision.
func newACARevisionRun(wl acaRevisionWorkload, scaling acaScaling) *acaRevisionRun {
	app, rev := wl.app, wl.rev
	run := &acaRevisionRun{grace: acaAppStopGrace(app), replicas: map[string]*acaReplica{}}
	defer func() {
		run.scaler = newACAScaler(run, scaling, acaQueueReader{appID: app.ID, identity: app.Identity, secrets: wl.secrets})
	}()
	names := make([]string, 0, len(app.Properties.Template.Containers))
	for _, c := range app.Properties.Template.Containers {
		names = append(names, c.Name)
	}
	initNames := make([]string, 0, len(app.Properties.Template.InitContainers))
	for _, c := range app.Properties.Template.InitContainers {
		initNames = append(initNames, c.Name)
	}
	run.set = replicas.New(replicas.Spec{
		NewID:   func() string { return acaReplicaName(rev.Name) },
		Restart: replicas.Always,
		InPlace: true,
		Backoff: acaReplicaRestartBackoff,
		Settled: 10 * time.Minute,
		Start: func(ctx context.Context, name string) (*workload.Group, func(), error) {
			replica, restarting := run.replica(name)
			if restarting {
				replica.restarting()
			} else {
				replica = newACAReplica(name, rev.Name, names, initNames)
				acaRecordSystemEvent(app.ID, wl.envID, app.Name, rev.Name, replica.name, "AssigningReplica",
					fmt.Sprintf("Replica '%s' has been scheduled to run on a node.", replica.name))
			}
			members := wl.members(replica)
			// The pod holds its address across restarts in place; the
			// replica's record releases it once the replica ends.
			if !restarting {
				address, release, err := azureWorkloadAddress(ctx, wl.network, wl.subnetID, members[0].Config.Name)
				if err != nil {
					return nil, nil, err
				}
				replica.address, replica.release = address, release
			}
			backend, err := azurePlacementBackend(placementContainerApps)
			if err != nil {
				if !restarting {
					replica.release()
				}
				return nil, nil, err
			}
			pod := func(cfg *sim.ContainerConfig) {
				// A replica is a pod, and a pod's hostname is its name.
				cfg.Hostname = replica.name
				cfg.Network = wl.network.Network
				cfg.NetworkAliases = wl.netAliases
				cfg.DNS = wl.network.resolvers()
				cfg.ExtraHosts = workloadhost.ExtraHosts()
				cfg.IPv4Address = replica.address
			}
			// A pod runs its init containers once, not again when its
			// containers restart in place.
			if !restarting {
				if err := wl.runInitContainers(ctx, backend, replica, pod); err != nil {
					replica.release()
					return nil, nil, err
				}
			}
			group, err := workload.StartGroup(ctx, backend, members, workload.StartOptions{
				Sink: func(i int) sim.LogSink {
					if i < len(replica.containers) {
						return replica.containers[i].sink(app, rev.Name)
					}
					return &acaAppLogSink{app: app, container: "daprd"}
				},
				Owner: pod,
				Placed: func(i int, h placement.Handle) {
					if i < len(replica.containers) {
						replica.containers[i].track(h, app.ID, wl.envID, app.Name, rev.Name, replica.name)
					}
				},
			})
			if err != nil {
				if !restarting {
					releaseAfterStartError(err, replica.release)
					return nil, nil, err
				}
				// The next restart takes the pod's address again once the
				// members this one placed have left it.
				var startErr *workload.StartError
				if errors.As(err, &startErr) {
					<-startErr.Stopped()
				}
				return nil, nil, err
			}
			run.add(replica)
			return group, nil, nil
		},
	}, replicas.Hooks{
		Exited: func(r *replicas.Replica, _ sim.ProcessResult) {
			if !slices.Contains(run.set.Replicas(), r) {
				run.remove(r.ID)
			}
		},
		Stopped: func(r *replicas.Replica) { run.remove(r.ID) },
	})
	return run
}

// releaseAfterStartError releases what a replica that failed to start held
// once the members it placed have exited.
func releaseAfterStartError(err error, release func()) {
	var startErr *workload.StartError
	if errors.As(err, &startErr) {
		bg.Go(func() {
			<-startErr.Stopped()
			release()
		})
		return
	}
	release()
}

// startACARevisionReplicas starts a revision's replicas: the revision's
// template run under the app's current configuration, replacing the replicas
// it ran before once the new ones run.
func startACARevisionReplicas(ctx context.Context, app ContainerApp, rev acaRevision) error {
	scaling := acaRevisionScaling(rev.Template, acaIngressEnabled(app))
	want := scaling.baseline()
	if want == 0 && !scaling.demandDriven() {
		// Nothing can start a replica of a revision with no minimum and no
		// rule its requests meet.
		stopACAAppReplicas(rev.ID)
		return nil
	}
	wl, err := newACARevisionWorkload(ctx, app, rev)
	if err != nil {
		return err
	}
	run := newACARevisionRun(wl, scaling)
	if err := run.set.Scale(ctx, want); err != nil {
		bg.Go(func() { run.stop(context.Background()) })
		return err
	}
	if previous, ok := acaRevisionRuns.Swap(rev.ID, run); ok {
		if prev, ok := previous.(*acaRevisionRun); ok {
			bg.Go(func() { prev.stop(context.Background()) })
		}
	}
	return nil
}

// stop stops the revision's scaling and its replicas, and returns once they
// have exited.
func (run *acaRevisionRun) stop(ctx context.Context) {
	run.scaler.close()
	_ = run.set.Stop(ctx)
}

// stopACAAppReplicas stops the replicas of one revision in the background;
// the revision holds none from here on.
func stopACAAppReplicas(revisionID string) {
	if v, ok := acaRevisionRuns.LoadAndDelete(revisionID); ok {
		if run, ok := v.(*acaRevisionRun); ok {
			bg.Go(func() { run.stop(context.Background()) })
		}
	}
}

// stopACAAppReplicasAndWait stops the replicas of one revision and returns
// once they have exited.
func stopACAAppReplicasAndWait(revisionID string) {
	if v, ok := acaRevisionRuns.LoadAndDelete(revisionID); ok {
		if run, ok := v.(*acaRevisionRun); ok {
			run.stop(context.Background())
		}
	}
}

// pick is the next ready replica of the revision in turn.
func (run *acaRevisionRun) pick() (*workload.Group, bool) {
	_, group, ok := run.set.Pick(func(r *replicas.Replica) bool {
		replica, ok := run.replica(r.ID)
		return ok && replica.ready()
	})
	return group, ok
}

// acaAcquireReplica is the replica the ingress forwards a request to: the
// next ready one, or, for a revision the HTTP rule scaled to zero, one it
// starts for the request, once it is ready.
func acaAcquireReplica(ctx context.Context, revisionID string) (*workload.Group, bool) {
	run, ok := acaRevisionRunOf(revisionID)
	if !ok {
		return nil, false
	}
	run.scaler.record(time.Now())
	for {
		changed := acaReadinessChange()
		if group, ok := run.pick(); ok {
			return group, true
		}
		if err := run.scaler.activate(ctx); err != nil || len(run.set.Replicas()) == 0 {
			return nil, false
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// acaRevisionServes reports whether the ingress can send a revision requests:
// it has a replica running, or its HTTP rule starts one for a request.
func acaRevisionServes(revisionID string) bool {
	run, ok := acaRevisionRunOf(revisionID)
	if !ok {
		return false
	}
	if run.scaler.scaling.concurrency > 0 {
		return true
	}
	for _, r := range run.set.Replicas() {
		if _, running := r.Group(); running {
			return true
		}
	}
	return false
}

// adoptACAAppReplicas takes over, for every active revision of every stored
// app, the replicas an earlier process of this persistent simulator left
// running, and starts the rest the revision's template asks for in the
// background. Containers
// of revisions no longer active, and of replicas missing a member, are
// removed.
func adoptACAAppReplicas(ctx context.Context) {
	if !sim.HasPersistentWorkloadIdentity() {
		return
	}
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		log.Printf("Container Apps: adopt replicas: %v", err)
		return
	}
	existing, err := backend.Find(ctx, map[string]string{acaRevisionLabel: ""})
	if err != nil {
		log.Printf("Container Apps: find the replicas an earlier process left: %v", err)
		return
	}
	byRevision := map[string]map[string][]placement.Existing{}
	for _, e := range existing {
		rev, replica := e.Labels[acaRevisionLabel], e.Labels[acaReplicaLabel]
		if byRevision[rev] == nil {
			byRevision[rev] = map[string][]placement.Existing{}
		}
		byRevision[rev][replica] = append(byRevision[rev][replica], e)
	}
	for revID, groups := range byRevision {
		rev, ok := acaRevisions.Get(revID)
		app, appOK := acaApps.Get(rev.AppID)
		scaling := acaRevisionScaling(rev.Template, acaIngressEnabled(app))
		if !ok || !appOK || !rev.Active || app.Properties.RunningStatus == acaStopped || (scaling.baseline() == 0 && !scaling.demandDriven()) {
			for _, group := range groups {
				removeACAContainers(ctx, backend, group)
			}
			continue
		}
		if err := adoptACARevision(ctx, backend, app, rev, groups); err != nil {
			log.Printf("Container Apps: adopt revision %s: %v", rev.Name, err)
		}
	}
	for _, app := range acaApps.List() {
		if app.Properties.RunningStatus == acaStopped {
			continue
		}
		for _, rev := range acaAppRevisions(app.ID) {
			scaling := acaRevisionScaling(rev.Template, acaIngressEnabled(app))
			if _, running := acaRevisionRunOf(rev.ID); rev.Active && !running && (scaling.baseline() > 0 || scaling.demandDriven()) {
				bg.Go(func() {
					if err := startACARevisionReplicas(context.Background(), app, rev); err != nil {
						log.Printf("Container Apps: start revision %s: %v", rev.Name, err)
						return
					}
					if current, ok := acaRevisions.Get(rev.ID); !ok || !current.Active {
						stopACAAppReplicas(rev.ID)
					}
				})
			}
		}
	}
}

func removeACAContainers(ctx context.Context, backend placement.Backend, group []placement.Existing) {
	for _, e := range group {
		if err := backend.Remove(ctx, e.ID); err != nil {
			log.Printf("Container Apps: remove container %s: %v", e.ID, err)
		}
	}
}

// adoptACARevision adopts the complete, running replicas of a revision up to
// its replica count, removes the others, and starts what is missing.
func adoptACARevision(ctx context.Context, backend placement.Backend, app ContainerApp, rev acaRevision, groups map[string][]placement.Existing) error {
	wl, err := newACARevisionWorkload(ctx, app, rev)
	if err != nil {
		return err
	}
	scaling := acaRevisionScaling(rev.Template, acaIngressEnabled(app))
	run := newACARevisionRun(wl, scaling)
	want := scaling.baseline()
	if scaling.demandDriven() {
		// The replicas demand had raised run on; the cooldown scales them in.
		want = min(max(want, len(groups)), scaling.max)
	}
	names := make([]string, 0, len(rev.Template.Containers))
	for _, c := range rev.Template.Containers {
		names = append(names, c.Name)
	}
	if containerAppDaprSpec(app) != nil {
		names = append(names, "daprd")
	}
	adopted := 0
	for replicaName, group := range groups {
		byName := map[string]placement.Existing{}
		complete := true
		for _, e := range group {
			byName[e.Labels[acaContainerLabel]] = e
			complete = complete && e.Running
		}
		for _, name := range names {
			if _, ok := byName[name]; !ok {
				complete = false
			}
		}
		if adopted >= want || !complete || len(byName) != len(names) {
			removeACAContainers(ctx, backend, group)
			continue
		}
		group, release, err := adoptACAReplica(ctx, backend, wl, run, replicaName, byName)
		if err != nil {
			log.Printf("Container Apps: adopt replica %s: %v", replicaName, err)
			removeACAContainers(ctx, backend, groups[replicaName])
			continue
		}
		run.set.Adopt(replicaName, group, time.Now(), release)
		adopted++
	}
	acaRevisionRuns.Store(rev.ID, run)
	bg.Go(func() {
		if err := run.set.Scale(context.Background(), want); err != nil {
			log.Printf("Container Apps: start the replicas revision %s is missing: %v", rev.Name, err)
		}
	})
	return nil
}

// adoptACAReplica takes over one replica's containers and the address its
// pod holds.
func adoptACAReplica(ctx context.Context, backend placement.Backend, wl acaRevisionWorkload, run *acaRevisionRun, replicaName string, byName map[string]placement.Existing) (*workload.Group, func(), error) {
	app, rev := wl.app, wl.rev
	names := make([]string, 0, len(app.Properties.Template.Containers))
	for _, c := range app.Properties.Template.Containers {
		names = append(names, c.Name)
	}
	initNames := make([]string, 0, len(app.Properties.Template.InitContainers))
	for _, c := range app.Properties.Template.InitContainers {
		initNames = append(initNames, c.Name)
	}
	replica := newACAReplica(replicaName, rev.Name, names, initNames)
	// The pod's init containers completed before its containers ran.
	for _, c := range replica.initContainers {
		c.terminated = true
	}
	members := wl.members(replica)
	handles := make([]placement.Handle, 0, len(members))
	cancel := func() {
		for _, h := range handles {
			h.Cancel()
		}
	}
	for i, m := range members {
		var sink sim.LogSink = &acaAppLogSink{app: app, container: "daprd"}
		if i < len(replica.containers) {
			sink = replica.containers[i].sink(app, rev.Name)
		}
		label := m.Name
		if i >= len(replica.containers) {
			label = "daprd"
		}
		handle, err := backend.Adopt(ctx, byName[label].ID, m.Config, sink)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		handles = append(handles, handle)
		if i < len(replica.containers) {
			replica.containers[i].track(handle, app.ID, wl.envID, app.Name, rev.Name, replica.name)
		}
	}
	address, err := handles[0].Address(ctx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	release, err := azureAdoptWorkloadAddress(ctx, wl.network, wl.subnetID, members[0].Config.Name, address)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	replica.address, replica.release = address, release
	// The replica ran ready before the restart; its probes carry on.
	for i, c := range app.Properties.Template.Containers {
		replica.containers[i].setReady(true)
		replica.containers[i].watchProbes(acaContainerProbes(app, c, i == 0), handles[i], address, func() { handles[0].Stop() })
	}
	run.add(replica)
	return &workload.Group{Main: handles[0], Sidecars: handles[1:]}, nil, nil
}

// acaReplicaName names a replica of revision as Container Apps does:
// <revision>-<10 characters>-<5 characters>.
func acaReplicaName(revision string) string {
	return fmt.Sprintf("%s-%s-%s", revision, randomSuffix(10), randomSuffix(5))
}

// acaIngressEnabled reports whether the app takes traffic through ingress.
func acaIngressEnabled(app ContainerApp) bool {
	return app.Properties.Configuration != nil && app.Properties.Configuration.Ingress != nil
}

// runInitContainers runs the template's init containers one after another
// with the pod's address and hostname, each to a zero exit, before the app's
// containers start.
func (wl acaRevisionWorkload) runInitContainers(ctx context.Context, backend placement.Backend, replica *acaReplica, pod func(*sim.ContainerConfig)) error {
	for i, c := range wl.app.Properties.Template.InitContainers {
		container := acaAppContainer(wl.app, c, replica.name, wl.envID, wl.metadataEnv, wl.secrets)
		container.Config.Name += "-init"
		container.Config.Labels[acaRevisionLabel] = wl.rev.ID
		container.Config.Labels[acaReplicaLabel] = replica.name
		container.Config.Labels[acaContainerLabel] = c.Name
		pod(&container.Config)
		record := replica.initContainers[i]
		handle, err := workload.Place(ctx, backend, "init", container, record.sink(wl.app, wl.rev.Name))
		if err != nil {
			return err
		}
		record.track(handle, wl.app.ID, wl.envID, wl.app.Name, wl.rev.Name, replica.name)
		select {
		case <-handle.Exited():
		case <-ctx.Done():
			handle.Cancel()
			<-handle.Exited()
			return ctx.Err()
		}
		if result := handle.Wait(); result.Error != nil || result.ExitCode != 0 {
			return fmt.Errorf("init container '%s' exited %d: %v", c.Name, result.ExitCode, result.Error)
		}
	}
	return nil
}
