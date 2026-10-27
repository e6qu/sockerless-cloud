package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// WebJobs have no PUT: a deployment that lands files under
// App_Data/jobs/{triggered,continuous}/<name>/ creates them, where the
// platform discovers them. A run executes the job's run command in a
// container of the site's own image with the site's app settings.

const webJobsRoot = "App_Data/jobs/"

const webJobsContainerRoot = "/home/site/wwwroot/" + webJobsRoot

type WebJobRecord struct {
	ID         string `json:"id"`
	SiteID     string `json:"siteId"`
	Name       string `json:"name"`
	JobKind    string `json:"jobKind"`
	RunCommand string `json:"runCommand"`
	// Status, DetailedStatus and Error apply to continuous jobs.
	Status         string `json:"status,omitempty"`
	DetailedStatus string `json:"detailedStatus,omitempty"`
	Error          string `json:"error,omitempty"`
	// ScheduleError says why the scheduler does not run Schedule.
	Schedule      string `json:"schedule,omitempty"`
	ScheduleError string `json:"scheduleError,omitempty"`
}

func webJobError(rec WebJobRecord) string {
	if rec.ScheduleError != "" {
		return rec.ScheduleError
	}
	return rec.Error
}

var webWebJobs sim.Store[WebJobRecord]

type WebJobRunRecord struct {
	ID        string `json:"id"`
	JobID     string `json:"jobId"`
	RunID     string `json:"runId"`
	SiteID    string `json:"siteId"`
	JobName   string `json:"jobName"`
	Status    string `json:"status"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime,omitempty"`
	Duration  string `json:"duration,omitempty"`
	// Kudu records "External - " and the caller's user agent for a run requested
	// over the SCM site.
	Trigger string `json:"trigger,omitempty"`
}

var webJobRuns sim.Store[WebJobRunRecord]

// A deliberate kill pops the entry first, so an exit watcher whose handle is
// no longer tracked leaves the durable status to the caller that popped it.
var webJobContainers = struct {
	sync.Mutex
	m map[string]placement.Handle
}{m: map[string]placement.Handle{}}

