package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_webjobs.go implements the App Service WebJobs slice of the
// Microsoft.Web ARM surface: the combined webjobs read view, triggered
// webjobs (get/list/delete/run + run history) and continuous webjobs
// (get/list/delete/start/stop), on production sites and deployment slots.
//
// WebJobs are Kudu-deployed artifacts, not ARM-created resources: the ARM API
// has no PUT for them. They come into existence when a deployment package
// (WebApps_CreateMSDeployOperation / WebApps_CreateOneDeployOperation, in
// web_deploy_extras.go) lands files under App_Data/jobs/{triggered,continuous}/
// <name>/ — exactly where the real platform discovers them. Runs are real: a
// triggered run and a continuous start execute the job's run command inside a
// container of the site's own image with the site's app settings, and the run
// history records the container's actual exit status and timing.

// webJobsRoot is the wwwroot-relative directory the platform scans for jobs.
const webJobsRoot = "App_Data/jobs/"

// webJobsContainerRoot is where the job's artifact directory is mounted inside
// the run container — the real App Service path of a deployed webjob.
const webJobsContainerRoot = "/home/site/wwwroot/" + webJobsRoot

// WebJobRecord is one discovered webjob. ID is the job's canonical ARM
// resource ID (<site-or-slot>/triggeredwebjobs/<name> or
// .../continuouswebjobs/<name>).
type WebJobRecord struct {
	ID         string `json:"id"`
	SiteID     string `json:"siteId"`
	Name       string `json:"name"`
	JobKind    string `json:"jobKind"` // "triggered" | "continuous"
	RunCommand string `json:"runCommand"`
	// Status / DetailedStatus / Error apply to continuous jobs
	// (ContinuousWebJobStatus: Initializing/Starting/Running/PendingRestart/
	// Stopped).
	Status         string `json:"status,omitempty"`
	DetailedStatus string `json:"detailedStatus,omitempty"`
	Error          string `json:"error,omitempty"`
}

var webWebJobs sim.Store[WebJobRecord]

// WebJobRunRecord is one triggered webjob run — the real container execution
// behind WebApps_RunTriggeredWebJob, recorded with the container's actual
// start/end time and exit-derived status.
type WebJobRunRecord struct {
	ID        string `json:"id"` // <jobID>/history/<runId>
	JobID     string `json:"jobId"`
	RunID     string `json:"runId"`
	SiteID    string `json:"siteId"`
	JobName   string `json:"jobName"`
	Status    string `json:"status"` // TriggeredWebJobStatus: Running/Success/Failed/Error/Aborted
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

var webJobRuns sim.Store[WebJobRunRecord]

// webJobContainers tracks the live container of each running webjob (keyed by
// the job ARM ID for continuous jobs, by the run record ID for triggered
// runs). Containers are process state, not durable state — the durable status
// lives on the records. A deliberate kill (Stop / Delete / site cleanup) pops
// the entry synchronously, so an exit watcher whose handle is no longer the
// tracked one knows its container was superseded or deliberately stopped and
// leaves the durable status to the caller that popped it.
var webJobContainers = struct {
	sync.Mutex
	m map[string]*sim.ContainerHandle
}{m: map[string]*sim.ContainerHandle{}}

// initWebJobStores wires the stores and reconciles restart state: a continuous
// job persisted as Running lost its container with the previous process, which
// is exactly the state the real platform reports as PendingRestart.
func initWebJobStores(srv *sim.Server) {
	webWebJobs = sim.MakeStore[WebJobRecord](srv.DB(), "web_webjobs")
	webJobRuns = sim.MakeStore[WebJobRunRecord](srv.DB(), "web_webjob_runs")
	for _, rec := range webWebJobs.List() {
		if rec.JobKind == "continuous" && (rec.Status == "Running" || rec.Status == "Starting" || rec.Status == "Initializing") {
			webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
				row.Status = "PendingRestart"
				row.DetailedStatus = "The job's process exited with the simulator restart and is awaiting a start."
			})
		}
	}
	for _, run := range webJobRuns.List() {
		if run.Status == "Running" {
			webJobRuns.Update(run.ID, func(row *WebJobRunRecord) {
				row.Status = "Aborted"
				row.EndTime = time.Now().UTC().Format(time.RFC3339)
			})
		}
	}
}

