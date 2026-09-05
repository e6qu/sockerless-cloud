package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// App Service instances and processes, read from the site's live workload
// container.
//
// A container-image App Service site runs as a real container in this
// simulator, so the platform data these operations report already exists and
// is read rather than synthesized:
//
//   - The running workload container is the site instance. Its identifier is
//     the instance name, its state is the instance's runtime state, and the
//     engine's resource-usage sample is the instance's `containers` statistics
//     — the WebSiteInstanceStatus `containers` member is a dictionary of
//     ContainerInfo whose members (currentCpuStats, previousCpuStats,
//     memoryStats, eth0) are the container engine's own statistics document,
//     member for member.
//   - The engine's process table for that container is the site's process
//     list. It is read through the engine's `GET /containers/{id}/top` API,
//     which runs `ps` for the container's processes, so every process, its
//     command line, owner, parent, resident and virtual memory, thread count,
//     elapsed and CPU time is the real value for a real process.
//
// A site whose container is not running has no instances and no processes, and
// answers with an empty collection — the same answer Azure gives for a stopped
// site.
//
// Members of ProcessInfo, ProcessThreadInfo and WebSiteInstanceStatus that the
// container engine's API does not report are omitted rather than filled with
// invented values.
//
// The engine exposes exactly one process-inspection primitive over its HTTP
// API — `GET /containers/{id}/top`, the `ps` output for the container's
// processes. It reports no loaded shared objects, no open file handles, and no
// per-process memory peaks, and there is no other engine API that does.
// Reading them would need `/proc/<pid>/maps`, `/proc/<pid>/fd` and
// `/proc/<pid>/status` from inside the container, which requires either a
// shell in the workload image (a scratch image has none) or the engine host's
// own `/proc` (unreachable when the engine runs in a virtual machine). The
// module, dump and open-handle families of operations are therefore not served
// at all rather than served with fabricated values, and the members they would
// populate are absent from ProcessInfo. The same limit stops DeleteProcess:
// the engine can signal a container's main process, not an arbitrary process
// inside it.

// webProcess is one row of the engine's process table for a site container,
// merged from the two `ps` projections the engine is asked for: the default
// projection, whose trailing command column carries the whole command line
// with its spaces intact, and an all-numeric projection that carries the
// memory, thread and timing columns.
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

// webThread is one row of the engine's thread-level process table.
type webThread struct {
	PID         int
	TID         int
	State       string
	CPUTime     string
	Priority    int
	ElapsedSecs int64
}

// webSiteInstanceContainer resolves the live workload container backing a site
// or slot. The second result is false when the site has no running container,
// which is the state a stopped site is in.
func webSiteInstanceContainer(site Site) (string, bool) {
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	id := inst.containerID
	inst.mu.Unlock()
	if id == "" || !sim.ContainerRunning(id) {
		return "", false
	}
	return id, true
}

// webContainerTop reads one `ps` projection for a container through the
// engine's process-table API and returns it keyed by column title.
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

// webTopColumn reads a named column out of one process-table row. Engines
// spell the same column differently (`UID` and `USER`, `CMD` and `COMMAND`),
// so a caller names every spelling it accepts.
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

// webProcessTable reads the site container's processes. Two projections are
// requested: the engine's default one, whose trailing command column is parsed
// as a whole so a command line keeps its spaces, and an all-numeric one that
// carries the columns the default projection does not.
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

// webThreadTable reads the site container's threads, one row per thread.
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

// webContainerStats reads one resource-usage sample for a container, the
// document that fills the instance's `containers` statistics.
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

// wire shapes

func webProcessResourceID(base string, instanceID string, pid int) string {
	if instanceID != "" {
		return fmt.Sprintf("%s/instances/%s/processes/%d", base, instanceID, pid)
	}
	return fmt.Sprintf("%s/processes/%d", base, pid)
}

// webProcessDoc renders one ProcessInfo. Only members the engine's process
// table reports are present; see webProcessUnservedNote for the rest.
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

// webProcessFileName is the executable the process runs: the first token of
// the command line the engine reports.
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

// webContainerInfoDoc maps the engine's resource-usage sample onto the
// ContainerInfo the WebSiteInstanceStatus `containers` dictionary holds. Every
// member is the engine's own counter.
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

// webInstanceDoc renders one WebSiteInstanceStatus. The URL members Azure
// fills with Kudu links (statusUrl, consoleUrl, detectorUrl, healthCheckUrl)
// are absent: this simulator serves no Kudu status, console or detector
// endpoint, and a link to something nothing answers would be a fabrication.
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

