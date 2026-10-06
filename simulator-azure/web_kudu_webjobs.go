package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// web_kudu_webjobs.go serves Kudu's WebJobs API on a site's SCM host, over
// the same webjob records and run history the Microsoft.Web webjob resources
// read: list, get, upload and delete of triggered and continuous jobs, a
// triggered job's run and history, a continuous job's start and stop, and a
// job's settings.job.

// kuduWebJobsMaxUpload bounds a job upload as a deployment package is bounded.
const kuduWebJobsMaxUpload = webSiteContentLimit

// kuduWebJobKinds maps the API's collection names to the job kinds.
var kuduWebJobKinds = map[string]string{
	"triggeredwebjobs":  "triggered",
	"continuouswebjobs": "continuous",
}

// serveKuduWebJobs answers a request under /api/webjobs,
// /api/triggeredwebjobs or /api/continuouswebjobs, reporting whether the path
// is one of them.
func serveKuduWebJobs(w http.ResponseWriter, r *http.Request, site *Site) bool {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "api") {
		return false
	}
	collection := strings.ToLower(parts[1])
	if collection == "webjobs" && len(parts) == 2 {
		if kuduMethod(w, r, http.MethodGet) {
			kuduListWebJobs(w, r, site, "")
		}
		return true
	}
	kind, ok := kuduWebJobKinds[collection]
	if !ok {
		return false
	}
	switch len(parts) {
	case 2:
		if kuduMethod(w, r, http.MethodGet) {
			kuduListWebJobs(w, r, site, kind)
		}
	case 3:
		name := parts[2]
		switch r.Method {
		case http.MethodGet:
			if rec, ok := kuduWebJob(w, site, kind, name); ok {
				sim.WriteJSON(w, http.StatusOK, kuduWebJobWire(r, rec))
			}
		case http.MethodPut:
			kuduUploadWebJob(w, r, site, kind, name)
		case http.MethodDelete:
			kuduDeleteWebJob(site, kind, name)
			w.WriteHeader(http.StatusOK)
		default:
			kuduMethod(w, r, http.MethodGet, http.MethodPut, http.MethodDelete)
		}
	case 4:
		kuduWebJobAction(w, r, site, kind, parts[2], strings.ToLower(parts[3]))
	case 5:
		if kind != "triggered" || !strings.EqualFold(parts[3], "history") {
			kuduError(w, http.StatusNotFound, "No HTTP resource was found that matches the request URI '%s'.", r.URL.Path)
			return true
		}
		if !kuduMethod(w, r, http.MethodGet) {
			return true
		}
		rec, ok := kuduWebJob(w, site, kind, parts[2])
		if !ok {
			return true
		}
		run, found := webJobRuns.Get(rec.ID + "/history/" + parts[4])
		if !found {
			kuduError(w, http.StatusNotFound, "Run %q of job %q was not found.", parts[4], rec.Name)
			return true
		}
		sim.WriteJSON(w, http.StatusOK, kuduWebJobRunWire(r, rec, run))
	default:
		kuduError(w, http.StatusNotFound, "No HTTP resource was found that matches the request URI '%s'.", r.URL.Path)
	}
	return true
}

func kuduWebJobAction(w http.ResponseWriter, r *http.Request, site *Site, kind, name, action string) {
	switch {
	case action == "settings":
		switch r.Method {
		case http.MethodGet:
			if rec, ok := kuduWebJob(w, site, kind, name); ok {
				sim.WriteJSON(w, http.StatusOK, kuduWebJobSettings(rec))
			}
		case http.MethodPut:
			kuduPutWebJobSettings(w, r, site, kind, name)
		default:
			kuduMethod(w, r, http.MethodGet, http.MethodPut)
		}
	case kind == "triggered" && action == "run":
		if kuduMethod(w, r, http.MethodPost) {
			kuduRunWebJob(w, r, site, name)
		}
	case kind == "triggered" && action == "history":
		if !kuduMethod(w, r, http.MethodGet) {
			return
		}
		rec, ok := kuduWebJob(w, site, kind, name)
		if !ok {
			return
		}
		runs := webJobRunsFor(rec.ID)
		out := make([]any, 0, len(runs))
		for _, run := range runs {
			out = append(out, kuduWebJobRunWire(r, rec, run))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"runs": out})
	case kind == "continuous" && (action == "start" || action == "stop"):
		if !kuduMethod(w, r, http.MethodPost) {
			return
		}
		rec, ok := kuduWebJob(w, site, kind, name)
		if !ok {
			return
		}
		if action == "start" {
			webStartContinuousWebJob(rec)
		} else {
			webKillWebJobContainer(rec.ID)
			webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
				row.Status = "Stopped"
				row.DetailedStatus = ""
			})
		}
		w.WriteHeader(http.StatusOK)
	default:
		kuduError(w, http.StatusNotFound, "No HTTP resource was found that matches the request URI '%s'.", r.URL.Path)
	}
}