// webDiscoverWebJobs scans a site's deployed content for
// App_Data/jobs/{triggered,continuous}/<name>/ directories and upserts a
// WebJobRecord per job found — the discovery the real platform performs after
// a deployment. Continuous jobs auto-start exactly as the platform does,
// unless the site carries the real WEBJOBS_STOPPED=1 app setting.
func webDiscoverWebJobs(resID string) {
	type found struct{ kind, name, runCmd string }
	jobs := map[string]found{}
	prefix := resID + "|"
	for _, f := range webSiteContent.Filter(func(f WebSiteContentFile) bool { return strings.HasPrefix(f.ID, prefix) }) {
		rest, ok := strings.CutPrefix(f.Path, webJobsRoot)
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) != 3 || (parts[0] != "triggered" && parts[0] != "continuous") {
			continue
		}
		kind, name, file := parts[0], parts[1], parts[2]
		if strings.Contains(file, "/") || !strings.HasPrefix(file, "run.") {
			continue
		}
		key := kind + "/" + name
		cur, seen := jobs[key]
		// run.sh is the run file the Linux platform prefers; otherwise the
		// first run.* alphabetically.
		if !seen || file == "run.sh" || (cur.runCmd != "run.sh" && file < cur.runCmd) {
			jobs[key] = found{kind: kind, name: name, runCmd: file}
		}
	}
	// A webjob exists exactly while its App_Data/jobs script does, so a job
	// whose directory has left the file system is gone: its container is
	// killed and its record and run history removed. A deployment never
	// triggers this — Web Deploy creates and overwrites but does not delete —
	// but a backup, snapshot or deleted-app restore replaces the whole file
	// system and can take a job away with it.
	present := map[string]bool{}
	for key := range jobs {
		kind, name, _ := strings.Cut(key, "/")
		present[resID+"/"+kind+"webjobs/"+name] = true
	}
	jobPrefix := resID + "/"
	for _, rec := range webWebJobs.Filter(func(rec WebJobRecord) bool {
		return strings.HasPrefix(rec.ID, jobPrefix) && rec.SiteID == resID
	}) {
		if present[rec.ID] {
			continue
		}
		webKillWebJobContainer(rec.ID)
		for _, run := range webJobRunsFor(rec.ID) {
			webKillWebJobContainer(run.ID)
			webJobRuns.Delete(run.ID)
		}
		webWebJobs.Delete(rec.ID)
	}
	for _, j := range jobs {
		id := resID + "/" + j.kind + "webjobs/" + j.name
		existing, ok := webWebJobs.Get(id)
		if ok {
			webWebJobs.Update(id, func(row *WebJobRecord) { row.RunCommand = j.runCmd })
			if j.kind == "continuous" && existing.Status == "Running" {
				continue
			}
		} else {
			rec := WebJobRecord{ID: id, SiteID: resID, Name: j.name, JobKind: j.kind, RunCommand: j.runCmd}
			if j.kind == "continuous" {
				rec.Status = "Stopped"
			}
			webWebJobs.Put(id, rec)
		}
		if j.kind == "continuous" && !webJobsStopped(resID) {
			rec, _ := webWebJobs.Get(id)
			webStartContinuousWebJob(rec)
		}
	}
}

// webJobsStopped reports the real WEBJOBS_STOPPED=1 App Service setting, the
// platform's own switch for keeping deployed webjobs from running.
func webJobsStopped(resID string) bool {
	cfg, _ := siteConfigStore.Get(resID)
	if cfg.AppSettings["WEBJOBS_STOPPED"] == "1" {
		return true
	}
	site, ok := webJobSite(resID)
	if !ok || site.Properties.SiteConfig == nil {
		return false
	}
	for _, kv := range site.Properties.SiteConfig.AppSettings {
		if kv.Name == "WEBJOBS_STOPPED" && kv.Value == "1" {
			return true
		}
	}
	return false
}

// webJobSite resolves a site or slot record from its canonical ARM ID.
func webJobSite(resID string) (Site, bool) {
	if strings.Contains(resID, "/slots/") {
		return webSlots.Get(resID)
	}
	return azfSites.Get(resID)
}

// webCleanupWebJobs stops and removes every webjob (and run history) stored
// under a deleted site or slot.
func webCleanupWebJobs(resID string) {
	prefix := resID + "/"
	for _, rec := range webWebJobs.Filter(func(rec WebJobRecord) bool { return strings.HasPrefix(rec.ID, prefix) }) {
		webKillWebJobContainer(rec.ID)
		webWebJobs.Delete(rec.ID)
	}
	for _, run := range webJobRuns.Filter(func(run WebJobRunRecord) bool { return strings.HasPrefix(run.ID, prefix) }) {
		webKillWebJobContainer(run.ID)
		webJobRuns.Delete(run.ID)
	}
}

