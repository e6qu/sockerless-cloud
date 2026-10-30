package main

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every Availability Zone the simulator serves places its instances on the one
// host whose CPUs and memory ECS placement measures, so a score reads what
// that host has left once the running instances and placed tasks hold theirs.
func ec2HostFreeCapacity() (vcpus, memoryMiB int) {
	return ec2HostFreeCapacityExcept("")
}

// ec2HostCapacityMu serialises a launch's capacity check with the write that
// commits the instance, so two launches cannot both take the last memory.
var ec2HostCapacityMu sync.Mutex

const ec2InsufficientCapacity = "There is not enough capacity to fulfill your request."

// ec2HostHoldsInstanceType reports whether the host's free memory holds one
// more instance of the type. Only memory bounds a launch: like a hypervisor,
// the host overcommits vCPUs. Caller holds ec2HostCapacityMu.
func ec2HostHoldsInstanceType(instanceType, except string) bool {
	facts, ok := ec2LookupInstanceType(instanceType)
	if !ok {
		return true
	}
	_, freeMemory := ec2HostFreeCapacityExcept(except)
	return facts.MemoryMiB <= freeMemory
}

func ec2HostFreeCapacityExcept(except string) (vcpus, memoryMiB int) {
	ceiling := ecsPlacementCeiling()
	ecsPlacementMu.Lock()
	tasks := ecsCommittedTotal()
	ecsPlacementMu.Unlock()
	vcpus = ceiling.CPUUnits/1024 - tasks.CPUUnits/1024
	memoryMiB = ceiling.MemoryMiB - tasks.MemoryMiB
	_, types := ec2InstanceTypeCatalog()
	for _, inst := range ec2Instances.Filter(func(inst EC2Instance) bool {
		return inst.InstanceId != except && (inst.State == "pending" || inst.State == "running" || inst.State == "stopping")
	}) {
		vcpus -= types[inst.InstanceType].VCpus
		memoryMiB -= types[inst.InstanceType].MemoryMiB
	}
	return max(vcpus, 0), max(memoryMiB, 0)
}

// The catalog's instance types that satisfy the vCPU, memory and architecture
// bounds under prefix ("InstanceRequirements" or a request's nested member).
func ec2InstanceTypesMatching(r *http.Request, prefix, archPrefix string) []string {
	minVcpus := ec2AtoiOr(r.FormValue(prefix+".VCpuCount.Min"), 0)
	maxVcpus := ec2AtoiOr(r.FormValue(prefix+".VCpuCount.Max"), 0)
	minMem := ec2AtoiOr(r.FormValue(prefix+".MemoryMiB.Min"), 0)
	maxMem := ec2AtoiOr(r.FormValue(prefix+".MemoryMiB.Max"), 0)
	arches := ec2ParamList(r, archPrefix)

	var matches []string
	catalog, _ := ec2InstanceTypeCatalog()
	for _, t := range catalog {
		if minVcpus > 0 && t.VCpus < minVcpus {
			continue
		}
		if maxVcpus > 0 && t.VCpus > maxVcpus {
			continue
		}
		if minMem > 0 && t.MemoryMiB < minMem {
			continue
		}
		if maxMem > 0 && t.MemoryMiB > maxMem {
			continue
		}
		if len(arches) > 0 && !slices.ContainsFunc(t.Architectures, func(a string) bool { return ec2StrInValues(a, arches) }) {
			continue
		}
		matches = append(matches, t.Name)
	}
	sort.Strings(matches)
	return matches
}

// high: several times the amount; medium: the amount with little to spare;
// low: less than the amount. AWS documents the bands, not a multiple, so
// "several" reads as three.
func ec2SupportabilityScore(held, amount int) string {
	switch {
	case held >= 3*amount:
		return "high"
	case held >= amount:
		return "medium"
	default:
		return "low"
	}
}

