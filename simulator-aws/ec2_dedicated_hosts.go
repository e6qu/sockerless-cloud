package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ec2DedicatedHostFacts is one Dedicated Host family as the vendored Price
// List states it: the host's vCPUs and physical cores, and how many instances
// of each instance type the host holds when it runs that type alone. A family
// whose offer states no per-size capacity has an empty Capacity.
type ec2DedicatedHostFacts struct {
	VCpus    int
	Cores    int
	Capacity map[string]int
}

var ec2DedicatedHostCatalog = sync.OnceValue(func() map[string]ec2DedicatedHostFacts {
	var vendored struct {
		DedicatedHosts []struct {
			InstanceFamily   string            `json:"instanceFamily"`
			VCPU             string            `json:"vcpu"`
			PhysicalCores    string            `json:"physicalCores"`
			InstanceCapacity map[string]string `json:"instanceCapacity"`
		} `json:"dedicatedHosts"`
	}
	if err := json.Unmarshal(ec2InstanceTypesJSON, &vendored); err != nil {
		panic("vendored Amazon EC2 instance-type catalog is not valid JSON: " + err.Error())
	}
	catalog := make(map[string]ec2DedicatedHostFacts, len(vendored.DedicatedHosts))
	for _, h := range vendored.DedicatedHosts {
		vcpus, err := strconv.Atoi(h.VCPU)
		if err != nil {
			panic(fmt.Sprintf("vendored Dedicated Host family %s states vcpu %q", h.InstanceFamily, h.VCPU))
		}
		cores, err := strconv.Atoi(h.PhysicalCores)
		if err != nil {
			panic(fmt.Sprintf("vendored Dedicated Host family %s states physicalCores %q", h.InstanceFamily, h.PhysicalCores))
		}
		facts := ec2DedicatedHostFacts{VCpus: vcpus, Cores: cores, Capacity: map[string]int{}}
		for size, value := range h.InstanceCapacity {
			count, err := strconv.Atoi(value)
			if err != nil {
				panic(fmt.Sprintf("vendored Dedicated Host family %s states instanceCapacity-%s %q", h.InstanceFamily, size, value))
			}
			// The offer file capitalizes the sizes (`Large`, `Xlarge`,
			// `Metal`) that instance type names spell in lower case.
			facts.Capacity[h.InstanceFamily+"."+strings.ToLower(size)] = count
		}
		catalog[h.InstanceFamily] = facts
	}
	return catalog
})

// ec2HostFamily is the instance family a host was allocated for: the family
// it names, or the family of the one instance type it names.
func ec2HostFamily(h EC2Host) string {
	if h.InstanceFamily != "" {
		return h.InstanceFamily
	}
	family, _, _ := strings.Cut(h.InstanceType, ".")
	return family
}

// ec2HostInstances are the instances placed on the host that hold its
// capacity: every one not terminated or terminating.
func ec2HostInstances(hostID string) []EC2Instance {
	placed := ec2Instances.Filter(func(inst EC2Instance) bool {
		return inst.Placement.HostId == hostID && inst.State != "terminated" && inst.State != "shutting-down"
	})
	sort.Slice(placed, func(i, j int) bool { return placed[i].InstanceId < placed[j].InstanceId })
	return placed
}

// ec2HostTypeCapacity is one instance type's room on a host.
type ec2HostTypeCapacity struct {
	InstanceType string
	Available    int
	Total        int
}

// ec2HostCapacity is what is left on a host: its vCPUs less those of the
// instances placed on it, and for each instance type the host runs, the
// instances of that type still fitting, which is the Price List's count for
// the type bounded by the vCPUs left. A host allocated for one instance type
// runs that type only.
func ec2HostCapacity(h EC2Host, placed []EC2Instance) (availableVCpus int, types []ec2HostTypeCapacity) {
	facts, known := ec2DedicatedHostCatalog()[ec2HostFamily(h)]
	if !known {
		return 0, nil
	}
	_, instanceTypes := ec2InstanceTypeCatalog()
	availableVCpus = facts.VCpus
	used := map[string]int{}
	for _, inst := range placed {
		availableVCpus -= instanceTypes[inst.InstanceType].VCpus
		used[inst.InstanceType]++
	}
	names := make([]string, 0, len(facts.Capacity))
	for name := range facts.Capacity {
		if h.InstanceType == "" || name == h.InstanceType {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		total := facts.Capacity[name]
		available := total - used[name]
		if vcpus := instanceTypes[name].VCpus; vcpus > 0 {
			available = min(available, availableVCpus/vcpus)
		}
		types = append(types, ec2HostTypeCapacity{InstanceType: name, Available: max(available, 0), Total: total})
	}
	return availableVCpus, types
}

// ec2HostRefusal is the message refusing an instance of instanceType a place
// on the host, or "" when it fits: a host runs only its own family (only its
// one type, when allocated for one), and an instance needs its vCPUs free and,
// where the Price List states the type's count, a free slot of that type.
func ec2HostRefusal(h EC2Host, instanceType string) string {
	if h.State != "available" {
		return fmt.Sprintf("Host %s is %s and accepts no instances", h.HostId, h.State)
	}
	family, _, _ := strings.Cut(instanceType, ".")
	if family != ec2HostFamily(h) || (h.InstanceType != "" && instanceType != h.InstanceType) {
		return fmt.Sprintf("Host %s does not support instance type %s", h.HostId, instanceType)
	}
	facts, known := ec2DedicatedHostCatalog()[family]
	if !known {
		return ""
	}
	_, instanceTypes := ec2InstanceTypeCatalog()
	availableVCpus, types := ec2HostCapacity(h, ec2HostInstances(h.HostId))
	if instanceTypes[instanceType].VCpus > availableVCpus {
		return fmt.Sprintf("Host %s has %d vCPUs available, and %s needs %d", h.HostId, availableVCpus, instanceType, instanceTypes[instanceType].VCpus)
	}
	if _, stated := facts.Capacity[instanceType]; !stated {
		return ""
	}
	for _, t := range types {
		if t.InstanceType == instanceType && t.Available > 0 {
			return ""
		}
	}
	return fmt.Sprintf("Host %s has no capacity left for instance type %s", h.HostId, instanceType)
}

// ec2HostPlacementMu serializes the capacity check and the placement it
// admits, so two launches onto one host cannot both take its last slot.
var ec2HostPlacementMu sync.Mutex
