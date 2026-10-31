package main

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Cloud Run's regional CPU quota (cloudrun-quotas.md.gz): the CPU, in milli
// vCPU, allocated across every instance a project runs in a region, against
// the value Cloud Quotas holds in effect for run.googleapis.com's
// CpuAllocPerProjectRegion. An instance holds its containers' CPU limits from
// the moment it starts until it stops; a deploy needs room for the instance
// its revision starts first.

// regionalCPUQuotaErrorMessage is the message a deploy gets once the region's
// CPU quota has no room for its revision, verbatim, since clients match on it.
const regionalCPUQuotaErrorMessage = "Container Healthcheck failed. Quota exceeded for total allowable CPU per project per region."

// errCloudRunCPUQuota is what an instance start gets when the region's CPU
// quota has no room for it.
var errCloudRunCPUQuota = errors.New("quota exceeded for total allowable CPU per project per region")

// regionalCPUQuotaErrorJSON writes the 400 INVALID_ARGUMENT Cloud Functions
// and Cloud Run answer when the CPU quota has no room for the deploy.
func regionalCPUQuotaErrorJSON(w http.ResponseWriter, resourceName string) {
	msg := regionalCPUQuotaErrorMessage
	if resourceName != "" {
		msg = "Could not create or update Cloud Run service " + resourceName + ", " + msg
	}
	GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
}

var cloudRunCPUAllocations = struct {
	sync.Mutex
	// byKey is the milli vCPU allocated, keyed by projects/{number}|region.
	byKey map[string]int64
}{byKey: map[string]int64{}}

func cloudRunCPUKey(project, region string) string {
	return cloudQuotaProjectName(project) + "|" + region
}

// cloudRunCPUHold is the CPU one instance holds; release gives it back once.
type cloudRunCPUHold struct {
	once  sync.Once
	key   string
	milli int64
}

func (h *cloudRunCPUHold) release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		cloudRunCPUAllocations.Lock()
		cloudRunCPUAllocations.byKey[h.key] -= h.milli
		if cloudRunCPUAllocations.byKey[h.key] <= 0 {
			delete(cloudRunCPUAllocations.byKey, h.key)
		}
		cloudRunCPUAllocations.Unlock()
	})
}

// cloudRunHoldCPU allocates the CPU of an instance of containers starting in
// project's region, or fails with errCloudRunCPUQuota when the allocation
// would pass the quota in effect.
func cloudRunHoldCPU(project, region string, containers []Container) (*cloudRunCPUHold, error) {
	milli := cloudRunInstanceMilliCPU(containers)
	limit := cloudQuotaLimit(cloudRunCPUQuota(), project, region)
	key := cloudRunCPUKey(project, region)
	cloudRunCPUAllocations.Lock()
	defer cloudRunCPUAllocations.Unlock()
	if limit != -1 && cloudRunCPUAllocations.byKey[key]+milli > limit {
		return nil, errCloudRunCPUQuota
	}
	cloudRunCPUAllocations.byKey[key] += milli
	return &cloudRunCPUHold{key: key, milli: milli}, nil
}

// cloudRunAdoptCPU allocates the CPU of an instance an earlier simulator
// process started, which runs whatever the quota now allows.
func cloudRunAdoptCPU(project, region string, containers []Container) *cloudRunCPUHold {
	milli := cloudRunInstanceMilliCPU(containers)
	key := cloudRunCPUKey(project, region)
	cloudRunCPUAllocations.Lock()
	cloudRunCPUAllocations.byKey[key] += milli
	cloudRunCPUAllocations.Unlock()
	return &cloudRunCPUHold{key: key, milli: milli}
}

// cloudRunAllocatedCPU is the milli vCPU a project's instances hold in a
// region; project is an ID, a number or projects/{number}.
func cloudRunAllocatedCPU(project, region string) int64 {
	key := cloudRunCPUKey(strings.TrimPrefix(project, "projects/"), region)
	cloudRunCPUAllocations.Lock()
	defer cloudRunCPUAllocations.Unlock()
	return cloudRunCPUAllocations.byKey[key]
}

// cloudRunAllocatedRegions are the regions a project's instances hold CPU in.
func cloudRunAllocatedRegions(project string) []string {
	prefix := cloudQuotaProjectName(strings.TrimPrefix(project, "projects/")) + "|"
	cloudRunCPUAllocations.Lock()
	defer cloudRunCPUAllocations.Unlock()
	var regions []string
	for key := range cloudRunCPUAllocations.byKey {
		if region, ok := strings.CutPrefix(key, prefix); ok {
			regions = append(regions, region)
		}
	}
	sort.Strings(regions)
	return regions
}

// cloudRunDeployFits reports whether the region's CPU quota has room for the
// first instance of the revision svc's template deploys.
func cloudRunDeployFits(project, region string, svc ServiceV2) bool {
	var containers []Container
	if svc.Template != nil {
		containers = svc.Template.Containers
	}
	limit := cloudQuotaLimit(cloudRunCPUQuota(), project, region)
	return limit == -1 || cloudRunAllocatedCPU(project, region)+cloudRunInstanceMilliCPU(containers) <= limit
}

func cloudRunCPUQuota() *cloudQuotaDefinition {
	return cloudQuotaLookup(cloudRunQuotaService, cloudRunCPUQuotaID)
}

// containerCPULoad converts a container CPU limit ("1", "0.5", "1000m") into
// vCPU; an empty limit is 1, the Cloud Run default.
func containerCPULoad(cpuLimit string) float64 {
	cpuLimit = strings.TrimSpace(cpuLimit)
	if cpuLimit == "" {
		return 1.0
	}
	if strings.HasSuffix(cpuLimit, "m") {
		if n, err := strconv.ParseFloat(strings.TrimSuffix(cpuLimit, "m"), 64); err == nil {
			return n / 1000.0
		}
	}
	if n, err := strconv.ParseFloat(cpuLimit, 64); err == nil {
		return n
	}
	return 1.0
}

// cloudRunInstanceMilliCPU sums the CPU limits of an instance's containers,
// which Cloud Run schedules together, in milli vCPU.
func cloudRunInstanceMilliCPU(containers []Container) int64 {
	var sum float64
	for _, c := range containers {
		var cpu string
		if c.Resources != nil {
			cpu = c.Resources.Limits["cpu"]
		}
		sum += containerCPULoad(cpu)
	}
	if sum == 0 {
		sum = 1
	}
	return int64(math.Round(sum * 1000))
}
