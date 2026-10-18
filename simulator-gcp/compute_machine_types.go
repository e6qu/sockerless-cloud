package main

import (
	"fmt"
	"sort"
	"strings"
)

// gcpMachineType is one entry of the machine type catalog: what
// machineTypes.get reports, and the shape a container backend gives an
// instance of the type.
type gcpMachineType struct {
	Name        string
	Description string
	GuestCpus   int
	MemoryMb    int
	// CPUs is the compute an instance of the type gets: a shared-core type
	// holds a fraction of its guest CPUs.
	CPUs         float64
	SharedCpu    bool
	Architecture string
}

// gcpMachineTypes is the catalog of machine types the simulator offers, with
// the vCPU and memory Compute Engine documents for each. A type the catalog
// does not hold does not exist in any zone.
var gcpMachineTypes = map[string]gcpMachineType{
	"f1-micro":       {Description: "1 vCPU (shared physical core) and 0.6 GB RAM", GuestCpus: 1, MemoryMb: 614, CPUs: 0.2, SharedCpu: true, Architecture: "X86_64"},
	"g1-small":       {Description: "1 vCPU (shared physical core) and 1.7 GB RAM", GuestCpus: 1, MemoryMb: 1740, CPUs: 0.5, SharedCpu: true, Architecture: "X86_64"},
	"e2-micro":       {Description: "Efficient Instance, 2 vCPUs (shared), 1 GB RAM", GuestCpus: 2, MemoryMb: 1024, CPUs: 0.25, SharedCpu: true, Architecture: "X86_64"},
	"e2-small":       {Description: "Efficient Instance, 2 vCPUs (shared), 2 GB RAM", GuestCpus: 2, MemoryMb: 2048, CPUs: 0.5, SharedCpu: true, Architecture: "X86_64"},
	"e2-medium":      {Description: "Efficient Instance, 2 vCPUs (shared), 4 GB RAM", GuestCpus: 2, MemoryMb: 4096, CPUs: 1, SharedCpu: true, Architecture: "X86_64"},
	"e2-standard-2":  {Description: "Efficient Instance, 2 vCPUs, 8 GB RAM", GuestCpus: 2, MemoryMb: 8192, CPUs: 2, Architecture: "X86_64"},
	"e2-standard-4":  {Description: "Efficient Instance, 4 vCPUs, 16 GB RAM", GuestCpus: 4, MemoryMb: 16384, CPUs: 4, Architecture: "X86_64"},
	"e2-standard-8":  {Description: "Efficient Instance, 8 vCPUs, 32 GB RAM", GuestCpus: 8, MemoryMb: 32768, CPUs: 8, Architecture: "X86_64"},
	"n1-standard-1":  {Description: "1 vCPU, 3.75 GB RAM", GuestCpus: 1, MemoryMb: 3840, CPUs: 1, Architecture: "X86_64"},
	"n1-standard-2":  {Description: "2 vCPUs, 7.5 GB RAM", GuestCpus: 2, MemoryMb: 7680, CPUs: 2, Architecture: "X86_64"},
	"n1-standard-4":  {Description: "4 vCPUs, 15 GB RAM", GuestCpus: 4, MemoryMb: 15360, CPUs: 4, Architecture: "X86_64"},
	"n1-standard-8":  {Description: "8 vCPUs, 30 GB RAM", GuestCpus: 8, MemoryMb: 30720, CPUs: 8, Architecture: "X86_64"},
	"n2-standard-2":  {Description: "2 vCPUs 8 GB RAM", GuestCpus: 2, MemoryMb: 8192, CPUs: 2, Architecture: "X86_64"},
	"n2-standard-4":  {Description: "4 vCPUs 16 GB RAM", GuestCpus: 4, MemoryMb: 16384, CPUs: 4, Architecture: "X86_64"},
	"n2-standard-8":  {Description: "8 vCPUs 32 GB RAM", GuestCpus: 8, MemoryMb: 32768, CPUs: 8, Architecture: "X86_64"},
	"n2d-standard-2": {Description: "2 vCPUs 8 GB RAM", GuestCpus: 2, MemoryMb: 8192, CPUs: 2, Architecture: "X86_64"},
	"n2d-standard-4": {Description: "4 vCPUs 16 GB RAM", GuestCpus: 4, MemoryMb: 16384, CPUs: 4, Architecture: "X86_64"},
	"c2-standard-4":  {Description: "Compute Optimized: 4 vCPUs, 16 GB RAM", GuestCpus: 4, MemoryMb: 16384, CPUs: 4, Architecture: "X86_64"},
	"c3-standard-4":  {Description: "4 vCPUs 16 GB RAM", GuestCpus: 4, MemoryMb: 16384, CPUs: 4, Architecture: "X86_64"},
	"t2a-standard-1": {Description: "1 vCPU, 4 GB RAM", GuestCpus: 1, MemoryMb: 4096, CPUs: 1, Architecture: "ARM64"},
	"t2a-standard-2": {Description: "2 vCPUs, 8 GB RAM", GuestCpus: 2, MemoryMb: 8192, CPUs: 2, Architecture: "ARM64"},
	"t2a-standard-4": {Description: "4 vCPUs, 16 GB RAM", GuestCpus: 4, MemoryMb: 16384, CPUs: 4, Architecture: "ARM64"},
}

// gcpMachineTypeNames lists the catalog in name order.
func gcpMachineTypeNames() []string {
	names := make([]string, 0, len(gcpMachineTypes))
	for name := range gcpMachineTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// gcpLookupMachineType resolves a machine type reference however it is
// spelled — a bare name, a zonal path, a full URL — to its catalog entry.
func gcpLookupMachineType(ref string) (gcpMachineType, bool) {
	name := ref[strings.LastIndex(ref, "/")+1:]
	mt, ok := gcpMachineTypes[name]
	if ok {
		mt.Name = name
	}
	return mt, ok
}

// gcpMachineTypeJSON renders a catalog entry as compute#machineType in a zone.
func gcpMachineTypeJSON(project, zone string, mt gcpMachineType) map[string]any {
	out := map[string]any{
		"kind":                         "compute#machineType",
		"id":                           computeNumericID(),
		"creationTimestamp":            "1969-12-31T16:00:00.000-08:00",
		"name":                         mt.Name,
		"description":                  mt.Description,
		"guestCpus":                    mt.GuestCpus,
		"memoryMb":                     mt.MemoryMb,
		"imageSpaceGb":                 0,
		"maximumPersistentDisks":       128,
		"maximumPersistentDisksSizeGb": "263168",
		"zone":                         zone,
		"selfLink":                     computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, mt.Name)),
		"architecture":                 mt.Architecture,
	}
	if mt.SharedCpu {
		out["isSharedCpu"] = true
	}
	return out
}
