package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// App Service diagnostics: the diagnostic categories of a site or slot, the
// detectors and analyses in them, and the detector responses the site
// publishes at its own /detectors collection.
//
// A detector is a measurement, so the simulator publishes exactly the
// detectors it can measure, computed at the moment the client asks:
//
//   - sitecrashes reads the workload container's terminal state from the
//     container engine — whether it exited, with which code, and whether the
//     kernel killed it for exceeding its memory limit;
//   - sitememoryanalysis reads the engine's memory sample for that container
//     (usage, peak, limit) and reports an issue when the kernel OOM-killed it,
//     which is an event the engine records rather than a threshold this
//     simulator invented;
//   - sitecpuanalysis reads the engine's CPU sample and its cgroup throttling
//     counters, and reports an issue when the container's own CPU quota
//     throttled it — again a counter the kernel keeps;
//   - threadcount sums the thread counts of the container's process table,
//     read through the engine's process-table API;
//   - siterestartuserinitiated reports the restarts the site actually went
//     through, from the journal the lifecycle operations write;
//   - deployment reports the site's real deployment records.
//
// Every value in a response is one of those measurements or a member of the
// site's own record. A detector whose inputs the simulator does not hold is
// NOT listed as if it existed and does not answer with plausible numbers:
// asking for it by name returns a declared 501 gap naming the input that is
// missing. Those detectors, all of them names Microsoft's own
// Diagnostics_ListSiteDetectors example carries, and the input each would
// need:
//
//	servicehealth               Azure Service Health incident records for the
//	                            region hosting the site. The simulator
//	                            publishes no service-health events at all.
//	siteswap                    the slot-swap history. The simulator's swap
//	                            operation exchanges nothing and keeps no
//	                            record of having run.
//	workeravailability          the health of the worker instances of the App
//	                            Service plan. There is no worker fleet here:
//	                            the whole execution model is one workload
//	                            container per site.
//	sitelatency                 per-request timing. Requests reach the site's
//	                            container without a request log being kept.
//	failedrequestsperuri        per-request status codes and URIs, from the
//	                            same request log that does not exist.
//	frebanalysis                IIS Failed Request Tracing logs, which only a
//	                            Windows worker's IIS produces; the workload is
//	                            a Linux container.
//	loganalyzer                 the PHP error log of a PHP site, which the
//	                            simulator neither collects nor parses.
//	aspnetcore                  the ASP.NET Core stdout log and its
//	                            startup-failure records, likewise uncollected.
//	committedmemoryusage        the Windows committed-memory counter.
//	pagefileoperations          the Windows page-file counter. Neither exists
//	                            on a Linux container host.
//	autoheal                    the auto-heal rule engine's action history.
//	                            The simulator evaluates no auto-heal rules.
//	siterestartsettingupdate    restarts the platform performs when a site's
//	                            configuration changes. The simulator does not
//	                            restart a site on a configuration change, so
//	                            there are none to report — and reporting the
//	                            user-initiated ones under this name would
//	                            attribute them to the wrong cause.
//
// The same rule governs the analyses: apprestartanalysis and memoryanalysis
// are computed from the journal and the memory sample, while appanalysis,
// perfanalysis and tcpconnectionsanalysis need the request, performance and
// TCP-connection telemetry the simulator does not collect.

// ---------------------------------------------------------------------------
// the site event journal
// ---------------------------------------------------------------------------

// WebSiteEvent is one lifecycle event a site went through. The journal is what
// the restart detectors read: it records the operation that ran, when it ran,
// and what caused it.
type WebSiteEvent struct {
	ID     string `json:"id"`
	SiteID string `json:"siteId"`
	// Operation is the App Service operation that ran ("Stop", "Start",
	// "Restart").
	Operation string `json:"operation"`
	// Cause distinguishes an operation a user called on the site from one the
	// platform performed on it, which is the distinction the restart detectors
	// are split along.
	Cause string `json:"cause"`
	Time  string `json:"time"`
}

var webSiteEvents sim.Store[WebSiteEvent]

const (
	webEventCauseUser     = "UserInitiated"
	webEventCausePlatform = "PlatformInitiated"
)

// recordWebSiteEvent appends one lifecycle event to a site's journal.
func recordWebSiteEvent(siteID, operation, cause string) {
	if webSiteEvents == nil || siteID == "" {
		return
	}
	at := time.Now().UTC()
	id := fmt.Sprintf("%s/events/%s-%s", siteID, at.Format("20060102T150405.000000000"), operation)
	webSiteEvents.Put(id, WebSiteEvent{
		ID:        id,
		SiteID:    siteID,
		Operation: operation,
		Cause:     cause,
		Time:      at.Format(time.RFC3339Nano),
	})
}

