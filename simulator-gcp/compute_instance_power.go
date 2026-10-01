package main

import (
	"context"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// gcpBootVM, gcpHaltVM and gcpDestroyVM move an instance's machine. A unit test
// on a host without nested KVM substitutes them to hold a move open on a
// channel and observe the instance and its operation around it.
var (
	gcpBootVM = gcpStartRealVM
	gcpHaltVM = func(ctx context.Context, inst *ComputeInstance) error {
		return gcpFabric.StopVM(ctx, inst.SelfLink, nil)
	}
	gcpDestroyVM = func(ctx context.Context, inst *ComputeInstance) error { return gcpDeleteRealVM(ctx, *inst) }
)

// computeInstanceMove is an instance verb that moves the machine. Compute
// Engine answers it with a RUNNING zone operation, holds the instance in the
// interim status while the machine moves, and settles the instance and the
// operation when the move ends.
type computeInstanceMove struct {
	verb string
	// refuse reports why the instance's current status does not allow the
	// verb, or nil.
	refuse  func(ComputeInstance) error
	interim ComputeInstanceStatus
	settled ComputeInstanceStatus
	// remove deletes the instance once the move succeeds instead of settling it.
	remove bool
	move   func(context.Context, *ComputeInstance) error
}

var (
	computeStartMove = computeInstanceMove{
		interim: ComputeInstanceStaging, settled: ComputeInstanceRunning,
		move: func(ctx context.Context, inst *ComputeInstance) error { return gcpBootVM(ctx, inst) },
	}
	computeStopMove = computeInstanceMove{
		verb: "stop", interim: ComputeInstanceStopping, settled: ComputeInstanceTerminated,
		move: func(ctx context.Context, inst *ComputeInstance) error { return gcpHaltVM(ctx, inst) },
	}
	computeDeleteMove = computeInstanceMove{
		verb: "delete", interim: ComputeInstanceStopping, remove: true,
		move: func(ctx context.Context, inst *ComputeInstance) error { return gcpDestroyVM(ctx, inst) },
	}
	computeSuspendMove = computeInstanceMove{
		verb: "suspend", interim: ComputeInstanceSuspending, settled: ComputeInstanceSuspended,
		refuse: func(inst ComputeInstance) error {
			if inst.Status != ComputeInstanceRunning {
				return errComputeInvalid("only a running instance can be suspended")
			}
			return nil
		},
		move: func(ctx context.Context, inst *ComputeInstance) error { return gcpHaltVM(ctx, inst) },
	}
	computeResumeMove = computeInstanceMove{
		verb: "resume", interim: ComputeInstanceStaging, settled: ComputeInstanceRunning,
		refuse: func(inst ComputeInstance) error {
			if inst.Status != ComputeInstanceSuspended {
				return errComputeInvalid("only a suspended instance can be resumed")
			}
			return nil
		},
		move: func(ctx context.Context, inst *ComputeInstance) error { return gcpBootVM(ctx, inst) },
	}
)

func (m computeInstanceMove) as(verb string) computeInstanceMove {
	m.verb = verb
	return m
}

func registerComputeInstancePower(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/zones/{zone}/instances/{name}"
	logger := srv.Logger()

	// run answers the verb with the operation and moves the machine behind it,
	// on a context of its own: a client that stops waiting must not take the
	// move down with it, and the request's context dies with the response.
	run := func(w http.ResponseWriter, r *http.Request, m computeInstanceMove) {
		project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")
		link := computeInstanceSelfLink(project, zone, name)
		var (
			refused error
			prior   ComputeInstanceStatus
			moving  ComputeInstance
		)
		found := gcpInstances.Update(link, func(inst *ComputeInstance) {
			if m.refuse != nil {
				if refused = m.refuse(*inst); refused != nil {
					return
				}
			}
			prior = inst.Status
			inst.Status = m.interim
			moving = *inst
		})
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if refused != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", refused)
			return
		}

		op := newComputeOpRecord(project, "zones/"+zone, link, m.verb)
		recordComputeOp(op)
		bg.Go(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), computeInstanceBootBudget)
			defer cancel()
			if err := m.move(ctx, &moving); err != nil {
				logger.Error().Err(err).
					Str("project", project).Str("zone", zone).Str("instance", name).Str("verb", m.verb).
					Msg("failed to move real Compute Engine instance")
				gcpInstances.Update(link, func(inst *ComputeInstance) { inst.Status = prior })
				computeOpFinish(op.Name, err)
				return
			}
			if m.remove {
				gcpInstances.Delete(link)
				computeOpFinish(op.Name, nil)
				return
			}
			settled := gcpInstances.Update(link, func(inst *ComputeInstance) {
				inst.Status = m.settled
				// A boot leases the interface its address.
				for i := range inst.NetworkInterfaces {
					if i < len(moving.NetworkInterfaces) && moving.NetworkInterfaces[i].NetworkIP != "" {
						inst.NetworkInterfaces[i].NetworkIP = moving.NetworkInterfaces[i].NetworkIP
					}
				}
			})
			if !settled {
				// The instance was deleted while its machine moved; a machine
				// the move left behind has no instance to belong to.
				if err := gcpDestroyVM(ctx, &moving); err != nil {
					logger.Error().Err(err).Str("instance", link).
						Msg("failed to tear down the machine of a deleted Compute Engine instance")
				}
			}
			computeOpFinish(op.Name, nil)
		})
		sim.WriteJSON(w, http.StatusOK, computeOpJSON(op))
	}

	srv.HandleFunc("DELETE "+base, func(w http.ResponseWriter, r *http.Request) {
		run(w, r, computeDeleteMove)
	})
	srv.HandleFunc("POST "+base+"/start", func(w http.ResponseWriter, r *http.Request) {
		run(w, r, computeStartMove.as("start"))
	})
	srv.HandleFunc("POST "+base+"/stop", func(w http.ResponseWriter, r *http.Request) {
		run(w, r, computeStopMove)
	})
	srv.HandleFunc("POST "+base+"/suspend", func(w http.ResponseWriter, r *http.Request) {
		run(w, r, computeSuspendMove)
	})
	srv.HandleFunc("POST "+base+"/resume", func(w http.ResponseWriter, r *http.Request) {
		run(w, r, computeResumeMove)
	})

	// The keys are supplied per disk and never stored: they exist for the
	// duration of the call, which is why an instance started this way looks
	// exactly like one started any other way afterwards.
	srv.HandleFunc("POST "+base+"/startWithEncryptionKey", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Disks []struct {
				Source string `json:"source"`
			} `json:"disks"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if len(req.Disks) == 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"startWithEncryptionKey needs the keys for the instance's encrypted disks")
			return
		}
		run(w, r, computeStartMove.as("startWithEncryptionKey"))
	})
}
