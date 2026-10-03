package main

import (
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Run worker pools and instances reconcile asynchronously: a create,
// update or start answers with an operation that completes once every
// instance has started and passed its startup probes, and fails with the start
// error otherwise. While it runs the resource reports `reconciling` and a
// CONDITION_RECONCILING `Ready` condition.

const (
	cloudRunWorkerPoolType = "type.googleapis.com/google.cloud.run.v2.WorkerPool"
	cloudRunInstanceType   = "type.googleapis.com/google.cloud.run.v2.Instance"
)

// cloudRunDeployFailureCode is the google.rpc.Code a Cloud Run deploy whose
// containers fail to start ends its operation with: INTERNAL, as Terraform
// reports a revision that "is not ready and cannot serve traffic".
const cloudRunDeployFailureCode = 13

// cloudRunCancelledCode ends a reconciliation operation a client cancelled:
// CANCELLED, which AIP-151 says a successfully cancelled operation carries.
const cloudRunCancelledCode = 1

// cloudRunReconcileAbortedCode ends the reconciliation operations of a
// resource deleted before they completed: ABORTED.
const cloudRunReconcileAbortedCode = 10

// startCloudRunReconcileOperation records the operation a create, update or
// start returns: not done, with the resource as its metadata.
func startCloudRunReconcileOperation(project, location string, resource any, typeName string) Operation {
	op := Operation{
		Name:     gcpLocationOperationName(project, location, sim.NewUUID()),
		Metadata: gcpResourceOperationMetadata(gcpOperationAny(resource, typeName)),
	}
	crOperations.Put(op.Name, op)
	return op
}

// currentCloudRunOperation reads op back from the store, where a
// reconciliation that settled at once has already completed it.
func currentCloudRunOperation(op Operation) Operation {
	if current, ok := crOperations.Get(op.Name); ok {
		return current
	}
	return op
}

// finishCloudRunReconcileOperations completes every unfinished reconciliation
// operation of the resource named name: with the settled resource as its
// response, or, when failure is not empty, with code and failure as its error.
func finishCloudRunReconcileOperations(name, typeName string, resource any, code int, failure string) {
	if crOperations == nil {
		return
	}
	pending := crOperations.Filter(func(op Operation) bool {
		return !op.Done && op.Metadata["@type"] == typeName && op.Metadata["name"] == name
	})
	for _, op := range pending {
		gcpFinishOperation(op.Name, func(o *Operation) {
			if resource != nil {
				o.Metadata = gcpResourceOperationMetadata(gcpOperationAny(resource, typeName))
			}
			if failure != "" {
				o.Error = &OperationError{Code: code, Message: failure}
				return
			}
			o.Response = gcpOperationAny(resource, typeName)
		})
	}
}

func reconcilingCondition(now string) Condition {
	return Condition{Type: "Ready", State: "CONDITION_RECONCILING", LastTransitionTime: now}
}

// beginCloudRunWorkerPoolReconcile marks a pool as reconciling the generation
// it carries.
func beginCloudRunWorkerPoolReconcile(pool *WorkerPoolV2) {
	ready := reconcilingCondition(pool.UpdateTime)
	pool.Reconciling = true
	pool.TerminalCondition = &ready
	pool.Conditions = []Condition{ready}
}

// beginCloudRunInstanceReconcile marks an instance as reconciling.
func beginCloudRunInstanceReconcile(inst *InstanceV2) {
	ready := reconcilingCondition(inst.UpdateTime)
	inst.Reconciling = true
	inst.TerminalCondition = &ready
	inst.Conditions = []Condition{ready}
}

// settleCloudRunWorkerPool ends the reconciliation of the pool's generation:
// on success the revision it deployed becomes the pool's ready revision; on
// failure the pool keeps its last ready revision and reports the start error.
// A newer generation's reconciliation supersedes it.
func settleCloudRunWorkerPool(name string, generation int64, startErr error) {
	if crv2WorkerPools == nil {
		return
	}
	var settled WorkerPoolV2
	var message string
	if !crv2WorkerPools.Update(name, func(p *WorkerPoolV2) {
		if p.Generation != generation || !p.Reconciling {
			return
		}
		now := nowTimestamp()
		revision := p.LatestCreatedRevision[strings.LastIndex(p.LatestCreatedRevision, "/")+1:]
		ready := Condition{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now}
		if startErr != nil {
			message = fmt.Sprintf("Revision '%s' is not ready and cannot serve traffic. %v", revision, startErr)
			ready = Condition{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Message: message}
		} else {
			p.LatestReadyRevision = p.LatestCreatedRevision
			p.ObservedGeneration = p.Generation
			p.InstanceSplitStatuses = cloudRunWorkerPoolSplitStatuses(p.InstanceSplits, revision)
		}
		p.Reconciling = false
		p.TerminalCondition = &ready
		p.Conditions = []Condition{ready}
		settled = *p
	}) || settled.Name == "" {
		return
	}
	if crv2WorkerPoolRevisions != nil {
		crv2WorkerPoolRevisions.Update(settled.LatestCreatedRevision, func(rev *RevisionV2) {
			rev.Conditions = []Condition{*settled.TerminalCondition}
		})
	}
	if startErr != nil {
		finishCloudRunReconcileOperations(name, cloudRunWorkerPoolType, settled, cloudRunDeployFailureCode, message)
		return
	}
	finishCloudRunReconcileOperations(name, cloudRunWorkerPoolType, settled, 0, "")
}

// cloudRunWorkerPoolSplitStatuses is the instance split a pool serves once
// revision is ready: its configured splits with the latest revision named, or
// every instance on revision.
func cloudRunWorkerPoolSplitStatuses(splits []InstanceSplit, revision string) []InstanceSplit {
	if len(splits) == 0 {
		return []InstanceSplit{{Type: "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST", Percent: 100, Revision: revision}}
	}
	out := make([]InstanceSplit, len(splits))
	for i, split := range splits {
		out[i] = split
		if split.Type == "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST" {
			out[i].Revision = revision
		}
	}
	return out
}

// settleCloudRunInstance ends the reconciliation of the instance run started:
// its Ready condition succeeds once its containers have started, or fails
// with the start error.
func settleCloudRunInstance(name string, run *cloudRunRun, startErr error) {
	if crv2Instances == nil || !cloudRunInstanceRunIs(name, run) {
		return
	}
	var settled InstanceV2
	var message string
	if !crv2Instances.Update(name, func(i *InstanceV2) {
		now := nowTimestamp()
		ready := Condition{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now}
		if startErr != nil {
			message = fmt.Sprintf("Instance '%s' is not ready. %v", name[strings.LastIndex(name, "/")+1:], startErr)
			ready = Condition{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Message: message}
		} else {
			i.ObservedGeneration = i.Generation
		}
		i.Reconciling = false
		i.TerminalCondition = &ready
		i.Conditions = []Condition{ready}
		settled = *i
	}) {
		return
	}
	if startErr != nil {
		finishCloudRunReconcileOperations(name, cloudRunInstanceType, settled, cloudRunDeployFailureCode, message)
		return
	}
	finishCloudRunReconcileOperations(name, cloudRunInstanceType, settled, 0, "")
}

// endCloudRunInstance records an instance its restart policy did not restart,
// or that failed past its restarts: a clean exit stops it, anything else
// fails it.
func endCloudRunInstance(name string, run *cloudRunRun, exitCode int64, failure string) {
	if crv2Instances == nil || !cloudRunInstanceRunIs(name, run) {
		return
	}
	crv2Instances.Update(name, func(i *InstanceV2) {
		now := nowTimestamp()
		i.UpdateTime = now
		i.Reconciling = false
		ready := Condition{Type: "Ready", State: "CONDITION_PENDING", LastTransitionTime: now, Reason: "Stopped"}
		if failure != "" || exitCode != 0 {
			if failure == "" {
				failure = fmt.Sprintf("The instance's container exited with code %d.", exitCode)
			}
			ready = Condition{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Message: failure}
		}
		i.TerminalCondition = &ready
		i.Conditions = []Condition{ready}
		i.Etag = sim.NewUUID()
	})
}

func cloudRunInstanceRunIs(name string, run *cloudRunRun) bool {
	cloudRunInstanceRuns.Lock()
	defer cloudRunInstanceRuns.Unlock()
	return cloudRunInstanceRuns.byName[name] == run
}

// cloudRunInstanceStopped reports whether the instance is not meant to run:
// stopped by StopInstance or by its own exit, or failed past its restarts.
func cloudRunInstanceStopped(inst InstanceV2) bool {
	c := inst.TerminalCondition
	return c != nil && !inst.Reconciling && (c.Reason == "Stopped" || c.State == "CONDITION_FAILED")
}

// resumeCloudRunWorkloads starts the instances of every stored worker pool and
// every stored instance that is meant to run, which the previous simulator
// process ran until it stopped; reconciliations it left unfinished settle as
// they start.
func resumeCloudRunWorkloads() {
	if crv2WorkerPools != nil {
		for _, pool := range crv2WorkerPools.List() {
			if !pool.Reconciling && pool.TerminalCondition != nil && pool.TerminalCondition.State == "CONDITION_FAILED" {
				continue
			}
			runCloudRunWorkerPool(pool)
		}
	}
	if crv2Instances != nil {
		for _, inst := range crv2Instances.List() {
			if cloudRunInstanceStopped(inst) {
				continue
			}
			runCloudRunInstance(inst)
		}
	}
}

// cancelCloudRunReconcile stops the instances a worker pool's or instance's
// unfinished deploy was starting, fails its Ready condition as cancelled, and
// ends the deploy's operations CANCELLED.
func cancelCloudRunReconcile(name, typeName string) {
	cancelled := func(now string) Condition {
		return Condition{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Reason: "Cancelled",
			Message: "The deploy was cancelled before its instances started."}
	}
	var resource any
	switch typeName {
	case cloudRunWorkerPoolType:
		if crv2WorkerPools == nil {
			return
		}
		stopCloudRunWorkerPool(name)
		crv2WorkerPools.Update(name, func(p *WorkerPoolV2) {
			if !p.Reconciling {
				return
			}
			ready := cancelled(nowTimestamp())
			p.Reconciling = false
			p.TerminalCondition = &ready
			p.Conditions = []Condition{ready}
		})
		if pool, ok := crv2WorkerPools.Get(name); ok {
			resource = pool
		}
	case cloudRunInstanceType:
		if crv2Instances == nil {
			return
		}
		stopCloudRunInstance(name)
		crv2Instances.Update(name, func(i *InstanceV2) {
			if !i.Reconciling {
				return
			}
			ready := cancelled(nowTimestamp())
			i.Reconciling = false
			i.TerminalCondition = &ready
			i.Conditions = []Condition{ready}
		})
		if inst, ok := crv2Instances.Get(name); ok {
			resource = inst
		}
	}
	finishCloudRunReconcileOperations(name, typeName, resource, cloudRunCancelledCode, "The operation was cancelled.")
}