// webSiteEventsFor returns a site's journal inside a window, oldest first.
func webSiteEventsFor(siteID string, from, to time.Time) []WebSiteEvent {
	if webSiteEvents == nil {
		return nil
	}
	events := webSiteEvents.Filter(func(e WebSiteEvent) bool {
		if !strings.EqualFold(e.SiteID, siteID) {
			return false
		}
		at, err := time.Parse(time.RFC3339Nano, e.Time)
		if err != nil {
			return false
		}
		return !at.Before(from) && !at.After(to)
	})
	sort.Slice(events, func(i, j int) bool { return events[i].Time < events[j].Time })
	return events
}

// ---------------------------------------------------------------------------
// observation of the site
// ---------------------------------------------------------------------------

// webSiteObservation is everything a detector run measured about one site. The
// container reads happen once per run and are shared by every detector in it.
type webSiteObservation struct {
	site       Site
	resourceID string
	readAt     time.Time
	from, to   time.Time

	containerID string
	inspect     *mobycontainer.InspectResponse
	stats       *mobycontainer.StatsResponse
	processes   []webProcess
	events      []WebSiteEvent
}

// observeWebSite gathers the measurements the detectors read. A site with no
// workload container is observed too: "there is no container" is itself the
// answer several detectors give.
func observeWebSite(site Site, resourceID string, from, to time.Time) webSiteObservation {
	obs := webSiteObservation{
		site:       site,
		resourceID: resourceID,
		readAt:     time.Now().UTC(),
		from:       from,
		to:         to,
		events:     webSiteEventsFor(resourceID, from, to),
	}
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	obs.containerID = inst.containerID
	inst.mu.Unlock()
	if obs.containerID == "" {
		return obs
	}
	cli := sim.DockerClient()
	if cli == nil {
		return obs
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if res, err := cli.ContainerInspect(ctx, obs.containerID, mobyclient.ContainerInspectOptions{}); err == nil {
		inspect := res.Container
		obs.inspect = &inspect
	}
	if obs.inspect != nil && obs.inspect.State != nil && obs.inspect.State.Running {
		if stats, err := webContainerStats(obs.containerID); err == nil {
			obs.stats = &stats
		}
		if procs, err := webProcessTable(obs.containerID); err == nil {
			obs.processes = procs
		}
	}
	return obs
}

func (o webSiteObservation) running() bool {
	return o.inspect != nil && o.inspect.State != nil && o.inspect.State.Running
}

// ---------------------------------------------------------------------------
// detector catalog
// ---------------------------------------------------------------------------

// webDetectorFinding is what one detector measured: the table it renders, the
// severity it reports, and the events it correlated.
type webDetectorFinding struct {
	issueDetected bool
	// status is one of the swagger's InsightStatus values.
	status  string
	message string
	// columns and rows are the measurement, in the DataTableResponseObject
	// shape both response families render.
	tableName string
	columns   []webDetectorColumn
	rows      [][]string
	// abnormal are the correlated events the detector found, each an
	// occurrence the simulator observed rather than an inferred window.
	abnormal []webDetectorEvent
	// metrics are the measured series the detector sampled.
	metrics []map[string]any
}

type webDetectorColumn struct{ name, dataType, columnType string }

type webDetectorEvent struct {
	start, end time.Time
	message    string
	issueType  string
}

// webDetector is one detector the simulator computes, named and ranked the way
// Microsoft's own detector catalog names and ranks it.
type webDetector struct {
	name        string
	displayName string
	rank        float64
	// description is what this simulator measures for the detector. Azure
	// leaves the member null in its own listing; saying what was measured is
	// more use than repeating that.
	description string
	measure     func(webSiteObservation) webDetectorFinding
}

// webUnservedDetector is a detector Microsoft's catalog carries that the
// simulator cannot measure, with the input it would need.
type webUnservedDetector struct {
	name   string
	reason string
}

const webDiagnosticCategory = "availability"

// webDiagnosticCategoryDescription is the description Azure gives the
// availability category, spelling included.
const webDiagnosticCategoryDescription = "Availability and Perfomance Diagnostics"

func webDetectors() []webDetector {
	return []webDetector{
		{
			name:        "sitecrashes",
			displayName: "Site Crash Events",
			rank:        9,
			description: "Reports the workload container's terminal state as the container engine records it.",
			measure:     measureSiteCrashes,
		},
		{
			name:        "sitecpuanalysis",
			displayName: "CPU Analysis",
			rank:        3,
			description: "Samples the workload container's CPU usage and its cgroup throttling counters.",
			measure:     measureSiteCPU,
		},
		{
			name:        "sitememoryanalysis",
			displayName: "Physical Memory Analysis",
			rank:        3,
			description: "Samples the workload container's memory usage against the limit it runs under.",
			measure:     measureSiteMemory,
		},
		{
			name:        "threadcount",
			displayName: "Thread Count",
			rank:        23,
			description: "Sums the threads of every process in the workload container's process table.",
			measure:     measureSiteThreads,
		},
		{
			name:        "siterestartuserinitiated",
			displayName: "User Initiated Site Restarts",
			rank:        14,
			description: "Reports the restarts a user called on the site.",
			measure:     measureSiteRestarts,
		},
		{
			name:        "deployment",
			displayName: "Site Deployments",
			rank:        7,
			description: "Reports the site's deployment records.",
			measure:     measureSiteDeployments,
		},
	}
}

// webUnservedDetectors are the detectors of Microsoft's catalog the simulator
// declares it cannot measure, each with the input it would need. Naming them
// is the point: a client that asks for one is told what is missing instead of
// receiving an empty answer it would read as "nothing wrong".
func webUnservedDetectors() []webUnservedDetector {
	return []webUnservedDetector{
		{"servicehealth", "an Azure Service Health incident record for the region hosting the site; the simulator publishes no service-health events"},
		{"siteswap", "the slot-swap history; the simulator's slot swap keeps no record of having run"},
		{"workeravailability", "the health of the App Service plan's worker instances; the simulator runs one workload container per site and no worker fleet"},
		{"sitelatency", "per-request timing; requests reach the site's container without a request log being kept"},
		{"failedrequestsperuri", "per-request status codes and URIs, from a request log the simulator does not keep"},
		{"frebanalysis", "IIS Failed Request Tracing logs, which only a Windows worker produces; the workload is a Linux container"},
		{"loganalyzer", "the PHP error log of a PHP site, which the simulator neither collects nor parses"},
		{"aspnetcore", "the ASP.NET Core stdout and startup-failure logs, which the simulator does not collect"},
		{"committedmemoryusage", "the Windows committed-memory counter, which a Linux container host does not have"},
		{"pagefileoperations", "the Windows page-file counter, which a Linux container host does not have"},
		{"autoheal", "the auto-heal rule engine's action history; the simulator evaluates no auto-heal rules"},
		{"siterestartsettingupdate", "restarts the platform performs on a configuration change; the simulator performs none, and reporting the user-initiated restarts here would attribute them to the wrong cause"},
	}
}

// webAnalyses are the analyses the simulator computes, each aggregating the
// detectors named in it.
type webAnalysis struct {
	name        string
	description string
	detectors   []string
}

func webAnalyses() []webAnalysis {
	return []webAnalysis{
		{
			name:        "apprestartanalysis",
			description: "Find the reasons that your app restarted",
			detectors:   []string{"siterestartuserinitiated", "sitecrashes"},
		},
		{
			name:        "memoryanalysis",
			description: "Detect issues with memory as well as suggest ways to troubleshoot memory problems",
			detectors:   []string{"sitememoryanalysis"},
		},
	}
}

func webUnservedAnalyses() []webUnservedDetector {
	return []webUnservedDetector{
		{"appanalysis", "per-request availability telemetry; the simulator keeps no request log for a site"},
		{"perfanalysis", "per-request latency and throughput telemetry, from the same request log"},
		{"tcpconnectionsanalysis", "the worker's TCP connection and port-exhaustion counters, which the simulator does not collect"},
	}
}

// ---------------------------------------------------------------------------
// measurements
// ---------------------------------------------------------------------------

func webNoContainerFinding(what string) webDetectorFinding {
	return webDetectorFinding{
		status:  "None",
		message: "The site has no workload container, so " + what + " could not be measured.",
	}
}

func measureSiteCrashes(o webSiteObservation) webDetectorFinding {
	if o.inspect == nil || o.inspect.State == nil {
		return webNoContainerFinding("its terminal state")
	}
	state := o.inspect.State
	finding := webDetectorFinding{
		tableName: "SiteCrashEvents",
		columns: []webDetectorColumn{
			{"Timestamp", "DateTime", "datetime"},
			{"ContainerId", "String", "string"},
			{"State", "String", "string"},
			{"ExitCode", "Int32", "int"},
			{"OOMKilled", "Boolean", "bool"},
			{"Error", "String", "string"},
		},
	}
	crashed := !state.Running && (state.ExitCode != 0 || state.OOMKilled || state.Dead)
	at := webParseDockerTime(state.FinishedAt)
	if at.IsZero() {
		at = o.readAt
	}
	if crashed {
		finding.issueDetected = true
		finding.status = "Critical"
		finding.message = fmt.Sprintf("The site's container exited with code %d.", state.ExitCode)
		if state.OOMKilled {
			finding.message = "The site's container was killed for exceeding its memory limit."
		}
		finding.rows = append(finding.rows, []string{
			at.Format(time.RFC3339), o.containerID, string(state.Status),
			strconv.Itoa(state.ExitCode), strconv.FormatBool(state.OOMKilled), state.Error,
		})
		finding.abnormal = append(finding.abnormal, webDetectorEvent{
			start: at, end: at, message: finding.message, issueType: "AppCrash",
		})
		return finding
	}
	finding.status = "Success"
	finding.message = "The site's container has not crashed."
	if state.Running {
		finding.rows = append(finding.rows, []string{
			webParseDockerTime(state.StartedAt).Format(time.RFC3339), o.containerID,
			string(state.Status), "0", "false", "",
		})
	}
	return finding
}

func measureSiteCPU(o webSiteObservation) webDetectorFinding {
	if o.stats == nil {
		return webNoContainerFinding("its CPU usage")
	}
	stats := o.stats
	percent := webContainerCPUPercent(*stats)
	throttled := stats.CPUStats.ThrottlingData.ThrottledPeriods
	finding := webDetectorFinding{
		tableName: "CpuAnalysis",
		columns: []webDetectorColumn{
			{"Timestamp", "DateTime", "datetime"},
			{"CpuPercentage", "Double", "double"},
			{"OnlineCpuCount", "Int64", "long"},
			{"ThrottledPeriods", "Int64", "long"},
			{"ThrottledTimeNanoseconds", "Int64", "long"},
		},
		rows: [][]string{{
			stats.Read.UTC().Format(time.RFC3339),
			strconv.FormatFloat(percent, 'f', 2, 64),
			strconv.FormatUint(uint64(stats.CPUStats.OnlineCPUs), 10),
			strconv.FormatUint(throttled, 10),
			strconv.FormatUint(stats.CPUStats.ThrottlingData.ThrottledTime, 10),
		}},
		metrics: []map[string]any{webDetectorMetric("CPU Percentage", "Percent", stats.PreRead, stats.Read, o.containerID, percent)},
	}
	if throttled > 0 {
		finding.issueDetected = true
		finding.status = "Warning"
		finding.message = fmt.Sprintf("The site's container was CPU-throttled in %d scheduling periods by the quota it runs under.", throttled)
		finding.abnormal = append(finding.abnormal, webDetectorEvent{
			start: stats.PreRead.UTC(), end: stats.Read.UTC(),
			message: finding.message, issueType: "RuntimeIssueDetected",
		})
		return finding
	}
	finding.status = "Success"
	finding.message = fmt.Sprintf("The site's container used %.2f%% CPU when sampled and was not throttled.", percent)
	return finding
}

// webContainerCPUPercent is the container's CPU usage between the engine's two
// samples, computed the way the engine's own client computes it: the delta of
// the container's CPU time over the delta of the host's, times the number of
// CPUs it may run on.
func webContainerCPUPercent(stats mobycontainer.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || systemDelta <= 0 {
		return 0
	}
	cpus := float64(stats.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return cpuDelta / systemDelta * cpus * 100
}

func measureSiteMemory(o webSiteObservation) webDetectorFinding {
	if o.stats == nil {
		return webNoContainerFinding("its memory usage")
	}
	mem := o.stats.MemoryStats
	finding := webDetectorFinding{
		tableName: "PhysicalMemoryAnalysis",
		columns: []webDetectorColumn{
			{"Timestamp", "DateTime", "datetime"},
			{"MemoryUsageBytes", "Int64", "long"},
			{"MemoryPeakBytes", "Int64", "long"},
			{"MemoryLimitBytes", "Int64", "long"},
			{"OOMKilled", "Boolean", "bool"},
		},
	}
	oomKilled := o.inspect != nil && o.inspect.State != nil && o.inspect.State.OOMKilled
	finding.rows = [][]string{{
		o.stats.Read.UTC().Format(time.RFC3339),
		strconv.FormatUint(mem.Usage, 10),
		strconv.FormatUint(mem.MaxUsage, 10),
		strconv.FormatUint(mem.Limit, 10),
		strconv.FormatBool(oomKilled),
	}}
	finding.metrics = []map[string]any{
		webDetectorMetric("Memory Usage", "Bytes", o.stats.PreRead, o.stats.Read, o.containerID, float64(mem.Usage)),
	}
	if oomKilled {
		finding.issueDetected = true
		finding.status = "Critical"
		finding.message = "The site's container was killed for exceeding the memory limit it runs under."
		at := webParseDockerTime(o.inspect.State.FinishedAt)
		if at.IsZero() {
			at = o.readAt
		}
		finding.abnormal = append(finding.abnormal, webDetectorEvent{
			start: at, end: at, message: finding.message, issueType: "AppCrash",
		})
		return finding
	}
	finding.status = "Success"
	finding.message = fmt.Sprintf("The site's container used %d bytes of memory when sampled.", mem.Usage)
	return finding
}

func measureSiteThreads(o webSiteObservation) webDetectorFinding {
	if !o.running() {
		return webNoContainerFinding("its thread count")
	}
	finding := webDetectorFinding{
		tableName: "ThreadCount",
		columns: []webDetectorColumn{
			{"Timestamp", "DateTime", "datetime"},
			{"ProcessId", "Int32", "int"},
			{"CommandLine", "String", "string"},
			{"ThreadCount", "Int32", "int"},
		},
		status: "Info",
	}
	total := 0
	for _, proc := range o.processes {
		total += proc.Threads
		finding.rows = append(finding.rows, []string{
			o.readAt.Format(time.RFC3339), strconv.Itoa(proc.PID), proc.CommandLine, strconv.Itoa(proc.Threads),
		})
	}
	finding.message = fmt.Sprintf("The site's container runs %d process(es) with %d thread(s) in total.",
		len(o.processes), total)
	finding.metrics = []map[string]any{
		webDetectorMetric("Thread Count", "Count", o.readAt, o.readAt, o.containerID, float64(total)),
	}
	return finding
}

func measureSiteRestarts(o webSiteObservation) webDetectorFinding {
	finding := webDetectorFinding{
		tableName: "UserInitiatedSiteRestarts",
		columns: []webDetectorColumn{
			{"Timestamp", "DateTime", "datetime"},
			{"Operation", "String", "string"},
			{"Cause", "String", "string"},
		},
		status: "Success",
	}
	for _, event := range o.events {
		if event.Cause != webEventCauseUser || event.Operation != "Restart" {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, event.Time)
		finding.rows = append(finding.rows, []string{
			at.UTC().Format(time.RFC3339), event.Operation, event.Cause,
		})
		finding.abnormal = append(finding.abnormal, webDetectorEvent{
			start: at.UTC(), end: at.UTC(),
			message:   "The site was restarted by a user-initiated restart operation.",
			issueType: "UserIssue",
		})
	}
	if len(finding.rows) > 0 {
		finding.issueDetected = true
		finding.status = "Info"
		finding.message = fmt.Sprintf("The site was restarted %d time(s) by a user in the period examined.", len(finding.rows))
		return finding
	}
	finding.message = "No user-initiated restart of the site was recorded in the period examined."
	return finding
}

func measureSiteDeployments(o webSiteObservation) webDetectorFinding {
	finding := webDetectorFinding{
		tableName: "SiteDeployments",
		columns: []webDetectorColumn{
			{"Timestamp", "DateTime", "datetime"},
			{"DeploymentId", "String", "string"},
			{"Status", "Int32", "int"},
			{"Active", "Boolean", "bool"},
			{"Author", "String", "string"},
			{"Message", "String", "string"},
		},
		status: "Success",
	}
	prefix := o.resourceID + "/deployments/"
	deployments := webDeployments.Filter(func(d WebDeployment) bool { return strings.HasPrefix(d.ID, prefix) })
	sort.Slice(deployments, func(i, j int) bool { return deployments[i].ID < deployments[j].ID })
	failed := 0
	for _, d := range deployments {
		at := webParseAnyTime(d.Properties.EndTime, d.Properties.StartTime)
		if at.IsZero() {
			at = o.readAt
		}
		if at.Before(o.from) || at.After(o.to) {
			continue
		}
		// Kudu reports a failed deployment as status 3; every other value is a
		// deployment that is pending, running or succeeded.
		if d.Properties.Status == 3 {
			failed++
		}
		finding.rows = append(finding.rows, []string{
			at.UTC().Format(time.RFC3339), d.Properties.ID, strconv.Itoa(d.Properties.Status),
			strconv.FormatBool(d.Properties.Active), d.Properties.Author, d.Properties.Message,
		})
	}
	switch {
	case failed > 0:
		finding.issueDetected = true
		finding.status = "Critical"
		finding.message = fmt.Sprintf("%d of the site's %d deployment record(s) report a failed deployment.", failed, len(finding.rows))
	case len(finding.rows) > 0:
		finding.message = fmt.Sprintf("The site has %d deployment record(s), none of them failed.", len(finding.rows))
	default:
		finding.message = "The site has no deployment records in the period examined."
	}
	return finding
}

// webDetectorMetric renders one measured sample as the swagger's
// DiagnosticMetricSet.
func webDetectorMetric(name, unit string, start, end time.Time, roleInstance string, value float64) map[string]any {
	if start.IsZero() {
		start = end
	}
	grain := end.Sub(start)
	if grain < 0 {
		grain = 0
	}
	return map[string]any{
		"name":      name,
		"unit":      unit,
		"startTime": start.UTC().Format(time.RFC3339),
		"endTime":   end.UTC().Format(time.RFC3339),
		"timeGrain": fmt.Sprintf("%02d:%02d:%02d", int(grain.Hours()), int(grain.Minutes())%60, int(grain.Seconds())%60),
		"values": []map[string]any{{
			"timestamp":    end.UTC().Format(time.RFC3339),
			"roleInstance": roleInstance,
			"total":        value,
			"maximum":      value,
			"minimum":      value,
			"isAggregated": false,
		}},
	}
}

func webParseDockerTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	if at.Year() <= 1 {
		return time.Time{}
	}
	return at.UTC()
}

func webParseAnyTime(values ...string) time.Time {
	for _, v := range values {
		if at := webParseDockerTime(v); !at.IsZero() {
			return at
		}
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// wire rendering
// ---------------------------------------------------------------------------

func webDiagnosticChildType(r *http.Request, suffix string) string {
	base := "Microsoft.Web/sites"
	if sim.PathParam(r, "slot") != "" {
		base = "Microsoft.Web/sites/slots"
	}
	return base + "/" + suffix
}

func webDetectorTable(finding webDetectorFinding) map[string]any {
	columns := make([]map[string]any, 0, len(finding.columns))
	for _, c := range finding.columns {
		columns = append(columns, map[string]any{
			"columnName": c.name, "dataType": c.dataType, "columnType": c.columnType,
		})
	}
	rows := finding.rows
	if rows == nil {
		rows = [][]string{}
	}
	return map[string]any{
		"tableName": finding.tableName,
		"columns":   columns,
		"rows":      rows,
	}
}

// webDetectorResponseDoc renders one detector in the DetectorResponse shape the
// site's own /detectors collection publishes.
func webDetectorResponseDoc(r *http.Request, base string, det webDetector, finding webDetectorFinding) map[string]any {
	id := base + "/detectors/" + det.name
	props := map[string]any{
		"metadata": map[string]any{
			"id":          id,
			"name":        det.displayName,
			"description": det.description,
			"category":    webDiagnosticCategoryDescription,
			"type":        "Detector",
		},
		"dataset": []map[string]any{{
			"table": webDetectorTable(finding),
			"renderingProperties": map[string]any{
				"type":        "Table",
				"title":       det.displayName,
				"description": det.description,
			},
		}},
		"status": map[string]any{
			"message":  finding.message,
			"statusId": webInsightStatus(finding.status),
		},
	}
	return map[string]any{
		"id":         id,
		"name":       det.name,
		"type":       webDiagnosticChildType(r, "detectors"),
		"properties": props,
	}
}

// webInsightStatus is the finding's severity as one of the swagger's
// InsightStatus values.
func webInsightStatus(status string) string {
	switch status {
	case "Critical", "Warning", "Info", "Success":
		return status
	}
	return "None"
}

// webDiagnosticDetectorResponseDoc renders one detector run in the
// DiagnosticDetectorResponse shape the execute operation answers with.
func webDiagnosticDetectorResponseDoc(r *http.Request, base string, det webDetector, finding webDetectorFinding, from, to time.Time) map[string]any {
	abnormal := make([]map[string]any, 0, len(finding.abnormal))
	for _, event := range finding.abnormal {
		abnormal = append(abnormal, map[string]any{
			"startTime": event.start.UTC().Format(time.RFC3339),
			"endTime":   event.end.UTC().Format(time.RFC3339),
			"message":   event.message,
			"source":    det.name,
			"priority":  det.rank,
			"type":      event.issueType,
			"solutions": []any{},
		})
	}
	data := make([][]map[string]any, 0, len(finding.rows))
	for _, row := range finding.rows {
		pairs := make([]map[string]any, 0, len(row))
		for i, value := range row {
			if i >= len(finding.columns) {
				break
			}
			pairs = append(pairs, map[string]any{"name": finding.columns[i].name, "value": value})
		}
		data = append(data, pairs)
	}
	metrics := finding.metrics
	if metrics == nil {
		metrics = []map[string]any{}
	}
	return map[string]any{
		"id":   base + "/diagnostics/" + webDiagnosticCategory + "/detectors/" + det.name,
		"name": det.name,
		"type": webDiagnosticChildType(r, "diagnostics/detectors"),
		"properties": map[string]any{
			"startTime":           from.UTC().Format(time.RFC3339),
			"endTime":             to.UTC().Format(time.RFC3339),
			"issueDetected":       finding.issueDetected,
			"detectorDefinition":  webDetectorDefinitionDoc(det),
			"metrics":             metrics,
			"abnormalTimePeriods": abnormal,
			"data":                data,
		},
	}
}

func webDetectorDefinitionDoc(det webDetector) map[string]any {
	return map[string]any{
		"displayName": det.displayName,
		"description": det.description,
		"rank":        det.rank,
		"isEnabled":   true,
	}
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

// webDiagnosticWindow is the period the operation reports over: the one the
// client named, or the last day ending now.
func webDiagnosticWindow(r *http.Request) (time.Time, time.Time) {
	to := time.Now().UTC()
	if raw := r.URL.Query().Get("endTime"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			to = parsed.UTC()
		}
	}
	from := to.Add(-24 * time.Hour)
	if raw := r.URL.Query().Get("startTime"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			from = parsed.UTC()
		}
	}
	return from, to
}

// webRequireDiagnosticCategory resolves the diagnostic category the request
// addresses. The simulator publishes one category — the availability and
// performance measurements it can make — so any other name is not found.
func webRequireDiagnosticCategory(w http.ResponseWriter, r *http.Request) bool {
	category := sim.PathParam(r, "diagnosticCategory")
	if strings.EqualFold(category, webDiagnosticCategory) {
		return true
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The diagnostic category '%s' does not exist on this site. The categories it publishes are: %s.",
		category, webDiagnosticCategory)
	return false
}

// webFindDetector resolves a detector by name, answering the declared gap for
// a detector of Microsoft's catalog the simulator cannot measure and the ARM
// 404 for a name that is not a detector at all.
func webFindDetector(w http.ResponseWriter, name string) (webDetector, bool) {
	for _, det := range webDetectors() {
		if strings.EqualFold(det.name, name) {
			return det, true
		}
	}
	for _, unserved := range webUnservedDetectors() {
		if strings.EqualFold(unserved.name, name) {
			sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"The detector '%s' is not implemented by the simulator: it would need %s.",
				unserved.name, unserved.reason)
			return webDetector{}, false
		}
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The detector '%s' does not exist on this site.", name)
	return webDetector{}, false
}

func webFindAnalysis(w http.ResponseWriter, name string) (webAnalysis, bool) {
	for _, analysis := range webAnalyses() {
		if strings.EqualFold(analysis.name, name) {
			return analysis, true
		}
	}
	for _, unserved := range webUnservedAnalyses() {
		if strings.EqualFold(unserved.name, name) {
			sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"The analysis '%s' is not implemented by the simulator: it would need %s.",
				unserved.name, unserved.reason)
			return webAnalysis{}, false
		}
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The analysis '%s' does not exist on this site.", name)
	return webAnalysis{}, false
}

// registerWebDiagnostics mounts the site and slot diagnostics family.
func registerWebDiagnostics(both func(string, string, http.HandlerFunc)) {
	// Diagnostics_ListSiteDiagnosticCategories[Slot]
	both("GET", "/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		writeARMCollection(w, r, []map[string]any{webDiagnosticCategoryDoc(r)})
	})

	// Diagnostics_GetSiteDiagnosticCategory[Slot]
	both("GET", "/diagnostics/{diagnosticCategory}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, webDiagnosticCategoryDoc(r))
	})

	// Diagnostics_ListSiteDetectors[Slot]
	both("GET", "/diagnostics/{diagnosticCategory}/detectors", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		base := webResourceID(r)
		out := []map[string]any{}
		for _, det := range webDetectors() {
			out = append(out, map[string]any{
				"id":         base + "/diagnostics/" + webDiagnosticCategory + "/detectors/" + det.name,
				"name":       det.name,
				"type":       webDiagnosticChildType(r, "diagnostics/detectors"),
				"properties": webDetectorDefinitionDoc(det),
			})
		}
		writeARMCollection(w, r, out)
	})

	// Diagnostics_GetSiteDetector[Slot]
	both("GET", "/diagnostics/{diagnosticCategory}/detectors/{detectorName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		det, ok := webFindDetector(w, sim.PathParam(r, "detectorName"))
		if !ok {
			return
		}
		base := webResourceID(r)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         base + "/diagnostics/" + webDiagnosticCategory + "/detectors/" + det.name,
			"name":       det.name,
			"type":       webDiagnosticChildType(r, "diagnostics/detectors"),
			"properties": webDetectorDefinitionDoc(det),
		})
	})

	// Diagnostics_ExecuteSiteDetector[Slot] — run the detector now.
	both("POST", "/diagnostics/{diagnosticCategory}/detectors/{detectorName}/execute", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		det, ok := webFindDetector(w, sim.PathParam(r, "detectorName"))
		if !ok {
			return
		}
		site, _ := webResource(r)
		from, to := webDiagnosticWindow(r)
		obs := observeWebSite(site, webResourceID(r), from, to)
		sim.WriteJSON(w, http.StatusOK,
			webDiagnosticDetectorResponseDoc(r, webResourceID(r), det, det.measure(obs), from, to))
	})

	// Diagnostics_ListSiteAnalyses[Slot]
	both("GET", "/diagnostics/{diagnosticCategory}/analyses", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		out := []map[string]any{}
		for _, analysis := range webAnalyses() {
			out = append(out, webAnalysisDefinitionDoc(r, analysis))
		}
		writeARMCollection(w, r, out)
	})

	// Diagnostics_GetSiteAnalysis[Slot]
	both("GET", "/diagnostics/{diagnosticCategory}/analyses/{analysisName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		analysis, ok := webFindAnalysis(w, sim.PathParam(r, "analysisName"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, webAnalysisDefinitionDoc(r, analysis))
	})

	// Diagnostics_ExecuteSiteAnalysis[Slot] — run every detector the analysis
	// aggregates and report what they found.
	both("POST", "/diagnostics/{diagnosticCategory}/analyses/{analysisName}/execute", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !webRequireDiagnosticCategory(w, r) {
			return
		}
		analysis, ok := webFindAnalysis(w, sim.PathParam(r, "analysisName"))
		if !ok {
			return
		}
		site, _ := webResource(r)
		from, to := webDiagnosticWindow(r)
		obs := observeWebSite(site, webResourceID(r), from, to)

		payload := []map[string]any{}
		abnormal := []map[string]any{}
		for _, name := range analysis.detectors {
			det, found := webDetectorByName(name)
			if !found {
				continue
			}
			finding := det.measure(obs)
			metrics := finding.metrics
			if metrics == nil {
				metrics = []map[string]any{}
			}
			payload = append(payload, map[string]any{
				"source":             det.name,
				"detectorDefinition": webDetectorDefinitionDoc(det),
				"metrics":            metrics,
			})
			for _, event := range finding.abnormal {
				abnormal = append(abnormal, map[string]any{
					"startTime": event.start.UTC().Format(time.RFC3339),
					"endTime":   event.end.UTC().Format(time.RFC3339),
					"events": []map[string]any{{
						"startTime": event.start.UTC().Format(time.RFC3339),
						"endTime":   event.end.UTC().Format(time.RFC3339),
						"message":   event.message,
						"source":    det.name,
						"priority":  det.rank,
						"type":      event.issueType,
						"solutions": []any{},
					}},
					"solutions": []any{},
				})
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   webResourceID(r) + "/diagnostics/" + webDiagnosticCategory + "/analyses/" + analysis.name,
			"name": analysis.name,
			"type": webDiagnosticChildType(r, "diagnostics/analyses"),
			"properties": map[string]any{
				"startTime":              from.UTC().Format(time.RFC3339),
				"endTime":                to.UTC().Format(time.RFC3339),
				"abnormalTimePeriods":    abnormal,
				"payload":                payload,
				"nonCorrelatedDetectors": []any{},
			},
		})
	})

	// Diagnostics_ListSiteDetectorResponses[Slot] — the site's own detector
	// collection, every detector run at read time.
	both("GET", "/detectors", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		from, to := webDiagnosticWindow(r)
		obs := observeWebSite(site, webResourceID(r), from, to)
		out := []map[string]any{}
		for _, det := range webDetectors() {
			out = append(out, webDetectorResponseDoc(r, webResourceID(r), det, det.measure(obs)))
		}
		writeARMCollection(w, r, out)
	})

	// Diagnostics_GetSiteDetectorResponse[Slot]
	both("GET", "/detectors/{detectorName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		det, ok := webFindDetector(w, sim.PathParam(r, "detectorName"))
		if !ok {
			return
		}
		site, _ := webResource(r)
		from, to := webDiagnosticWindow(r)
		obs := observeWebSite(site, webResourceID(r), from, to)
		sim.WriteJSON(w, http.StatusOK,
			webDetectorResponseDoc(r, webResourceID(r), det, det.measure(obs)))
	})
}