func handleGetCapacityManagerSupportabilityScores(w http.ResponseWriter, r *http.Request) {
	for _, required := range []string{"CapacityAmount", "CapacityUnit", "MarketType", "CapacityAvailabilityZoneId.1"} {
		if r.FormValue(required) == "" {
			ec2ErrorXML(w, "MissingParameter", fmt.Sprintf("The request must contain the parameter %s", strings.TrimSuffix(required, ".1")), http.StatusBadRequest)
			return
		}
	}
	amount, err := strconv.Atoi(r.FormValue("CapacityAmount"))
	if err != nil || amount < 1 {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter CapacityAmount is invalid. It must be a positive integer.", r.FormValue("CapacityAmount")), http.StatusBadRequest)
		return
	}
	unit := r.FormValue("CapacityUnit")
	if unit != "vcpus" && unit != "instances" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter CapacityUnit is invalid. Valid values: vcpus, instances.", unit), http.StatusBadRequest)
		return
	}
	if market := r.FormValue("MarketType"); market != "on-demand" && market != "spot" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter MarketType is invalid. Valid values: on-demand, spot.", market), http.StatusBadRequest)
		return
	}
	zones := ec2ParamList(r, "CapacityAvailabilityZoneId")
	if len(zones) > 10 {
		ec2ErrorXML(w, "InvalidParameterValue", "You can specify up to 10 Availability Zones.", http.StatusBadRequest)
		return
	}
	for _, id := range zones {
		if !slices.ContainsFunc(awsAvailabilityZones(), func(zone string) bool { return ec2AvailabilityZoneId(zone) == id }) {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter CapacityAvailabilityZoneId is invalid.", id), http.StatusBadRequest)
			return
		}
	}

	types := ec2ParamList(r, "InstanceType")
	const requirements = "InstanceRequirementsWithMetadata"
	byRequirements := r.FormValue(requirements+".InstanceRequirements.VCpuCount.Min") != "" ||
		r.FormValue(requirements+".InstanceRequirements.MemoryMiB.Min") != ""
	switch {
	case len(types) > 0 && byRequirements:
		ec2ErrorXML(w, "InvalidParameterCombination", "Specify either InstanceTypes or InstanceRequirementsWithMetadata, but not both.", http.StatusBadRequest)
		return
	case len(types) == 0 && !byRequirements:
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceTypes or InstanceRequirementsWithMetadata", http.StatusBadRequest)
		return
	case len(types) > 50:
		ec2ErrorXML(w, "InvalidParameterValue", "You can specify up to 50 instance types.", http.StatusBadRequest)
		return
	case byRequirements:
		types = ec2InstanceTypesMatching(r, requirements+".InstanceRequirements", requirements+".ArchitectureType")
	}
	for _, name := range types {
		if _, ok := ec2LookupInstanceType(name); !ok {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter InstanceType is invalid.", name), http.StatusBadRequest)
			return
		}
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}

	freeVcpus, freeMemory := ec2HostFreeCapacity()
	var items strings.Builder
	best := 0
	for _, name := range types {
		facts, _ := ec2LookupInstanceType(name)
		held := 0
		if facts.VCpus > 0 && facts.MemoryMiB > 0 {
			held = min(freeVcpus/facts.VCpus, freeMemory/facts.MemoryMiB)
		}
		if unit == "vcpus" {
			held *= facts.VCpus
		}
		// The zones share one host, so together they hold what one does.
		best = max(best, held)
		score := ec2SupportabilityScore(held, amount)
		for _, zone := range zones {
			fmt.Fprintf(&items, "<item><availabilityZoneId>%s</availabilityZoneId><instanceType>%s</instanceType><score>%s</score></item>", zone, name, score)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetCapacityManagerSupportabilityScoresResponse %s><requestId>%s</requestId><capacityManagerSupportabilityScoreSet>%s</capacityManagerSupportabilityScoreSet><aggregateScore>%s</aggregateScore></GetCapacityManagerSupportabilityScoresResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String(), ec2SupportabilityScore(best, amount))
}