// handlers

// webEngineFailure reports a container-engine read that did not succeed. The
// operation cannot answer without the engine, and answering with an empty
// collection would claim the site has no processes when the truth is unknown.
func webEngineFailure(w http.ResponseWriter, err error) {
	AzureError(w, "InternalServerError",
		"Could not read the site instance from the container engine: "+err.Error(),
		http.StatusInternalServerError)
}

// registerWebProcesses mounts WebApps_ListInstanceIdentifiers[Slot],
// WebApps_GetInstanceInfo[Slot], WebApps_ListProcesses[Slot],
// WebApps_GetProcess[Slot], WebApps_ListProcessThreads[Slot] and their
// per-instance spellings.
func registerWebProcesses(both func(string, string, http.HandlerFunc)) {
	// WebApps_ListInstanceIdentifiers[Slot] — the site's running instances.
	both("GET", "/instances", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		base := webResourceID(r)
		values := []map[string]any{}
		if containerID, ok := webSiteInstanceContainer(site); ok {
			values = append(values, webInstanceDoc(base, containerID, true, nil))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": values})
	})

	// WebApps_GetInstanceInfo[Slot] — one instance, carrying the container
	// engine's live resource-usage statistics.
	both("GET", "/instances/{instanceId}", func(w http.ResponseWriter, r *http.Request) {
		containerID, ok := webRequireInstance(w, r)
		if !ok {
			return
		}
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

	// WebApps_ListProcesses[Slot] and WebApps_ListInstanceProcesses[Slot].
	both("GET", "/processes", webListProcesses)
	both("GET", "/instances/{instanceId}/processes", webListProcesses)

	// WebApps_GetProcess[Slot] and WebApps_GetInstanceProcess[Slot].
	both("GET", "/processes/{processId}", webGetProcess)
	both("GET", "/instances/{instanceId}/processes/{processId}", webGetProcess)

	// WebApps_ListProcessThreads[Slot] and
	// WebApps_ListInstanceProcessThreads[Slot].
	both("GET", "/processes/{processId}/threads", webListProcessThreads)
	both("GET", "/instances/{instanceId}/processes/{processId}/threads", webListProcessThreads)

	// Killing a process really kills it in the site's container, so the
	// process table the read beside this returns no longer has it.
	both("DELETE", "/processes/{processId}", webDeleteProcess)
	both("DELETE", "/instances/{instanceId}/processes/{processId}", webDeleteProcess)

	// The modules a process has loaded, read from the process itself.
	both("GET", "/processes/{processId}/modules", webListProcessModules)
	both("GET", "/instances/{instanceId}/processes/{processId}/modules", webListProcessModules)
	both("GET", "/processes/{processId}/modules/{baseAddress}", webGetProcessModule)
	both("GET", "/instances/{instanceId}/processes/{processId}/modules/{baseAddress}", webGetProcessModule)
}

// webRequireInstance resolves the site's live container and checks it against
// the instance the request addresses, writing the ARM 404 when either the site
// or the instance is not there.
func webRequireInstance(w http.ResponseWriter, r *http.Request) (string, bool) {
	containerID, running, ok := webResolveInstance(w, r)
	if !ok {
		return "", false
	}
	if !running {
		webInstanceNotFound(w, r)
		return "", false
	}
	return containerID, true
}

// webResolveInstance resolves the site addressed by the request and its live
// workload container. The second result is false when the site has no running
// container, or when the request names an instance that is not the running
// one; the third is false only when the site itself does not exist, in which
// case the ARM 404 has already been written.
func webResolveInstance(w http.ResponseWriter, r *http.Request) (string, bool, bool) {
	if webMissing(w, r) {
		return "", false, false
	}
	site, _ := webResource(r)
	containerID, running := webSiteInstanceContainer(site)
	if requested := sim.PathParam(r, "instanceId"); requested != "" && requested != containerID {
		return "", false, true
	}
	return containerID, running, true
}

func webInstanceNotFound(w http.ResponseWriter, r *http.Request) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/sites/%s/instances/%s' was not found.",
		sim.PathParam(r, "siteName"), sim.PathParam(r, "instanceId"))
}

// webSiteProcesses resolves the site's instance and reads its process table.
// A site whose container is not running has an empty process table rather than
// an error, which is what a collection read of a stopped site answers.
func webSiteProcesses(w http.ResponseWriter, r *http.Request) (string, []webProcess, bool) {
	containerID, running, ok := webResolveInstance(w, r)
	if !ok {
		return "", nil, false
	}
	if !running {
		return "", nil, true
	}
	procs, err := webProcessTable(containerID)
	if err != nil {
		webEngineFailure(w, err)
		return "", nil, false
	}
	return containerID, procs, true
}

