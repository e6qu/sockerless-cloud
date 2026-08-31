package main

import (
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// A site's performance counters, and the four platform reads beside them that
// the simulator has no primitive for.
//
// WebApps_ListPerfMonCounters reports what the site is using, and the site's
// workload container is what is using it — so the counters are the container
// engine's own resource-usage sample, read the same way the instance and
// diagnostics reads read it. A site with no running container is using
// nothing, and reports nothing, rather than reporting figures nothing produced.
//
// The other four families answer a declared 501 naming what is missing:
//
//   - phplogging reads the effective php.ini of the site's PHP worker, and the
//     master values are the platform image's own defaults. No PHP worker runs
//     here and Microsoft's platform php.ini is not vendored.
//     (The migrations that used to sit here are served: see
//     web_migrate_mysql.go.)
//   - a process dump is written from `/proc/<pid>` inside the container, which
//     the engine's HTTP API does not expose — the same limit that stops the
//     module reads beside it.

// registerWebPerfCounters mounts the counters and the four declared gaps.
func registerWebPerfCounters(both, site func(string, string, http.HandlerFunc)) {
	both("GET", "/perfcounters", webListPerfMonCounters)

	gap := func(operation, reason string) http.HandlerFunc {
		// The gap does not depend on the site existing: the operation is
		// unimplemented whatever it is asked about, and answering a 404 for an
		// absent site first would report it as one the simulator serves.
		return func(w http.ResponseWriter, _ *http.Request) {
			sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"%s is not implemented by the simulator: %s.", operation, reason)
		}
	}

	const phpReason = "the flag reports the effective php.ini of the site's PHP worker, and its master values are the App Service platform image's own defaults — no PHP worker runs here and Microsoft's platform php.ini is not vendored"
	both("GET", "/phplogging", gap("WebApps_GetSitePhpErrorLogFlag", phpReason))

	const dumpReason = "a process dump is written from /proc/<pid> inside the container, which the container engine's HTTP API does not expose — the same limit that stops the process module reads"
	both("GET", "/processes/{processId}/dump", gap("WebApps_GetProcessDump", dumpReason))
	both("GET", "/instances/{instanceId}/processes/{processId}/dump",
		gap("WebApps_GetInstanceProcessDump", dumpReason))
}

// webListPerfMonCounters reports the site's resource usage as the counter sets
// Azure names them. Each set carries the one sample the engine just produced,
// which is the window the simulator can speak for: it keeps no counter history,
// so a set spanning an hour would be reporting time it did not measure.
func webListPerfMonCounters(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	site, _ := webResource(r)
	containerID, running := webSiteInstanceContainer(site)
	if !running {
		// A site with nothing running is using nothing. An empty collection is
		// the true reading; a zeroed set would claim a measurement was taken.
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
		return
	}
	stats, err := webContainerStats(containerID)
	if err != nil {
		webEngineFailure(w, err)
		return
	}

	sampledAt := time.Now().UTC()
	// The sample covers the interval the engine measured it over, which is the
	// gap between the two readings it takes.
	startedAt := sampledAt.Add(-time.Second)
	instanceName := containerID

	// One counter is one response carrying one set, which is how the document
	// declares it: a PerfMonResponse's data member is a single PerfMonSet, not
	// a list of them.
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

	// The counters Azure reports for a site, each taken from the reading the
	// engine produced for the container behind it.
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