// handleGetSpotPlacementScores scores 1 to 10 how likely the target capacity
// is to be fulfilled, from what the host holds: 10 at three times the target,
// as "several times" reads for the supportability scores, linear below it,
// never under 1. A Region the simulator does not serve holds nothing of its.
func handleGetSpotPlacementScores(w http.ResponseWriter, r *http.Request) {
	target, err := strconv.Atoi(r.FormValue("TargetCapacity"))
	if r.FormValue("TargetCapacity") == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter TargetCapacity", http.StatusBadRequest)
		return
	}
	if err != nil || target < 1 {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter TargetCapacity is invalid. It must be a positive integer.", r.FormValue("TargetCapacity")), http.StatusBadRequest)
		return
	}
	unit := r.FormValue("TargetCapacityUnitType")
	if unit == "" {
		unit = "units"
	}
	if unit != "units" && unit != "vcpu" && unit != "memory-mib" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter TargetCapacityUnitType is invalid. Valid values: units, vcpu, memory-mib.", unit), http.StatusBadRequest)
		return
	}
	types := ec2ParamList(r, "InstanceType")
	const requirements = "InstanceRequirementsWithMetadata"
	byRequirements := r.FormValue(requirements+".InstanceRequirements.VCpuCount.Min") != "" ||
		r.FormValue(requirements+".InstanceRequirements.MemoryMiB.Min") != ""
	switch {
	case len(types) > 0 && byRequirements:
		ec2ErrorXML(w, "InvalidParameterCombination", "Specify either InstanceTypes or InstanceRequirementsWithMetadata, but not both.", http.StatusBadRequest)
		return
	case len(types) == 0 && !byRequirements:
		// The model requires neither; a request naming no type is scored over
		// every type the catalog holds.
		catalog, _ := ec2InstanceTypeCatalog()
		for _, facts := range catalog {
			types = append(types, facts.Name)
		}
	case byRequirements:
		types = ec2InstanceTypesMatching(r, requirements+".InstanceRequirements", requirements+".ArchitectureType")
	}
	for _, name := range types {
		if _, ok := ec2LookupInstanceType(name); !ok {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter InstanceType is invalid.", name), http.StatusBadRequest)
			return
		}
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}

	freeVcpus, freeMemory := ec2HostFreeCapacity()
	held := 0
	for _, name := range types {
		facts, _ := ec2LookupInstanceType(name)
		count := 0
		if facts.VCpus > 0 && facts.MemoryMiB > 0 {
			count = min(freeVcpus/facts.VCpus, freeMemory/facts.MemoryMiB)
		}
		switch unit {
		case "vcpu":
			count *= facts.VCpus
		case "memory-mib":
			count *= facts.MemoryMiB
		}
		held = max(held, count)
	}
	score := ec2SpotPlacementScore(held, target)

	regions := ec2ParamList(r, "RegionName")
	if len(regions) == 0 {
		regions = []string{awsRegion()}
	}
	single := r.FormValue("SingleAvailabilityZone") == "true"
	var items strings.Builder
	for _, region := range regions {
		regionScore := 1
		if region == awsRegion() {
			regionScore = score
		}
		if !single {
			fmt.Fprintf(&items, "<item><region>%s</region><score>%d</score></item>", region, regionScore)
			continue
		}
		if region != awsRegion() {
			continue
		}
		for _, zone := range awsAvailabilityZones() {
			fmt.Fprintf(&items, "<item><region>%s</region><availabilityZoneId>%s</availabilityZoneId><score>%d</score></item>", region, ec2AvailabilityZoneId(zone), regionScore)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSpotPlacementScoresResponse %s><requestId>%s</requestId><spotPlacementScoreSet>%s</spotPlacementScoreSet></GetSpotPlacementScoresResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String())
}

func ec2SpotPlacementScore(held, target int) int {
	score := int(math.Round(10 * float64(held) / float64(3*target)))
	return max(1, min(10, score))
}
