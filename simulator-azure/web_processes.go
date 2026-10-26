package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// A container-image App Service site runs as a real container, so the site
// instance is the running workload container and its process list is the
// engine's `GET /containers/{id}/top` table. The WebSiteInstanceStatus
// `containers` member is the engine's own statistics document, member for
// member. A stopped site has no instances and no processes, as on Azure.
// ProcessInfo members the engine does not report are omitted rather than
// invented.

// webProcess merges two `ps` projections: the default one, whose trailing
// command column keeps the command line's spaces, and an all-numeric one that
// carries the memory, thread and timing columns.
type webProcess struct {
	PID         int
	PPID        int
	User        string
	CommandLine string
	RSSKiB      int64
	VSZKiB      int64
	Threads     int
	ElapsedSecs int64
	CPUTime     string
}

type webThread struct {
	PID         int
	TID         int
	State       string
	CPUTime     string
	Priority    int
	ElapsedSecs int64
}

// The second result is false when the site has no running workload.
func webSiteInstanceContainer(site Site) (placement.Handle, bool) {
	_, handle := azfInstanceFor(site.Name).running()
	if handle == nil {
		return nil, false
	}
	select {
	case <-handle.Exited():
		return nil, false
	default:
		return handle, true
	}
}

func webContainerTop(containerID string, args ...string) ([]string, [][]string, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return nil, nil, fmt.Errorf("no container engine is available to read the site's process table")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := cli.ContainerTop(ctx, containerID, mobyclient.ContainerTopOptions{Arguments: args})
	if err != nil {
		return nil, nil, err
	}
	return res.Titles, res.Processes, nil
}

// Engines spell the same column differently (`UID` and `USER`, `CMD` and
// `COMMAND`), so a caller names every spelling it accepts.
func webTopColumn(titles []string, row []string, names ...string) string {
	for _, name := range names {
		for i, title := range titles {
			if !strings.EqualFold(title, name) || i >= len(row) {
				continue
			}
			return strings.TrimSpace(row[i])
		}
	}
	return ""
}

