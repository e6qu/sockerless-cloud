package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
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
//   - phplogging reads the effective php.ini of the site's PHP worker. Both
//     halves are out of reach: the master values are the platform image's own
//     defaults, which are not vendored, and the local values come from a
//     .user.ini in the site's content, which is not modelled. An empty
//     settings resource would say the settings are unset rather than unknown,
//     which is why this declares rather than answering emptily — the reading
//     that let a pool's metric definitions answer with an empty collection
//     does not carry over to a resource of individual fields.
//     (The migrations that used to sit here are served: see
//     web_migrate_mysql.go.)
//   - a process dump is written from `/proc/<pid>` inside the container, which
//     the engine's HTTP API does not expose — the same limit that stops the
//     module reads beside it.

// registerWebPerfCounters mounts the counters, the PHP error-logging flag and
// the process dumps. All three read the site's running workload container:
// nothing here is a declared gap any more.
func registerWebPerfCounters(both, site func(string, string, http.HandlerFunc)) {
	both("GET", "/perfcounters", webListPerfMonCounters)

	// The PHP error-logging flag is served in web_php_logging.go, out of the
	// PHP the site is actually running: `php -i` reports every directive as
	// "name => local value => master value", which is the distinction this
	// resource makes.
	both("GET", "/phplogging", webGetSitePhpErrorLogFlag)

	// The process dump is served in web_process_dump.go: the site's processes
	// are the container's, the engine reports them in its host's PID namespace,
	// and where the simulator shares that kernel the dump is a real ELF core
	// written from the process's own memory.
	both("GET", "/processes/{processId}/dump", webGetProcessDump)
	both("GET", "/instances/{instanceId}/processes/{processId}/dump", webGetProcessDump)
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
