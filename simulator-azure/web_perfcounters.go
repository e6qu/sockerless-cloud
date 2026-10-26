package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// WebApps_ListPerfMonCounters reports the site's workload container's
// resource usage, read as the instance and diagnostics reads read it. A site
// with no running container reports nothing.

// All three read the site's running workload container.
func registerWebPerfCounters(both, site func(string, string, http.HandlerFunc)) {
	both("GET", "/perfcounters", webListPerfMonCounters)

	// Served in web_php_logging.go from the site's own PHP.
	both("GET", "/phplogging", webGetSitePhpErrorLogFlag)

	// Served in web_process_dump.go as a real ELF core where the simulator shares
	// the engine's kernel.
	both("GET", "/processes/{processId}/dump", webGetProcessDump)
	both("GET", "/instances/{instanceId}/processes/{processId}/dump", webGetProcessDump)
}

// Each set carries the one sample the engine just produced: the simulator
// keeps no counter history, so a longer span would claim unmeasured time.
func webListPerfMonCounters(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	site, _ := webResource(r)
	instance, running := webSiteInstanceContainer(site)
	if !running {
		// An empty collection, not a zeroed set that would claim a measurement.
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
		return
	}
	containerID := instance.ID()
	stats, err := webContainerStats(containerID)
	if err != nil {
		webEngineFailure(w, err)
		return
	}

	sampledAt := time.Now().UTC()
	// The engine measures over the gap between its two readings.
	startedAt := sampledAt.Add(-time.Second)
	instanceName := webInstanceName(containerID)

	// A PerfMonResponse's data is a single PerfMonSet, not a list.
	counter := func(name string, value uint64) map[string]any {
		return map[string]any{
			"code":    "Success",
			"message": "The counter is the container engine's reading for this site's workload.",
			"data": map[string]any{
				"name":      name,
				"startTime": startedAt.Format(time.RFC3339),
				"endTime":   sampledAt.Format(time.RFC3339),
				"timeGrain": "PT1S",
				"values": []any{map[string]any{
					"time":         sampledAt.Format(time.RFC3339),
					"instanceName": instanceName,
					"value":        value,
				}},
			},
		}
	}

	// Each counter is the engine's reading for the container.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []any{
			counter("cpuUsage", stats.CPUStats.CPUUsage.TotalUsage),
			counter("cpuKernelUsage", stats.CPUStats.CPUUsage.UsageInKernelmode),
			counter("cpuUserUsage", stats.CPUStats.CPUUsage.UsageInUsermode),
			counter("cpuThrottledTime", stats.CPUStats.ThrottlingData.ThrottledTime),
			counter("memoryWorkingSet", stats.MemoryStats.Usage),
			counter("memoryPeakWorkingSet", stats.MemoryStats.MaxUsage),
			counter("memoryLimit", stats.MemoryStats.Limit),
		},
	})
}
