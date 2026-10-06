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

// web_webjobs_logs.go writes the logs Kudu keeps for webjobs in the site's
// /home, where the VFS serves them and the job and run records name them:
// a triggered run's output_log.txt (and error_log.txt once the run writes to
// stderr) under data/jobs/triggered/<job>/<run>, a continuous job's
// job_log.txt under data/jobs/continuous/<job>, and a scheduled job's
// job_scheduler.log under data/jobs/triggered/<job>.

// kuduShortInstanceID names the instance in Kudu's log lines.
var kuduShortInstanceID = sim.RandomHex(3)

// webJobLogMu serializes appends to the job log files.
var webJobLogMu sync.Mutex

// webJobDataRel is the /home-relative directory Kudu keeps a job's data in.
func webJobDataRel(kind, name string) string {
	return path.Join("data", "jobs", kind, name)
}

// webJobRunLogRel is the /home-relative path of one of a triggered run's log
// files.
func webJobRunLogRel(jobName, runID, file string) string {
	return path.Join(webJobDataRel("triggered", jobName), runID, file)
}

// webContinuousJobLogRel is the /home-relative path of a continuous job's log.
func webContinuousJobLogRel(jobName string) string {
	return path.Join(webJobDataRel("continuous", jobName), "job_log.txt")
}

// webJobSchedulerLogRel is the /home-relative path of a triggered job's
// scheduler log.
func webJobSchedulerLogRel(jobName string) string {
	return path.Join(webJobDataRel("triggered", jobName), "job_scheduler.log")
}

// webJobLogExists reports whether a job log file exists in the site's /home.
func webJobLogExists(siteID, rel string) bool {
	site, ok := webJobSite(siteID)
	if !ok {
		return false
	}
	info, err := os.Stat(filepath.Join(siteHomeDir(site.Name), filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

// webJobAppendLog appends one line, in Kudu's format, to a job log file in the
// site's /home.
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

// webJobLogSink writes a job process's output to the site's container log
// and to the job's own log: stdout as INFO, stderr as ERR, which a triggered
// run also keeps in its error log.
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

// webJobRunLogURLs names a run's logs by their VFS URLs on the SCM host scm:
// output_url once the run started, error_url once it wrote to stderr.
func webJobRunLogURLs(out map[string]any, scm string, run WebJobRunRecord) {
	for member, file := range map[string]string{"output_url": "output_log.txt", "error_url": "error_log.txt"} {
		rel := webJobRunLogRel(run.JobName, run.RunID, file)
		if webJobLogExists(run.SiteID, rel) {
			out[member] = scm + "/vfs/" + rel
		}
	}
}

// webJobSchedulerLogURL names a triggered job's scheduler log once its
// schedule has written one.
func webJobSchedulerLogURL(out map[string]any, scm string, rec WebJobRecord) {
	if rel := webJobSchedulerLogRel(rec.Name); webJobLogExists(rec.SiteID, rel) {
		out["scheduler_logs_url"] = scm + "/vfs/" + rel
	}
}

// webContinuousJobLogURL names a continuous job's log once it has one.
func webContinuousJobLogURL(out map[string]any, scm string, rec WebJobRecord) {
	if rel := webContinuousJobLogRel(rec.Name); webJobLogExists(rec.SiteID, rel) {
		out["log_url"] = scm + "/vfs/" + rel
	}
}
