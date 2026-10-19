package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// gcpIngressStages is what an instance's first interface may receive: one
// stage of the network's ingress firewall rules that target the instance,
// in Compute Engine's order, which drops what no rule allows, as the
// implied deny-ingress rule does.
func gcpIngressStages(inst ComputeInstance) ([][]netrules.PacketRule, error) {
	if len(inst.NetworkInterfaces) == 0 {
		return nil, errors.New("the instance has no network interface")
	}
	rules, err := gcpIngressPacketRules(inst, inst.NetworkInterfaces[0])
	if err != nil {
		return nil, err
	}
	return [][]netrules.PacketRule{rules}, nil
}

// gcpReapplyIngress programs every running instance's machine with what
// the firewall rules allow it now: a rule's change, an instance's tags, and
// a member's arrival or departure, which a rule's source tags expand over,
// each change what an instance may receive. A backend that does not filter
// is left as it is.
func gcpReapplyIngress() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, running := range gcpRunningInstances() {
		link, handle := running.Key, running.Handle
		inst, ok := gcpInstances.Get(link)
		if !ok {
			continue
		}
		stages, err := gcpIngressStages(inst)
		if err != nil {
			log.Printf("compute: firewall rules of %s: %v", link, err)
			continue
		}
		if err := handle.SetIngress(ctx, stages); err != nil && !errors.Is(err, placement.ErrNoIngressFilter) {
			log.Printf("compute: apply the firewall rules of %s: %v", link, err)
		}
	}
}

// gcpReapplyIngressAfter is the realize step of a firewall write that goes
// through the typed update: the rules reach every machine before the
// operation is answered.
func gcpReapplyIngressAfter(context.Context) error {
	gcpReapplyIngress()
	return nil
}