// webInstanceSegment is the instance identifier the request addressed, or ""
// for the site-scoped spelling, so the resource IDs a response carries match
// the path the client called.
func webInstanceSegment(r *http.Request) string { return sim.PathParam(r, "instanceId") }

func webListProcesses(w http.ResponseWriter, r *http.Request) {
	containerID, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if containerID == "" && webInstanceSegment(r) != "" {
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
	containerID, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if containerID == "" && webInstanceSegment(r) != "" {
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
	containerID, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if containerID == "" && webInstanceSegment(r) != "" {
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
	threads, err := webThreadTable(containerID)
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

// webDeleteProcess kills a process in the site's container. It is a real kill:
// the process table the reads beside it return comes from the container, so a
// process killed here is gone from them afterwards.
func webDeleteProcess(w http.ResponseWriter, r *http.Request) {
	containerID, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return
	}
	if containerID == "" {
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
	if err := webKillContainerProcess(containerID, pid); err != nil {
		webEngineFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// webKillContainerProcess sends the process a TERM inside the container it runs
// in. The engine has no kill-one-process call, so it is done the way an
// operator would: by running kill in the container.
func webKillContainerProcess(containerID string, pid int) error {
	cli := sim.DockerClient()
	if cli == nil {
		return fmt.Errorf("no container engine is available to signal the site's process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	created, err := cli.ExecCreate(ctx, containerID, mobyclient.ExecCreateOptions{
		Cmd: []string{"kill", "-TERM", strconv.Itoa(pid)}, AttachStderr: true,
	})
	if err != nil {
		return err
	}
	attached, err := cli.ExecAttach(ctx, created.ID, mobyclient.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer attached.Close()
	_, _ = io.Copy(io.Discard, attached.Reader)
	return nil
}

// webListProcessModules reports the modules a process has loaded.
func webListProcessModules(w http.ResponseWriter, r *http.Request) {
	proc, _, ok := webResolveProcess(w, r)
	if !ok {
		return
	}
	modules, readable := webProcessModules(proc)
	if !readable {
		webProcessModulesUnreadable(w, "WebApps_ListProcessModules")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": webProcessModuleDocs(webResourceID(r), webInstanceSegment(r), proc, modules),
	})
}

// webGetProcessModule reports one module by the address it is mapped at.
func webGetProcessModule(w http.ResponseWriter, r *http.Request) {
	proc, _, ok := webResolveProcess(w, r)
	if !ok {
		return
	}
	modules, readable := webProcessModules(proc)
	if !readable {
		webProcessModulesUnreadable(w, "WebApps_GetProcessModule")
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

// webResolveProcess finds the process a request names in the site's container.
func webResolveProcess(w http.ResponseWriter, r *http.Request) (webProcess, string, bool) {
	containerID, procs, ok := webSiteProcesses(w, r)
	if !ok {
		return webProcess{}, "", false
	}
	if containerID == "" {
		webInstanceNotFound(w, r)
		return webProcess{}, "", false
	}
	pid, err := strconv.Atoi(sim.PathParam(r, "processId"))
	if err != nil {
		AzureError(w, "BadRequest", "The process identifier must be a number.", http.StatusBadRequest)
		return webProcess{}, "", false
	}
	for _, proc := range procs {
		if proc.PID == pid {
			return proc, containerID, true
		}
	}
	webProcessNotFound(w, r, pid)
	return webProcess{}, "", false
}

// webProcessModuleDocs renders the modules of a process. The executable a
// process is running is the one module every process has, and it is the one the
// container can name — the rest are shared objects the kernel maps, which this
// engine's process table does not expose.
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

// webProcessModulesUnreadable answers the module reads on a host that cannot
// see the site's processes. The engine reports them in its own host's PID
// namespace, so /proc holds them only where the simulator shares that kernel;
// where it does not — the engine in a virtual machine, which is every macOS
// host — there is no mapping table to read and no honest answer but this one.
func webProcessModulesUnreadable(w http.ResponseWriter, operation string) {
	AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
		"%s is not implemented by the simulator: a process's loaded modules and "+
			"their base addresses are read from its own /proc/<pid>/maps, and this "+
			"host does not share a kernel with the container engine, so the site's "+
			"processes are not in its /proc.", operation)
}