// kuduWebJob reads one of the site's jobs, answering 404 when it has none of
// that kind and name.
func kuduWebJob(w http.ResponseWriter, site *Site, kind, name string) (WebJobRecord, bool) {
	rec, ok := webWebJobs.Get(site.ID + "/" + kind + "webjobs/" + name)
	if !ok {
		kuduError(w, http.StatusNotFound, "No %s job named '%s' was found.", kind, name)
	}
	return rec, ok
}

func kuduListWebJobs(w http.ResponseWriter, r *http.Request, site *Site, kind string) {
	var recs []WebJobRecord
	for _, rec := range webWebJobsBySite.LookupAll(webWebJobs, site.ID,
		func(rec WebJobRecord) []string { return []string{rec.SiteID} }) {
		if kind == "" || rec.JobKind == kind {
			recs = append(recs, rec)
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].JobKind != recs[j].JobKind {
			return recs[i].JobKind > recs[j].JobKind
		}
		return recs[i].Name < recs[j].Name
	})
	out := make([]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, kuduWebJobWire(r, rec))
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func kuduWebJobsBase(r *http.Request) string {
	return azureRequestScheme(r) + "://" + r.Host + "/api/"
}

// kuduWebJobWire is a job as Kudu's TriggeredJob and ContinuousJob contracts
// spell it.
func kuduWebJobWire(r *http.Request, rec WebJobRecord) map[string]any {
	jobURL := kuduWebJobsBase(r) + rec.JobKind + "webjobs/" + url.PathEscape(rec.Name)
	out := map[string]any{
		"name":        rec.Name,
		"run_command": rec.RunCommand,
		"url":         jobURL,
		"type":        rec.JobKind,
		"error":       nil,
		"using_sdk":   false,
		"settings":    kuduWebJobSettings(rec),
	}
	if msg := webJobError(rec); msg != "" {
		out["error"] = msg
	}
	scm := azureRequestScheme(r) + "://" + r.Host
	if rec.JobKind == "continuous" {
		status := rec.Status
		if status == "" {
			status = "Stopped"
		}
		out["status"] = status
		out["detailed_status"] = rec.DetailedStatus
		webContinuousJobLogURL(out, scm, rec)
		return out
	}
	webJobSchedulerLogURL(out, scm, rec)
	out["history_url"] = jobURL + "/history"
	out["latest_run"] = nil
	if latest, ok := webLatestRun(rec.ID); ok {
		out["latest_run"] = kuduWebJobRunWire(r, rec, latest)
	}
	return out
}

// kuduWebJobRunWire is a run as Kudu's TriggeredJobRun contract spells it.
func kuduWebJobRunWire(r *http.Request, rec WebJobRecord, run WebJobRunRecord) map[string]any {
	out := map[string]any{
		"id":         run.RunID,
		"name":       run.RunID,
		"status":     run.Status,
		"start_time": run.StartTime,
		"end_time":   nil,
		"duration":   nil,
		"url":        kuduWebJobsBase(r) + "triggeredwebjobs/" + url.PathEscape(rec.Name) + "/history/" + run.RunID,
		"job_name":   rec.Name,
		"trigger":    webJobRunTrigger(run),
	}
	webJobRunLogURLs(out, azureRequestScheme(r)+"://"+r.Host, run)
	start, startErr := time.Parse(time.RFC3339, run.StartTime)
	if run.EndTime != "" {
		out["end_time"] = run.EndTime
		if end, err := time.Parse(time.RFC3339, run.EndTime); err == nil && startErr == nil {
			out["duration"] = kuduTimeSpan(end.Sub(start))
		}
	} else if startErr == nil {
		out["duration"] = kuduTimeSpan(time.Since(start))
	}
	return out
}