// webKillWebJobContainer pops and cancels the live container tracked under a
// job or run key. Popping before cancelling makes the kill deliberate from
// the exit watcher's point of view: the watcher finds another (or no) handle
// tracked under its key and leaves the durable status to this caller.
func webKillWebJobContainer(key string) {
	webJobContainers.Lock()
	handle := webJobContainers.m[key]
	delete(webJobContainers.m, key)
	webJobContainers.Unlock()
	if handle != nil {
		handle.Cancel()
	}
}

// materializeWebJobDir writes the job's deployed artifact files to a fresh
// host directory (preserving sub-paths and file modes) for the run container
// to bind-mount at the job's real App Service path.
func materializeWebJobDir(resID, kind, name string) (string, error) {
	jobPrefix := webJobsRoot + kind + "/" + name + "/"
	files := webSiteContent.Filter(func(f WebSiteContentFile) bool {
		return strings.HasPrefix(f.ID, resID+"|") && strings.HasPrefix(f.Path, jobPrefix)
	})
	if len(files) == 0 {
		return "", fmt.Errorf("no deployed artifact files under %s", jobPrefix)
	}
	dir, err := os.MkdirTemp("", "sockerless-sim-azure-webjob-*")
	if err != nil {
		return "", err
	}
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, jobPrefix)
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		mode := os.FileMode(f.Mode) & os.ModePerm
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dst, f.Data, mode); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// startWebJobProcess launches the job's run command inside a container of the
// site's own image with the site's app settings and Azure Files mounts — the
// execution model App Service gives a webjob — and returns the live handle.
func startWebJobProcess(site *Site, rec WebJobRecord, extraEnv map[string]string) (*sim.ContainerHandle, error) {
	image := siteContainerImage(site)
	if image == "" {
		if stack := siteRuntimeStack(site); stack != "" {
			return nil, fmt.Errorf(
				"site %q is configured with the built-in runtime stack %q, and this simulator runs "+
					"container images: the platform image that stack names is Microsoft's. Configure "+
					"the site with a container image (linuxFxVersion \"DOCKER|<image>\") to run webjob %q",
				site.Name, stack, rec.Name)
		}
		return nil, fmt.Errorf("site %q has no container image (linuxFxVersion) to run webjob %q in", site.Name, rec.Name)
	}
	localImage := sim.ResolveLocalImage(image)
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()
	platform, err := localImagePlatform(ctx, localImage)
	if err != nil {
		return nil, err
	}
	hostDir, err := materializeWebJobDir(rec.SiteID, rec.JobKind, rec.Name)
	if err != nil {
		return nil, err
	}
	jobDir := webJobsContainerRoot + rec.JobKind + "/" + rec.Name
	// The platform executes the job's run file from its own directory; a
	// shell-script run file goes through sh, anything else runs directly.
	runInvocation := "./" + rec.RunCommand
	if strings.HasSuffix(rec.RunCommand, ".sh") {
		runInvocation = "sh ./" + rec.RunCommand
	}
	env := mergeEnv(siteAppSettings(site), hostMetadataEnv())
	// The real platform exposes the job's identity to the process.
	env = mergeEnv(env, map[string]string{
		"WEBJOBS_NAME": rec.Name,
		"WEBJOBS_TYPE": rec.JobKind,
		"WEBJOBS_PATH": jobDir,
	})
	env = mergeEnv(env, extraEnv)
	sink := &funcLogSink{appName: site.Name}
	return sim.StartContainerSync(sim.ContainerConfig{
		CancelGracePeriod: 5 * time.Second,
		Image:             localImage,
		Architecture:      platform,
		Command:           []string{"/bin/sh", "-c", "cd " + jobDir + " && " + runInvocation},
		Env:               env,
		Binds:             append([]string{hostDir + ":" + jobDir}, siteAzureStorageBinds(site)...),
		Name:              fmt.Sprintf("sockerless-sim-azure-webjob-%s-%s-%s", site.Name, rec.Name, randomSuffix(6)),
		Labels: map[string]string{
			"sockerless-sim-type": "azure-webjob",
			"sockerless-site":     site.Name,
		},
		ExtraHosts: hostMetadataExtraHosts(),
		Sandbox:    SandboxAZF,
	}, sink)
}