// A continuous job persisted as Running lost its container with the previous
// process, which the platform reports as PendingRestart.
func initWebJobStores(srv *sim.Server) {
	webWebJobs = sim.MakeStore[WebJobRecord](srv.DB(), "web_webjobs")
	webJobRuns = sim.MakeStore[WebJobRunRecord](srv.DB(), "web_webjob_runs")
	startWebJobSchedules(srv)
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

// Continuous jobs auto-start unless the site is stopped or sets
// WEBJOBS_STOPPED=1.
func webDiscoverWebJobs(resID string) error {
	type found struct{ kind, name, runCmd string }
	jobs := map[string]found{}
	paths, err := webSiteContentPaths(resID, webJobsRoot)
	if err != nil {
		return err
	}
	site, _ := webJobSite(resID)
	for _, p := range paths {
		rest, ok := strings.CutPrefix(p, webJobsRoot)
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
		// The Linux platform prefers run.sh, else the first run.* alphabetically.
		if !seen || file == "run.sh" || (cur.runCmd != "run.sh" && file < cur.runCmd) {
			jobs[key] = found{kind: kind, name: name, runCmd: file}
		}
	}
	// A deployment never removes a job, since Web Deploy does not delete, but a
	// backup, snapshot or deleted-app restore replaces the whole file system and
	// can take a job away.
	present := map[string]bool{}
	for key := range jobs {
		kind, name, _ := strings.Cut(key, "/")
		present[resID+"/"+kind+"webjobs/"+name] = true
	}
	jobPrefix := resID + "/"
	for _, rec := range slices.Clone(webWebJobsBySite.LookupAll(webWebJobs, resID,
		func(rec WebJobRecord) []string { return []string{rec.SiteID} })) {
		if present[rec.ID] || !strings.HasPrefix(rec.ID, jobPrefix) {
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
		if j.kind == "continuous" && !webJobsStopped(resID) && !siteStopped(&site) {
			rec, _ := webWebJobs.Get(id)
			webStartContinuousWebJob(rec)
		}
	}
	return nil
}

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

func webJobSite(resID string) (Site, bool) {
	if strings.Contains(resID, "/slots/") {
		return webSlots.Get(resID)
	}
	return azfSites.Get(resID)
}

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

// Pop before cancelling so the exit watcher finds its key no longer tracked
// and leaves the durable status to this caller.
func webKillWebJobContainer(key string) {
	webJobContainers.Lock()
	handle := webJobContainers.m[key]
	delete(webJobContainers.m, key)
	webJobContainers.Unlock()
	if handle != nil {
		handle.Cancel()
	}
}

func materializeWebJobDir(resID, kind, name string) (string, error) {
	jobPrefix := webJobsRoot + kind + "/" + name + "/"
	files, err := webReadSiteContent(resID, jobPrefix)
	if err != nil {
		return "", err
	}
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

func startWebJobProcess(site *Site, rec WebJobRecord, extraEnv map[string]string, outRel, errRel string) (placement.Handle, error) {
	image := siteContainerImage(site)
	if platformImage, ok := sitePlatformImage(site); ok {
		image = platformImage
	}
	if image == "" {
		return nil, fmt.Errorf("webjob %q: %w", rec.Name, siteImageMissing(site))
	}
	localImage := sim.ResolveLocalImage(image)
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()
	platform, err := workload.LocalImagePlatform(ctx, localImage, "")
	if err != nil {
		return nil, err
	}
	hostDir, err := materializeWebJobDir(rec.SiteID, rec.JobKind, rec.Name)
	if err != nil {
		return nil, err
	}
	jobDir := webJobsContainerRoot + rec.JobKind + "/" + rec.Name
	// A shell-script run file goes through sh; anything else runs directly.
	runInvocation := "./" + rec.RunCommand
	if strings.HasSuffix(rec.RunCommand, ".sh") {
		runInvocation = "sh ./" + rec.RunCommand
	}
	// Kudu appends a run's arguments to the run file's command line.
	if args := extraEnv["WEBJOBS_COMMAND_ARGUMENTS"]; args != "" {
		runInvocation += " " + args
	}
	network, err := azurePlatformNetwork(ctx)
	if err != nil {
		return nil, err
	}
	metadataEnv, err := hostMetadataEnv(site, network.DNS)
	if err != nil {
		return nil, err
	}
	env := workloadhost.MergeEnv(webResolvedAppSettings(site), siteConnectionStringEnv(site), metadataEnv, map[string]string{
		"WEBJOBS_NAME": rec.Name,
		"WEBJOBS_TYPE": rec.JobKind,
		"WEBJOBS_PATH": jobDir,
	}, extraEnv)
	sink := webJobLogSink{site: site, container: newFuncLogSink(site), outRel: outRel, errRel: errRel}
	backend, err := azurePlacementBackend(placementWeb)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("sockerless-sim-azure-webjob-%s-%s-%s", siteStorageName(site.Name), rec.Name, randomSuffix(6))
	address, release, err := azurePlatformAddress(ctx, network, name)
	if err != nil {
		return nil, err
	}
	handle, err := backend.Place(ctx, sim.ContainerConfig{
		CancelGracePeriod: siteStopGrace(site),
		Image:             localImage,
		Architecture:      platform,
		Command:           []string{"/bin/sh", "-c", "cd " + jobDir + " && " + runInvocation},
		Env:               env,
		Binds:             append([]string{hostDir + ":" + jobDir}, siteAzureStorageBinds(site)...),
		Name:              name,
		Labels: map[string]string{
			"sockerless-sim-type": "azure-webjob",
			"sockerless-site":     site.Name,
		},
		Network:     network.Network,
		IPv4Address: address,
		DNS:         network.resolvers(),
		ExtraHosts:  workloadhost.ExtraHosts(),
		Sandbox:     SandboxAZF,
	}, sink)
	if err != nil {
		release()
		return nil, err
	}
	azureReleaseOnExit(handle.Exited(), release)
	return handle, nil
}

func webRunTriggeredWebJob(site *Site, rec WebJobRecord, trigger, arguments string) string {
	runID := sim.NewUUID()
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
		Trigger:   trigger,
	}
	webJobRuns.Put(runRecID, run)
	env := map[string]string{"WEBJOBS_RUN_ID": runID}
	if arguments != "" {
		env["WEBJOBS_COMMAND_ARGUMENTS"] = arguments
	}
	outRel := webJobRunLogRel(rec.Name, runID, "output_log.txt")
	webJobAppendLog(site, outRel, "SYS INFO", "Status changed to Initializing")
	handle, err := startWebJobProcess(site, rec, env, outRel, webJobRunLogRel(rec.Name, runID, "error_log.txt"))
	if err != nil {
		webJobAppendLog(site, outRel, "SYS ERR ", err.Error())
		webJobAppendLog(site, outRel, "SYS INFO", "Status changed to Failed")
		webJobRuns.Update(runRecID, func(row *WebJobRunRecord) {
			row.Status = "Error"
			row.EndTime = time.Now().UTC().Format(time.RFC3339)
			row.Duration = time.Since(now).String()
		})
		webWebJobs.Update(rec.ID, func(row *WebJobRecord) { row.Error = err.Error() })
		return runID
	}
	webJobContainers.Lock()
	webJobContainers.m[runRecID] = handle
	webJobContainers.Unlock()
	webJobAppendLog(site, outRel, "SYS INFO", "Status changed to Running")
	var result sim.ProcessResult
	bg.WatchThen(func() { result = handle.Wait() }, func() {
		webJobContainers.Lock()
		if webJobContainers.m[runRecID] != handle {
			// Deliberately killed; the record is gone.
			webJobContainers.Unlock()
			return
		}
		delete(webJobContainers.m, runRecID)
		webJobContainers.Unlock()
		status := "Success"
		if result.Error != nil || result.ExitCode != 0 {
			status = "Failed"
			webJobAppendLog(site, outRel, "SYS ERR ", fmt.Sprintf("Job failed due to exit code %d", result.ExitCode))
		}
		webJobAppendLog(site, outRel, "SYS INFO", "Status changed to "+status)
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
	})
	return runID
}

// The status is Running while the process lives, PendingRestart when it
// exits on its own, and Stopped when an explicit stop killed it.
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
	logRel := webContinuousJobLogRel(rec.Name)
	webJobAppendLog(&site, logRel, "SYS INFO", "Status changed to Starting")
	handle, err := startWebJobProcess(&site, rec, nil, logRel, "")
	if err != nil {
		webJobAppendLog(&site, logRel, "SYS ERR ", err.Error())
		webJobAppendLog(&site, logRel, "SYS INFO", "Status changed to Stopped")
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
	webJobAppendLog(&site, logRel, "SYS INFO", "Status changed to Running")
	var result sim.ProcessResult
	bg.WatchThen(func() { result = handle.Wait() }, func() {
		webJobContainers.Lock()
		// A Stop or Delete pops the entry, maybe followed by a Start tracking a fresh
		// handle, so only the watcher of the tracked container may touch the entry.
		if webJobContainers.m[rec.ID] != handle {
			webJobContainers.Unlock()
			return
		}
		delete(webJobContainers.m, rec.ID)
		webJobContainers.Unlock()
		webJobAppendLog(&site, logRel, "SYS INFO", "Status changed to PendingRestart")
		webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
			row.Status = "PendingRestart"
			row.DetailedStatus = fmt.Sprintf("The job's process exited with code %d.", result.ExitCode)
		})
	})
}