// kuduTimeSpan renders a duration as .NET serializes a TimeSpan.
func kuduTimeSpan(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	s := int(d % time.Minute / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// kuduWebJobSettingsPath is the wwwroot-relative path of a job's settings.job.
func kuduWebJobSettingsPath(kind, name string) string {
	return webJobsRoot + kind + "/" + name + "/settings.job"
}

// kuduWebJobSettings reads the job's settings.job, which holds a JSON object;
// a job without one has no settings.
func kuduWebJobSettings(rec WebJobRecord) map[string]any {
	settings := map[string]any{}
	f, ok := webSiteContent.Get(rec.SiteID + "|" + kuduWebJobSettingsPath(rec.JobKind, rec.Name))
	if ok {
		if err := json.Unmarshal(f.Data, &settings); err != nil {
			return map[string]any{}
		}
	}
	return settings
}

func kuduPutWebJobSettings(w http.ResponseWriter, r *http.Request, site *Site, kind, name string) {
	rec, ok := kuduWebJob(w, site, kind, name)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		kuduError(w, http.StatusBadRequest, "%v", err)
		return
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil || settings == nil {
		kuduError(w, http.StatusBadRequest, "The job settings must be a JSON object.")
		return
	}
	data, err := json.Marshal(settings)
	if err != nil {
		kuduError(w, http.StatusBadRequest, "%v", err)
		return
	}
	p := kuduWebJobSettingsPath(rec.JobKind, rec.Name)
	webSiteContent.Put(site.ID+"|"+p, WebSiteContentFile{ID: site.ID + "|" + p, Path: p, Mode: 0o644, Data: data, Modified: time.Now().UTC()})
	w.WriteHeader(http.StatusOK)
}

// kuduUploadWebJob places an uploaded job in the site's App_Data/jobs: a zip
// (Content-Type application/zip) unpacks into the job's directory, any other
// body becomes the one file its Content-Disposition names. The job replaces
// whatever the directory held.
func kuduUploadWebJob(w http.ResponseWriter, r *http.Request, site *Site, kind, name string) {
	if name == "" || strings.ContainsAny(name, `\/`) || name == "." || name == ".." {
		kuduError(w, http.StatusBadRequest, "The job name '%s' is not valid.", name)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, kuduWebJobsMaxUpload+1))
	if err != nil {
		kuduError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if len(data) > kuduWebJobsMaxUpload {
		kuduError(w, http.StatusRequestEntityTooLarge, "The job upload exceeds %d bytes.", kuduWebJobsMaxUpload)
		return
	}
	var files []archive.File
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.EqualFold(mediaType, "application/zip") {
		if err := archive.ReadZip(data, webSiteContentLimit, func(f archive.File) error {
			files = append(files, f)
			return nil
		}); err != nil {
			kuduError(w, http.StatusBadRequest, "The job upload is not a valid zip file: %v", err)
			return
		}
	} else {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
		filename := path.Base(params["filename"])
		if err != nil || filename == "" || filename == "." || filename == "/" {
			kuduError(w, http.StatusBadRequest,
				"Missing file name in the Content-Disposition header: send a zip as application/zip, or the run file with Content-Disposition: attachment; filename=<file>.")
			return
		}
		files = []archive.File{{Name: filename, Mode: 0o755, Data: data}}
	}
	hasRunFile := false
	for _, f := range files {
		if !strings.Contains(f.Name, "/") && strings.HasPrefix(f.Name, "run.") {
			hasRunFile = true
		}
	}
	if !hasRunFile {
		kuduError(w, http.StatusBadRequest,
			"Can't find a runnable script file in the root of the job: name it run.<extension> (run.sh, run.py, run.js, ...).")
		return
	}
	// A running continuous job restarts on its new files.
	id := site.ID + "/" + kind + "webjobs/" + name
	if kind == "continuous" {
		webKillWebJobContainer(id)
		webWebJobs.Update(id, func(row *WebJobRecord) { row.Status = "Stopped" })
	}
	webWriteSiteContent(site.ID, webJobsRoot+kind+"/"+name, files, webArtifact{Clean: true})
	webDiscoverWebJobs(site.ID)
	rec, ok := kuduWebJob(w, site, kind, name)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, kuduWebJobWire(r, rec))
}

// kuduDeleteWebJob removes a job's files and with them the job; deleting a job
// that does not exist changes nothing.
func kuduDeleteWebJob(site *Site, kind, name string) {
	prefix := site.ID + "|" + webJobsRoot + kind + "/" + name + "/"
	for _, f := range webSiteContent.Filter(func(f WebSiteContentFile) bool { return strings.HasPrefix(f.ID, prefix) }) {
		webSiteContent.Delete(f.ID)
	}
	webDiscoverWebJobs(site.ID)
}

// kuduRunWebJob starts a run of a triggered job and answers 202 with the
// run's history URL as Location. A job already running, or a site whose
// WEBJOBS_STOPPED setting keeps its jobs from running, answers 409.
func kuduRunWebJob(w http.ResponseWriter, r *http.Request, site *Site, name string) {
	rec, ok := kuduWebJob(w, site, "triggered", name)
	if !ok {
		return
	}
	if webJobsStopped(site.ID) {
		kuduWebAPIError(w, http.StatusConflict, "WebJobs are stopped for this site: the WEBJOBS_STOPPED setting is 1.")
		return
	}
	if latest, ok := webLatestRun(rec.ID); ok && latest.Status == "Running" {
		kuduWebAPIError(w, http.StatusConflict, "Cannot start a new run since job is already running.")
		return
	}
	trigger := "External - " + r.UserAgent()
	runID := webRunTriggeredWebJob(site, rec, strings.TrimSpace(trigger), kuduQuery(r, "arguments"))
	w.Header().Set("Location", kuduWebJobsBase(r)+"triggeredwebjobs/"+url.PathEscape(rec.Name)+"/history/"+runID)
	w.WriteHeader(http.StatusAccepted)
}

// kuduWebAPIError writes the error body ASP.NET Web API's CreateErrorResponse
// produces.
func kuduWebAPIError(w http.ResponseWriter, status int, message string) {
	sim.WriteJSON(w, status, map[string]any{"Message": message})
}