// webRunTriggeredWebJob starts one real run of a triggered webjob and records
// it in the job's history: Running at container start, then the terminal
// status the container's exit code dictates, with the actual timings.
func webRunTriggeredWebJob(site *Site, rec WebJobRecord) {
	runID := generateUUID()
	runRecID := rec.ID + "/history/" + runID
	now := time.Now().UTC()
	run := WebJobRunRecord{
		ID:        runRecID,
		JobID:     rec.ID,
		RunID:     runID,
		SiteID:    rec.SiteID,
		JobName:   rec.Name,
		Status:    "Running",
		StartTime: now.Format(time.RFC3339),
	}
	webJobRuns.Put(runRecID, run)
	handle, err := startWebJobProcess(site, rec, map[string]string{"WEBJOBS_RUN_ID": runID})
	if err != nil {
		webJobRuns.Update(runRecID, func(row *WebJobRunRecord) {
			row.Status = "Error"
			row.EndTime = time.Now().UTC().Format(time.RFC3339)
			row.Duration = time.Since(now).String()
		})
		webWebJobs.Update(rec.ID, func(row *WebJobRecord) { row.Error = err.Error() })
		return
	}
	webJobContainers.Lock()
	webJobContainers.m[runRecID] = handle
	webJobContainers.Unlock()
	go func() {
		result := handle.Wait()
		webJobContainers.Lock()
		if webJobContainers.m[runRecID] != handle {
			// Deliberately killed (job or site deleted); the record is gone.
			webJobContainers.Unlock()
			return
		}
		delete(webJobContainers.m, runRecID)
		webJobContainers.Unlock()
		status := "Success"
		if result.Error != nil || result.ExitCode != 0 {
			status = "Failed"
		}
		end := result.StoppedAt
		if end.IsZero() {
			end = time.Now().UTC()
		}
		start := result.StartedAt
		if start.IsZero() {
			start = now
		}
		webJobRuns.Update(runRecID, func(row *WebJobRunRecord) {
			row.Status = status
			row.EndTime = end.UTC().Format(time.RFC3339)
			row.Duration = end.Sub(start).String()
		})
	}()
}

// webStartContinuousWebJob starts a continuous webjob's real container,
// tracking the platform's status transitions: Running while the process
// lives, PendingRestart when it exits on its own, Stopped when an explicit
// stop killed it.
func webStartContinuousWebJob(rec WebJobRecord) {
	webJobContainers.Lock()
	if webJobContainers.m[rec.ID] != nil {
		webJobContainers.Unlock()
		return
	}
	webJobContainers.Unlock()
	site, ok := webJobSite(rec.SiteID)
	if !ok {
		return
	}
	handle, err := startWebJobProcess(&site, rec, nil)
	if err != nil {
		webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
			row.Status = "Stopped"
			row.DetailedStatus = ""
			row.Error = err.Error()
		})
		return
	}
	webJobContainers.Lock()
	webJobContainers.m[rec.ID] = handle
	webJobContainers.Unlock()
	webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
		row.Status = "Running"
		row.DetailedStatus = "Running"
		row.Error = ""
	})
	go func() {
		result := handle.Wait()
		webJobContainers.Lock()
		// A deliberate Stop/Delete pops the entry (and may be followed by a
		// new Start that tracks a fresh handle) before this watcher runs;
		// only the watcher of the currently tracked container may touch the
		// job's map entry and durable status.
		if webJobContainers.m[rec.ID] != handle {
			webJobContainers.Unlock()
			return
		}
		delete(webJobContainers.m, rec.ID)
		webJobContainers.Unlock()
		// The container exited on its own — the state the real platform
		// reports as PendingRestart.
		webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
			row.Status = "PendingRestart"
			row.DetailedStatus = fmt.Sprintf("The job's process exited with code %d.", result.ExitCode)
		})
	}()
}

// webJobSiteScopedName spells the ARM resource name of a site child the way
// Microsoft.Web does: "<site>/<child>" ("<site>/<slot>/<child>" under a slot).
func webJobSiteScopedName(resID, child string) string {
	name := resID[strings.LastIndex(resID, "/sites/")+len("/sites/"):]
	name = strings.Replace(name, "/slots/", "/", 1)
	return name + "/" + child
}

