package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Kudu keeps webjob logs in the site's /home, where the VFS serves them: a
// triggered run's output_log.txt (and error_log.txt once it writes to stderr)
// under data/jobs/triggered/<job>/<run>, a continuous job's job_log.txt under
// data/jobs/continuous/<job>, and a scheduled job's job_scheduler.log under
// data/jobs/triggered/<job>.

var kuduShortInstanceID = sim.RandomHex(3)

var webJobLogMu sync.Mutex

func webJobDataRel(kind, name string) string {
	return path.Join("data", "jobs", kind, name)
}

func webJobRunLogRel(jobName, runID, file string) string {
	return path.Join(webJobDataRel("triggered", jobName), runID, file)
}

func webContinuousJobLogRel(jobName string) string {
	return path.Join(webJobDataRel("continuous", jobName), "job_log.txt")
}

func webJobSchedulerLogRel(jobName string) string {
	return path.Join(webJobDataRel("triggered", jobName), "job_scheduler.log")
}

func webJobLogExists(siteID, rel string) bool {
	site, ok := webJobSite(siteID)
	if !ok {
		return false
	}
	info, err := os.Stat(filepath.Join(siteHomeDir(site.Name), filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

func webJobAppendLog(site *Site, rel, level, message string) {
	webJobLogMu.Lock()
	defer webJobLogMu.Unlock()
	file := filepath.Join(siteHomeDir(site.Name), filepath.FromSlash(rel))
	if err := sim.EnsureWritableDir(filepath.Dir(file)); err != nil {
		injectSiteTrace(site, fmt.Sprintf("WebJobs: create %s: %v", rel, err))
		return
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		injectSiteTrace(site, fmt.Sprintf("WebJobs: open %s: %v", rel, err))
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "[%s > %s: %s] %s\n", time.Now().UTC().Format("01/02/2006 15:04:05"), kuduShortInstanceID, level, message)
}

// stdout logs as INFO and stderr as ERR, which a triggered run also keeps in
// its error log.
type webJobLogSink struct {
	site      *Site
	container sim.LogSink
	outRel    string
	errRel    string
}

func (s webJobLogSink) WriteLog(line sim.LogLine) {
	s.container.WriteLog(line)
	if line.Stream == "stderr" {
		webJobAppendLog(s.site, s.outRel, "ERR ", line.Text)
		if s.errRel != "" {
			webJobAppendLog(s.site, s.errRel, "ERR ", line.Text)
		}
		return
	}
	webJobAppendLog(s.site, s.outRel, "INFO", line.Text)
}

// error_url appears only once the run wrote to stderr.
func webJobRunLogURLs(out map[string]any, scm string, run WebJobRunRecord) {
	for member, file := range map[string]string{"output_url": "output_log.txt", "error_url": "error_log.txt"} {
		rel := webJobRunLogRel(run.JobName, run.RunID, file)
		if webJobLogExists(run.SiteID, rel) {
			out[member] = scm + "/vfs/" + rel
		}
	}
}

func webJobSchedulerLogURL(out map[string]any, scm string, rec WebJobRecord) {
	if rel := webJobSchedulerLogRel(rec.Name); webJobLogExists(rec.SiteID, rel) {
		out["scheduler_logs_url"] = scm + "/vfs/" + rel
	}
}

func webContinuousJobLogURL(out map[string]any, scm string, rec WebJobRecord) {
	if rel := webContinuousJobLogRel(rec.Name); webJobLogExists(rec.SiteID, rel) {
		out["log_url"] = scm + "/vfs/" + rel
	}
}
