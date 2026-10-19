package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// azureReapplyIngress programs every running virtual machine with what the
// network security groups of its interface and its subnet allow now, one
// stage each, after a group, a rule, an interface or a subnet changed. A
// machine on no group receives everything, as on Azure; a backend that does
// not filter is left as it is.
func azureReapplyIngress() {
	if azureVMs == nil || azureNICs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	azureVMWorkloads.Lock()
	workloads := make([]*azureVMWorkload, 0, len(azureVMWorkloads.byID))
	for _, w := range azureVMWorkloads.byID {
		workloads = append(workloads, w)
	}
	azureVMWorkloads.Unlock()
	for _, w := range workloads {
		if w.handle == nil {
			continue
		}
		vm, ok := azureVMs.Get(w.metadata.VM.ID)
		if !ok || len(vm.Properties.NetworkProfile.NetworkInterfaces) == 0 {
			continue
		}
		nic, ok := azureNICs.Get(vm.Properties.NetworkProfile.NetworkInterfaces[0].ID)
		if !ok {
			continue
		}
		stages, err := azureIngressPacketStages(nic)
		if err != nil {
			log.Printf("compute: network security groups of %s: %v", vm.ID, err)
			continue
		}
		if err := w.handle.SetIngress(ctx, stages); err != nil && !errors.Is(err, placement.ErrNoIngressFilter) {
			log.Printf("compute: apply the network security groups of %s: %v", vm.ID, err)
		}
	}
}