// webJobScmBase is the site's Kudu host, where the platform's own webjob URLs
// (url / history_url — external surfaces the sim does not serve) point.
func webJobScmBase(rec WebJobRecord) string {
	site, _ := webJobSite(rec.SiteID)
	return "https://" + strings.Replace(site.Properties.DefaultHostName, ".azurewebsites.net", ".scm.azurewebsites.net", 1)
}

func triggeredJobRunWire(run WebJobRunRecord) map[string]any {
	out := map[string]any{
		"web_job_id":   run.RunID,
		"web_job_name": run.JobName,
		"job_name":     run.JobName,
		"status":       run.Status,
		"start_time":   run.StartTime,
		"trigger":      "External - ARM",
	}
	if run.EndTime != "" {
		out["end_time"] = run.EndTime
	}
	if run.Duration != "" {
		out["duration"] = run.Duration
	}
	return out
}

func webJobWire(rec WebJobRecord) map[string]any {
	kind := "Triggered"
	if rec.JobKind == "continuous" {
		kind = "Continuous"
	}
	props := map[string]any{
		"run_command":  rec.RunCommand,
		"web_job_type": kind,
		"using_sdk":    false,
	}
	if rec.Error != "" {
		props["error"] = rec.Error
	}
	return map[string]any{
		"id":         rec.SiteID + "/webjobs/" + rec.Name,
		"name":       webJobSiteScopedName(rec.SiteID, rec.Name),
		"type":       "Microsoft.Web/sites/webjobs",
		"properties": props,
	}
}

func triggeredWebJobWire(rec WebJobRecord) map[string]any {
	props := map[string]any{
		"run_command":  rec.RunCommand,
		"web_job_type": "Triggered",
		"using_sdk":    false,
		// External Kudu URLs on the deployed site, emitted for shape
		// fidelity; the sim does not serve the SCM surface.
		"url":         webJobScmBase(rec) + "/api/triggeredwebjobs/" + rec.Name,
		"history_url": webJobScmBase(rec) + "/api/triggeredwebjobs/" + rec.Name + "/history",
	}
	if rec.Error != "" {
		props["error"] = rec.Error
	}
	if latest, ok := webLatestRun(rec.ID); ok {
		props["latest_run"] = triggeredJobRunWire(latest)
	}
	return map[string]any{
		"id":         rec.ID,
		"name":       webJobSiteScopedName(rec.SiteID, rec.Name),
		"type":       "Microsoft.Web/sites/triggeredwebjobs",
		"properties": props,
	}
}

func continuousWebJobWire(rec WebJobRecord) map[string]any {
	status := rec.Status
	if status == "" {
		status = "Stopped"
	}
	props := map[string]any{
		"run_command":  rec.RunCommand,
		"web_job_type": "Continuous",
		"using_sdk":    false,
		"status":       status,
		"url":          webJobScmBase(rec) + "/api/continuouswebjobs/" + rec.Name,
	}
	if rec.DetailedStatus != "" {
		props["detailed_status"] = rec.DetailedStatus
	}
	if rec.Error != "" {
		props["error"] = rec.Error
	}
	return map[string]any{
		"id":         rec.ID,
		"name":       webJobSiteScopedName(rec.SiteID, rec.Name),
		"type":       "Microsoft.Web/sites/continuouswebjobs",
		"properties": props,
	}
}

func triggeredJobHistoryWire(rec WebJobRecord, runs []WebJobRunRecord) []any {
	out := make([]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, map[string]any{
			"id":         run.ID,
			"name":       run.RunID,
			"type":       "Microsoft.Web/sites/triggeredwebjobs/history",
			"properties": map[string]any{"runs": []any{triggeredJobRunWire(run)}},
		})
	}
	return out
}

// webLatestRun returns a triggered job's most recent run.
func webLatestRun(jobID string) (WebJobRunRecord, bool) {
	runs := webJobRunsFor(jobID)
	if len(runs) == 0 {
		return WebJobRunRecord{}, false
	}
	return runs[0], true
}

// webJobRunsFor lists a triggered job's runs, newest first.
func webJobRunsFor(jobID string) []WebJobRunRecord {
	prefix := jobID + "/history/"
	runs := webJobRuns.Filter(func(run WebJobRunRecord) bool { return strings.HasPrefix(run.ID, prefix) })
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].StartTime != runs[j].StartTime {
			return runs[i].StartTime > runs[j].StartTime
		}
		return runs[i].RunID > runs[j].RunID
	})
	return runs
}