// Microsoft.Web spells a site child "<site>/<child>", and
// "<site>/<slot>/<child>" under a slot.
func webJobSiteScopedName(resID, child string) string {
	name := resID[strings.LastIndex(resID, "/sites/")+len("/sites/"):]
	name = strings.Replace(name, "/slots/", "/", 1)
	return name + "/" + child
}

// A run recorded without a trigger was requested through Azure Resource
// Manager.
func webJobRunTrigger(run WebJobRunRecord) string {
	if run.Trigger == "" {
		return "External - ARM"
	}
	return run.Trigger
}

func webJobScmBase(r *http.Request, rec WebJobRecord) string {
	site, _ := webJobSite(rec.SiteID)
	return azureRequestScheme(r) + "://" + siteScmHost(&site)
}

func triggeredJobRunWire(scm string, run WebJobRunRecord) map[string]any {
	out := map[string]any{
		"web_job_id":   run.RunID,
		"web_job_name": run.JobName,
		"job_name":     run.JobName,
		"status":       run.Status,
		"start_time":   run.StartTime,
		"trigger":      webJobRunTrigger(run),
		"url":          scm + "/api/triggeredwebjobs/" + run.JobName + "/history/" + run.RunID,
	}
	webJobRunLogURLs(out, scm, run)
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
	if msg := webJobError(rec); msg != "" {
		props["error"] = msg
	}
	return map[string]any{
		"id":         rec.SiteID + "/webjobs/" + rec.Name,
		"name":       webJobSiteScopedName(rec.SiteID, rec.Name),
		"type":       "Microsoft.Web/sites/webjobs",
		"properties": props,
	}
}