func webTopInt(titles []string, row []string, names ...string) int64 {
	v, err := strconv.ParseInt(webTopColumn(titles, row, names...), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func webProcessTable(containerID string) ([]webProcess, error) {
	titles, rows, err := webContainerTop(containerID)
	if err != nil {
		return nil, err
	}
	byPID := map[int]*webProcess{}
	var order []int
	for _, row := range rows {
		pid := int(webTopInt(titles, row, "PID"))
		if pid == 0 {
			continue
		}
		byPID[pid] = &webProcess{
			PID:         pid,
			PPID:        int(webTopInt(titles, row, "PPID")),
			User:        webTopColumn(titles, row, "UID", "USER"),
			CommandLine: webTopColumn(titles, row, "CMD", "COMMAND"),
			CPUTime:     webTopColumn(titles, row, "TIME"),
		}
		order = append(order, pid)
	}

	detailTitles, detailRows, err := webContainerTop(containerID,
		"-eo", "pid,ppid,rss,vsz,nlwp,etimes,time,user")
	if err != nil {
		return nil, err
	}
	for _, row := range detailRows {
		pid := int(webTopInt(detailTitles, row, "PID"))
		proc, ok := byPID[pid]
		if !ok {
			continue
		}
		proc.RSSKiB = webTopInt(detailTitles, row, "RSS")
		proc.VSZKiB = webTopInt(detailTitles, row, "VSZ")
		proc.Threads = int(webTopInt(detailTitles, row, "NLWP"))
		proc.ElapsedSecs = webTopInt(detailTitles, row, "ELAPSED")
		if proc.User == "" {
			proc.User = webTopColumn(detailTitles, row, "USER", "UID")
		}
	}

	sort.Ints(order)
	out := make([]webProcess, 0, len(order))
	for _, pid := range order {
		out = append(out, *byPID[pid])
	}
	return out, nil
}

func webThreadTable(containerID string) ([]webThread, error) {
	titles, rows, err := webContainerTop(containerID, "-eLo", "pid,tid,stat,time,pri,etimes")
	if err != nil {
		return nil, err
	}
	out := make([]webThread, 0, len(rows))
	for _, row := range rows {
		pid := int(webTopInt(titles, row, "PID"))
		tid := int(webTopInt(titles, row, "TID", "LWP", "SPID"))
		if pid == 0 || tid == 0 {
			continue
		}
		out = append(out, webThread{
			PID:         pid,
			TID:         tid,
			State:       webTopColumn(titles, row, "STAT", "S"),
			CPUTime:     webTopColumn(titles, row, "TIME"),
			Priority:    int(webTopInt(titles, row, "PRI")),
			ElapsedSecs: webTopInt(titles, row, "ELAPSED"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PID != out[j].PID {
			return out[i].PID < out[j].PID
		}
		return out[i].TID < out[j].TID
	})
	return out, nil
}

func webContainerStats(containerID string) (mobycontainer.StatsResponse, error) {
	var stats mobycontainer.StatsResponse
	cli := sim.DockerClient()
	if cli == nil {
		return stats, fmt.Errorf("no container engine is available to read the instance's statistics")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := cli.ContainerStats(ctx, containerID, mobyclient.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return stats, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func webProcessResourceID(base string, instanceID string, pid int) string {
	if instanceID != "" {
		return fmt.Sprintf("%s/instances/%s/processes/%d", base, instanceID, pid)
	}
	return fmt.Sprintf("%s/processes/%d", base, pid)
}

// webProcessDoc renders one ProcessInfo with only the members the engine's
// process table reports.
func webProcessDoc(base, instanceID string, proc webProcess, procs []webProcess, readAt time.Time) map[string]any {
	href := webProcessResourceID(base, instanceID, proc.PID)
	props := map[string]any{
		"identifier":   proc.PID,
		"href":         href,
		"command_line": proc.CommandLine,
		"user_name":    proc.User,
		"is_scm_site":  false,
		"is_webjob":    false,
		"time_stamp":   readAt.UTC().Format(time.RFC3339),
	}
	if name := webProcessFileName(proc.CommandLine); name != "" {
		props["file_name"] = name
	}
	if proc.PPID != 0 {
		props["parent"] = webProcessResourceID(base, instanceID, proc.PPID)
	}
	var children []string
	for _, other := range procs {
		if other.PPID == proc.PID && other.PID != proc.PID {
			children = append(children, webProcessResourceID(base, instanceID, other.PID))
		}
	}
	if children != nil {
		props["children"] = children
	}
	if proc.Threads > 0 {
		props["thread_count"] = proc.Threads
	}
	if proc.RSSKiB > 0 {
		props["working_set"] = proc.RSSKiB * 1024
	}
	if proc.VSZKiB > 0 {
		props["virtual_memory"] = proc.VSZKiB * 1024
	}
	if proc.CPUTime != "" {
		props["total_cpu_time"] = proc.CPUTime
	}
	if proc.ElapsedSecs >= 0 {
		props["start_time"] = readAt.Add(-time.Duration(proc.ElapsedSecs) * time.Second).UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id":         href,
		"name":       strconv.Itoa(proc.PID),
		"type":       "Microsoft.Web/sites/processes",
		"properties": props,
	}
}

// webProcessFileName is the first token of the command line.
func webProcessFileName(commandLine string) string {
	fields := strings.Fields(commandLine)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func webThreadDoc(base, instanceID string, thread webThread, readAt time.Time) map[string]any {
	processHref := webProcessResourceID(base, instanceID, thread.PID)
	href := fmt.Sprintf("%s/threads/%d", processHref, thread.TID)
	props := map[string]any{
		"identifier": thread.TID,
		"href":       href,
		"process":    processHref,
		"state":      thread.State,
		"start_time": readAt.Add(-time.Duration(thread.ElapsedSecs) * time.Second).UTC().Format(time.RFC3339),
	}
	if thread.CPUTime != "" {
		props["total_processor_time"] = thread.CPUTime
	}
	if thread.Priority != 0 {
		props["current_priority"] = thread.Priority
	}
	return map[string]any{
		"id":         href,
		"name":       strconv.Itoa(thread.TID),
		"type":       "Microsoft.Web/sites/processes/threads",
		"properties": props,
	}
}

func webContainerInfoDoc(containerID string, stats mobycontainer.StatsResponse) map[string]any {
	info := map[string]any{
		"id":               containerID,
		"name":             strings.TrimPrefix(stats.Name, "/"),
		"currentTimeStamp": stats.Read.UTC().Format(time.RFC3339),
		"currentCpuStats":  webCPUStatsDoc(stats.CPUStats),
		"memoryStats": map[string]any{
			"usage":    stats.MemoryStats.Usage,
			"maxUsage": stats.MemoryStats.MaxUsage,
			"limit":    stats.MemoryStats.Limit,
		},
	}
	if !stats.PreRead.IsZero() {
		info["previousTimeStamp"] = stats.PreRead.UTC().Format(time.RFC3339)
		info["previousCpuStats"] = webCPUStatsDoc(stats.PreCPUStats)
	}
	if eth0, ok := stats.Networks["eth0"]; ok {
		info["eth0"] = map[string]any{
			"rxBytes":   eth0.RxBytes,
			"rxPackets": eth0.RxPackets,
			"rxErrors":  eth0.RxErrors,
			"rxDropped": eth0.RxDropped,
			"txBytes":   eth0.TxBytes,
			"txPackets": eth0.TxPackets,
			"txErrors":  eth0.TxErrors,
			"txDropped": eth0.TxDropped,
		}
	}
	return info
}

func webCPUStatsDoc(cpu mobycontainer.CPUStats) map[string]any {
	usage := map[string]any{
		"totalUsage":      cpu.CPUUsage.TotalUsage,
		"kernelModeUsage": cpu.CPUUsage.UsageInKernelmode,
		"userModeUsage":   cpu.CPUUsage.UsageInUsermode,
	}
	if len(cpu.CPUUsage.PercpuUsage) > 0 {
		usage["perCpuUsage"] = cpu.CPUUsage.PercpuUsage
	}
	return map[string]any{
		"cpuUsage":       usage,
		"systemCpuUsage": cpu.SystemUsage,
		"onlineCpuCount": cpu.OnlineCPUs,
		"throttlingData": map[string]any{
			"periods":          cpu.ThrottlingData.Periods,
			"throttledPeriods": cpu.ThrottlingData.ThrottledPeriods,
			"throttledTime":    cpu.ThrottlingData.ThrottledTime,
		},
	}
}

// The URL members Azure fills with Kudu links (statusUrl, consoleUrl,
// detectorUrl, healthCheckUrl) are absent: the simulator serves no Kudu
// status, console or detector endpoint for them to name.
func webInstanceDoc(base, instanceID string, running bool, containers map[string]any) map[string]any {
	state := "STOPPED"
	if running {
		state = "READY"
	}
	props := map[string]any{"state": state}
	if containers != nil {
		props["containers"] = containers
	}
	return map[string]any{
		"id":         base + "/instances/" + instanceID,
		"name":       instanceID,
		"type":       "Microsoft.Web/sites/instances",
		"properties": props,
	}
}

// Answering an empty collection would claim the site has no processes when
// the truth is unknown.
func webEngineFailure(w http.ResponseWriter, err error) {
	AzureError(w, "InternalServerError",
		"Could not read the site instance from the container engine: "+err.Error(),
		http.StatusInternalServerError)
}

func registerWebProcesses(both func(string, string, http.HandlerFunc)) {
	both("GET", "/instances", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		base := webResourceID(r)
		values := []map[string]any{}
		if instance, ok := webSiteInstanceContainer(site); ok {
			values = append(values, webInstanceDoc(base, instance.ID(), true, nil))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": values})
	})

	both("GET", "/instances/{instanceId}", func(w http.ResponseWriter, r *http.Request) {
		instance, ok := webRequireInstance(w, r)
		if !ok {
			return
		}
		containerID := instance.ID()
		stats, err := webContainerStats(containerID)
		if err != nil {
			webEngineFailure(w, err)
			return
		}
		info := webContainerInfoDoc(containerID, stats)
		name, _ := info["name"].(string)
		if name == "" {
			name = containerID
		}
		sim.WriteJSON(w, http.StatusOK, webInstanceDoc(webResourceID(r), containerID, true,
			map[string]any{name: info}))
	})

	both("GET", "/processes", webListProcesses)
	both("GET", "/instances/{instanceId}/processes", webListProcesses)

	both("GET", "/processes/{processId}", webGetProcess)
	both("GET", "/instances/{instanceId}/processes/{processId}", webGetProcess)

	both("GET", "/processes/{processId}/threads", webListProcessThreads)
	both("GET", "/instances/{instanceId}/processes/{processId}/threads", webListProcessThreads)

	both("DELETE", "/processes/{processId}", webDeleteProcess)
	both("DELETE", "/instances/{instanceId}/processes/{processId}", webDeleteProcess)

	both("GET", "/processes/{processId}/modules", webListProcessModules)
	both("GET", "/instances/{instanceId}/processes/{processId}/modules", webListProcessModules)
	both("GET", "/processes/{processId}/modules/{baseAddress}", webGetProcessModule)
	both("GET", "/instances/{instanceId}/processes/{processId}/modules/{baseAddress}", webGetProcessModule)
}

// webRequireInstance writes the ARM 404 when either the site or the instance
// the request addresses is not there.
func webRequireInstance(w http.ResponseWriter, r *http.Request) (placement.Handle, bool) {
	instance, running, ok := webResolveInstance(w, r)
	if !ok {
		return nil, false
	}
	if !running {
		webInstanceNotFound(w, r)
		return nil, false
	}
	return instance, true
}

// The second result is false when the site has no running container or the
// request names an instance that is not the running one; the third is false
// only when the site does not exist, and the ARM 404 is already written.
func webResolveInstance(w http.ResponseWriter, r *http.Request) (placement.Handle, bool, bool) {
	if webMissing(w, r) {
		return nil, false, false
	}
	site, _ := webResource(r)
	instance, running := webSiteInstanceContainer(site)
	if requested := sim.PathParam(r, "instanceId"); requested != "" && (!running || requested != instance.ID()) {
		return nil, false, true
	}
	return instance, running, true
}

func webInstanceNotFound(w http.ResponseWriter, r *http.Request) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/sites/%s/instances/%s' was not found.",
		sim.PathParam(r, "siteName"), sim.PathParam(r, "instanceId"))
}

// A stopped site answers an empty process table, not an error.
func webSiteProcesses(w http.ResponseWriter, r *http.Request) (placement.Handle, []webProcess, bool) {
	instance, running, ok := webResolveInstance(w, r)
	if !ok {
		return nil, nil, false
	}
	if !running {
		return nil, nil, true
	}
	procs, err := webProcessTable(instance.ID())
	if err != nil {
		webEngineFailure(w, err)
		return nil, nil, false
	}
	return instance, procs, true
}

// webInstanceSegment is "" for the site-scoped spelling, so the resource IDs a
// response carries match the path the client called.
func webInstanceSegment(r *http.Request) string { return sim.PathParam(r, "instanceId") }

func webListProcesses(w http.ResponseWriter, r *http.Request) {
	instance, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if instance == nil && webInstanceSegment(r) != "" {
		webInstanceNotFound(w, r)
		return
	}
	base := webResourceID(r)
	instanceID := webInstanceSegment(r)
	readAt := time.Now()
	values := make([]map[string]any, 0, len(procs))
	for _, proc := range procs {
		values = append(values, webProcessDoc(base, instanceID, proc, procs, readAt))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": values})
}

func webGetProcess(w http.ResponseWriter, r *http.Request) {
	instance, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if instance == nil && webInstanceSegment(r) != "" {
		webInstanceNotFound(w, r)
		return
	}
	pid, err := strconv.Atoi(sim.PathParam(r, "processId"))
	if err != nil {
		AzureError(w, "BadRequest", "The process identifier must be a number.", http.StatusBadRequest)
		return
	}
	for _, proc := range procs {
		if proc.PID != pid {
			continue
		}
		sim.WriteJSON(w, http.StatusOK,
			webProcessDoc(webResourceID(r), webInstanceSegment(r), proc, procs, time.Now()))
		return
	}
	webProcessNotFound(w, r, pid)
}

func webListProcessThreads(w http.ResponseWriter, r *http.Request) {
	instance, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if instance == nil && webInstanceSegment(r) != "" {
		webInstanceNotFound(w, r)
		return
	}
	pid, err := strconv.Atoi(sim.PathParam(r, "processId"))
	if err != nil {
		AzureError(w, "BadRequest", "The process identifier must be a number.", http.StatusBadRequest)
		return
	}
	found := false
	for _, proc := range procs {
		if proc.PID == pid {
			found = true
			break
		}
	}
	if !found {
		webProcessNotFound(w, r, pid)
		return
	}
	threads, err := webThreadTable(instance.ID())
	if err != nil {
		webEngineFailure(w, err)
		return
	}
	base := webResourceID(r)
	instanceID := webInstanceSegment(r)
	readAt := time.Now()
	values := make([]map[string]any, 0, len(threads))
	for _, thread := range threads {
		if thread.PID != pid {
			continue
		}
		values = append(values, webThreadDoc(base, instanceID, thread, readAt))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": values})
}

func webProcessNotFound(w http.ResponseWriter, r *http.Request, pid int) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/sites/%s/processes/%d' was not found.",
		sim.PathParam(r, "siteName"), pid)
}

func webDeleteProcess(w http.ResponseWriter, r *http.Request) {
	instance, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if instance == nil {
		webInstanceNotFound(w, r)
		return
	}
	pid, err := strconv.Atoi(sim.PathParam(r, "processId"))
	if err != nil {
		AzureError(w, "BadRequest", "The process identifier must be a number.", http.StatusBadRequest)
		return
	}
	found := false
	for _, proc := range procs {
		if proc.PID == pid {
			found = true
			break
		}
	}
	if !found {
		webProcessNotFound(w, r, pid)
		return
	}
	if err := webKillContainerProcess(instance, pid); err != nil {
		webEngineFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// No backend kills a single process, so run kill in the workload, as an
// operator would.
func webKillContainerProcess(instance placement.Handle, pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := instance.Exec(ctx, []string{"kill", "-TERM", strconv.Itoa(pid)})
	return err
}

func webListProcessModules(w http.ResponseWriter, r *http.Request) {
	proc, instance, ok := webResolveProcess(w, r)
	if !ok {
		return
	}
	modules, readable, err := webProcessModules(r.Context(), instance.ID(), proc)
	if !readable {
		webProcessModulesUnreadable(w, "WebApps_ListProcessModules")
		return
	}
	if err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"WebApps_ListProcessModules: read the process's mapping table: %v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": webProcessModuleDocs(webResourceID(r), webInstanceSegment(r), proc, modules),
	})
}

func webGetProcessModule(w http.ResponseWriter, r *http.Request) {
	proc, instance, ok := webResolveProcess(w, r)
	if !ok {
		return
	}
	modules, readable, err := webProcessModules(r.Context(), instance.ID(), proc)
	if !readable {
		webProcessModulesUnreadable(w, "WebApps_GetProcessModule")
		return
	}
	if err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"WebApps_GetProcessModule: read the process's mapping table: %v", err)
		return
	}
	wanted := sim.PathParam(r, "baseAddress")
	for _, entry := range webProcessModuleDocs(webResourceID(r), webInstanceSegment(r), proc, modules) {
		module, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		props, _ := module["properties"].(map[string]any)
		if props != nil && props["base_address"] == wanted {
			sim.WriteJSON(w, http.StatusOK, module)
			return
		}
	}
	AzureError(w, "NotFound",
		fmt.Sprintf("No module is mapped at %q in this process.", wanted), http.StatusNotFound)
}

func webResolveProcess(w http.ResponseWriter, r *http.Request) (webProcess, placement.Handle, bool) {
	instance, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return webProcess{}, nil, false
	}
	if instance == nil {
		webInstanceNotFound(w, r)
		return webProcess{}, nil, false
	}
	pid, err := strconv.Atoi(sim.PathParam(r, "processId"))
	if err != nil {
		AzureError(w, "BadRequest", "The process identifier must be a number.", http.StatusBadRequest)
		return webProcess{}, nil, false
	}
	for _, proc := range procs {
		if proc.PID == pid {
			return proc, instance, true
		}
	}
	webProcessNotFound(w, r, pid)
	return webProcess{}, nil, false
}

func webProcessModuleDocs(base, instanceID string, proc webProcess, modules []webProcModule) []any {
	docs := make([]any, 0, len(modules))
	for _, module := range modules {
		address := webProcModuleAddress(module.BaseAddress)
		name := module.Path[strings.LastIndex(module.Path, "/")+1:]
		docs = append(docs, map[string]any{
			"id":   webProcessResourceID(base, instanceID, proc.PID) + "/modules/" + address,
			"name": name,
			"type": "Microsoft.Web/sites/processes/modules",
			"properties": map[string]any{
				"base_address":       address,
				"file_name":          name,
				"file_path":          module.Path,
				"href":               webProcessResourceID(base, instanceID, proc.PID) + "/modules/" + address,
				"module_memory_size": module.Size,
			},
		})
	}
	return docs
}

// The engine reports processes in its own host's PID namespace, so /proc holds
// them only where the simulator shares that kernel; an engine in a virtual
// machine, as on every macOS host, leaves no mapping table to read.
func webProcessModulesUnreadable(w http.ResponseWriter, operation string) {
	AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
		"%s is not implemented by the simulator: a process's loaded modules and "+
			"their base addresses are read from its own /proc/<pid>/maps, and this "+
			"host does not share a kernel with the container engine, so the site's "+
			"processes are not in its /proc.", operation)
}