func registerWebJobHandlers(both func(string, string, http.HandlerFunc)) {
	jobID := func(r *http.Request, kind string) string {
		return webResourceID(r) + "/" + kind + "webjobs/" + sim.PathParam(r, "webJobName")
	}
	getJob := func(w http.ResponseWriter, r *http.Request, kind string) (WebJobRecord, bool) {
		if webMissing(w, r) {
			return WebJobRecord{}, false
		}
		rec, ok := webWebJobs.Get(jobID(r, kind))
		if !ok {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"WebJob %q not found.", sim.PathParam(r, "webJobName"))
			return WebJobRecord{}, false
		}
		return rec, true
	}
	listJobs := func(r *http.Request, kind string) []WebJobRecord {
		prefix := webResourceID(r) + "/"
		recs := webWebJobs.Filter(func(rec WebJobRecord) bool {
			return strings.HasPrefix(rec.ID, prefix) && (kind == "" || rec.JobKind == kind)
		})
		sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
		return recs
	}

	// GET /webjobs + /webjobs/{webJobName} — WebApps_{List,Get}WebJob[s]: the
	// combined read-only view across both kinds.
	both("GET", "/webjobs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		out := make([]any, 0)
		for _, rec := range listJobs(r, "") {
			out = append(out, webJobWire(rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/webjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		name := sim.PathParam(r, "webJobName")
		resID := webResourceID(r)
		for _, kind := range []string{"triggered", "continuous"} {
			if rec, ok := webWebJobs.Get(resID + "/" + kind + "webjobs/" + name); ok {
				sim.WriteJSON(w, http.StatusOK, webJobWire(rec))
				return
			}
		}
		AzureErrorf(w, "NotFound", http.StatusNotFound, "WebJob %q not found.", name)
	})

	// Triggered webjobs.
	both("GET", "/triggeredwebjobs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		out := make([]any, 0)
		for _, rec := range listJobs(r, "triggered") {
			out = append(out, triggeredWebJobWire(rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/triggeredwebjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "triggered")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, triggeredWebJobWire(rec))
	})
	both("DELETE", "/triggeredwebjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := jobID(r, "triggered")
		if _, ok := webWebJobs.Get(id); !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		for _, run := range webJobRunsFor(id) {
			webKillWebJobContainer(run.ID)
			webJobRuns.Delete(run.ID)
		}
		webWebJobs.Delete(id)
		w.WriteHeader(http.StatusOK)
	})
	both("POST", "/triggeredwebjobs/{webJobName}/run", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "triggered")
		if !ok {
			return
		}
		site, _ := webResource(r)
		webRunTriggeredWebJob(&site, rec)
		w.WriteHeader(http.StatusOK)
	})
	both("GET", "/triggeredwebjobs/{webJobName}/history", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "triggered")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": triggeredJobHistoryWire(rec, webJobRunsFor(rec.ID)),
		})
	})
	both("GET", "/triggeredwebjobs/{webJobName}/history/{id}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "triggered")
		if !ok {
			return
		}
		run, found := webJobRuns.Get(rec.ID + "/history/" + sim.PathParam(r, "id"))
		if !found {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"Run %q of webjob %q not found.", sim.PathParam(r, "id"), rec.Name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         run.ID,
			"name":       run.RunID,
			"type":       "Microsoft.Web/sites/triggeredwebjobs/history",
			"properties": map[string]any{"runs": []any{triggeredJobRunWire(run)}},
		})
	})

	// Continuous webjobs.
	both("GET", "/continuouswebjobs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		out := make([]any, 0)
		for _, rec := range listJobs(r, "continuous") {
			out = append(out, continuousWebJobWire(rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/continuouswebjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "continuous")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, continuousWebJobWire(rec))
	})
	both("DELETE", "/continuouswebjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := jobID(r, "continuous")
		if _, ok := webWebJobs.Get(id); !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		webKillWebJobContainer(id)
		webWebJobs.Delete(id)
		w.WriteHeader(http.StatusOK)
	})
	both("POST", "/continuouswebjobs/{webJobName}/start", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "continuous")
		if !ok {
			return
		}
		webStartContinuousWebJob(rec)
		w.WriteHeader(http.StatusOK)
	})
	both("POST", "/continuouswebjobs/{webJobName}/stop", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "continuous")
		if !ok {
			return
		}
		webKillWebJobContainer(rec.ID)
		webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
			row.Status = "Stopped"
			row.DetailedStatus = ""
		})
		w.WriteHeader(http.StatusOK)
	})
}