func webDetectorByName(name string) (webDetector, bool) {
	for _, det := range webDetectors() {
		if det.name == name {
			return det, true
		}
	}
	return webDetector{}, false
}

func webDiagnosticCategoryDoc(r *http.Request) map[string]any {
	return map[string]any{
		"id":         webResourceID(r) + "/diagnostics/" + webDiagnosticCategory,
		"name":       webDiagnosticCategory,
		"type":       webDiagnosticChildType(r, "diagnostics"),
		"properties": map[string]any{"description": webDiagnosticCategoryDescription},
	}
}

func webAnalysisDefinitionDoc(r *http.Request, analysis webAnalysis) map[string]any {
	return map[string]any{
		"id":         webResourceID(r) + "/diagnostics/" + webDiagnosticCategory + "/analyses/" + analysis.name,
		"name":       analysis.name,
		"type":       webDiagnosticChildType(r, "diagnostics/analyses"),
		"properties": map[string]any{"description": analysis.description},
	}
}

// ---------------------------------------------------------------------------
// hosting-environment diagnostics
// ---------------------------------------------------------------------------

// registerWebEnvironmentDiagnostics mounts the four diagnostics operations
// scoped to an App Service Environment. Every detector the simulator computes
// measures one site's workload container, and an environment is not a site: it
// has no container, no process table and no request log of its own. It
// therefore publishes no detectors and no diagnostic bundles — the empty
// collections the specification's own AppServiceEnvironments_ListDiagnostics
// and Diagnostics_ListHostingEnvironmentDetectorResponses examples show — and
// a read of one by name reports, correctly, that the environment has none. The
// per-site detectors of the apps IN the environment are read at those apps.
func registerWebEnvironmentDiagnostics(ase func(string, string, http.HandlerFunc)) {
	// AppServiceEnvironments_ListDiagnostics
	ase("GET", "/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		// The operation's response is a bare array of
		// HostingEnvironmentDiagnostics, not a paged collection.
		sim.WriteJSON(w, http.StatusOK, []any{})
	})

	// AppServiceEnvironments_GetDiagnosticsItem
	ase("GET", "/diagnostics/{diagnosticsName}", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The diagnostics item '%s' does not exist on App Service Environment '%s': the simulator computes no hosting-environment diagnostics.",
			sim.PathParam(r, "diagnosticsName"), row.Name)
	})

	// Diagnostics_ListHostingEnvironmentDetectorResponses
	ase("GET", "/detectors", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		writeARMCollection(w, r, []map[string]any{})
	})

	// Diagnostics_GetHostingEnvironmentDetectorResponse
	ase("GET", "/detectors/{detectorName}", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The detector '%s' does not exist on App Service Environment '%s': the simulator computes no hosting-environment detectors.",
			sim.PathParam(r, "detectorName"), row.Name)
	})
}