func triggeredWebJobWire(r *http.Request, rec WebJobRecord) map[string]any {
	props := map[string]any{
		"run_command":  rec.RunCommand,
		"web_job_type": "Triggered",
		"using_sdk":    false,
		"url":          webJobScmBase(r, rec) + "/api/triggeredwebjobs/" + rec.Name,
		"history_url":  webJobScmBase(r, rec) + "/api/triggeredwebjobs/" + rec.Name + "/history",
		"settings":     kuduWebJobSettings(rec),
	}
	webJobSchedulerLogURL(props, webJobScmBase(r, rec), rec)
	if msg := webJobError(rec); msg != "" {
		props["error"] = msg
	}
	if latest, ok := webLatestRun(rec.ID); ok {
		props["latest_run"] = triggeredJobRunWire(webJobScmBase(r, rec), latest)
	}
	return map[string]any{
		"id":         rec.ID,
		"name":       webJobSiteScopedName(rec.SiteID, rec.Name),
		"type":       "Microsoft.Web/sites/triggeredwebjobs",
		"properties": props,
	}
}

func continuousWebJobWire(r *http.Request, rec WebJobRecord) map[string]any {
	status := rec.Status
	if status == "" {
		status = "Stopped"
	}
	props := map[string]any{
		"run_command":  rec.RunCommand,
		"web_job_type": "Continuous",
		"using_sdk":    false,
		"status":       status,
		"url":          webJobScmBase(r, rec) + "/api/continuouswebjobs/" + rec.Name,
		"settings":     kuduWebJobSettings(rec),
	}
	webContinuousJobLogURL(props, webJobScmBase(r, rec), rec)
	if rec.DetailedStatus != "" {
		props["detailed_status"] = rec.DetailedStatus
	}
	if msg := webJobError(rec); msg != "" {
		props["error"] = msg
	}
	return map[string]any{
		"id":         rec.ID,
		"name":       webJobSiteScopedName(rec.SiteID, rec.Name),
		"type":       "Microsoft.Web/sites/continuouswebjobs",
		"properties": props,
	}
}

func triggeredJobHistoryWire(scm string, runs []WebJobRunRecord) []any {
	out := make([]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, map[string]any{
			"id":         run.ID,
			"name":       run.RunID,
			"type":       "Microsoft.Web/sites/triggeredwebjobs/history",
			"properties": map[string]any{"runs": []any{triggeredJobRunWire(scm, run)}},
		})
	}
	return out
}

func webLatestRun(jobID string) (WebJobRunRecord, bool) {
	runs := webJobRunsFor(jobID)
	if len(runs) == 0 {
		return WebJobRunRecord{}, false
	}
	return runs[0], true
}

var (
	webWebJobsBySite sim.GenerationIndex[WebJobRecord]
	webJobRunsByJob  sim.GenerationIndex[WebJobRunRecord]
)

func webJobRunsFor(jobID string) []WebJobRunRecord {
	runs := slices.Clone(webJobRunsByJob.LookupAll(webJobRuns, jobID,
		func(run WebJobRunRecord) []string { return []string{webChildParent(run.ID, "/history/")} }))
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

	both("GET", "/triggeredwebjobs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		out := make([]any, 0)
		for _, rec := range listJobs(r, "triggered") {
			out = append(out, triggeredWebJobWire(r, rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/triggeredwebjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "triggered")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, triggeredWebJobWire(r, rec))
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
		webRunTriggeredWebJob(&site, rec, "External - ARM", "")
		w.WriteHeader(http.StatusOK)
	})
	both("GET", "/triggeredwebjobs/{webJobName}/history", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "triggered")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": triggeredJobHistoryWire(webJobScmBase(r, rec), webJobRunsFor(rec.ID)),
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
			"properties": map[string]any{"runs": []any{triggeredJobRunWire(webJobScmBase(r, rec), run)}},
		})
	})

	both("GET", "/continuouswebjobs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		out := make([]any, 0)
		for _, rec := range listJobs(r, "continuous") {
			out = append(out, continuousWebJobWire(r, rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/continuouswebjobs/{webJobName}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := getJob(w, r, "continuous")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, continuousWebJobWire(r, rec))
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
