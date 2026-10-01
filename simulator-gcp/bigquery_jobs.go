package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// bqJobWork carries a load, copy or extract job out. It records the job's
// statistics in stats and commits its result only while ctx is live, so a
// cancelled or timed-out job changes nothing.
type bqJobWork func(ctx context.Context, stats map[string]any) error

// bqRun is a job the process is carrying out.
type bqRun struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

var (
	bqRunning    sync.Map
	bqJobRows    = sim.NewKeyedLocks()
	bqTableLocks = sim.NewKeyedLocks()

	errBQJobCancelled = &bqLoadFailure{"stopped", "Job execution was cancelled: User requested cancellation"}
)

// bqJobFailure is the ErrorProto a failed job finishes with.
func bqJobFailure(err error) map[string]any {
	var failure *bqLoadFailure
	if errors.As(err, &failure) {
		return map[string]any{"reason": failure.reason, "message": failure.message}
	}
	return map[string]any{"reason": "backendError", "message": err.Error()}
}

// bqNewJob is a job PENDING under its reference, as jobs.insert records it.
func bqNewJob(r *http.Request, project string, req BQJob) storedBQJob {
	jobID := req.JobReference.JobID
	if jobID == "" {
		jobID = "job_" + sim.NewUUID()
	}
	location := req.JobReference.Location
	if location == "" {
		location = "US"
	}
	return storedBQJob{BQJob: BQJob{
		Kind:          "bigquery#job",
		Etag:          bqEtag(jobID),
		ID:            project + ":" + jobID,
		SelfLink:      gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/jobs/"+jobID),
		JobReference:  BQJobRef{ProjectID: project, JobID: jobID, Location: location},
		Configuration: req.Configuration,
		Status:        map[string]any{"state": "PENDING"},
		Statistics:    map[string]any{"creationTime": bqMillisNow()},
	}}
}

// bqStartJob records job PENDING and carries work out in the background: the
// job turns RUNNING when work starts and DONE when it ends, with statsKey's
// statistics and, when work failed, an errorResult. A job whose
// configuration.jobTimeoutMs elapses first is stopped.
func bqStartJob(job storedBQJob, statsKey string, work bqJobWork) (storedBQJob, error) {
	key := bqJobKey(job.JobReference.ProjectID, job.JobReference.JobID)
	timeout, err := bqJobTimeout(job.Configuration)
	if err != nil {
		return storedBQJob{}, err
	}
	release := bqJobRows.Lock(key)
	if _, exists := bqJobs.Get(key); exists {
		release()
		return storedBQJob{}, apiRefuse(http.StatusConflict, "ALREADY_EXISTS", "Already Exists: Job %s:%s.%s",
			job.JobReference.ProjectID, job.JobReference.Location, job.JobReference.JobID)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	run := &bqRun{cancel: cancel, done: make(chan struct{})}
	bqRunning.Store(key, run)
	bqJobs.Put(key, job)
	release()

	go func() {
		defer close(run.done)
		defer bqRunning.Delete(key)
		defer cancel(nil)
		workCtx := ctx
		if timeout > 0 {
			var stop context.CancelFunc
			workCtx, stop = context.WithTimeoutCause(ctx, timeout, &bqLoadFailure{"timeout",
				fmt.Sprintf("Job timed out after %s: configuration.jobTimeoutMs is %d", timeout, timeout.Milliseconds())})
			defer stop()
		}
		bqUpdateJob(key, func(j *storedBQJob) {
			j.Status = map[string]any{"state": "RUNNING"}
			j.Statistics["startTime"] = bqMillisNow()
		})
		stats := map[string]any{}
		err := work(workCtx, stats)
		if err == nil && workCtx.Err() != nil {
			err = context.Cause(workCtx)
		}
		bqUpdateJob(key, func(j *storedBQJob) {
			j.Status = map[string]any{"state": "DONE"}
			if err != nil {
				proto := bqJobFailure(err)
				j.Status["errorResult"] = proto
				j.Status["errors"] = []any{proto}
			}
			j.Statistics["endTime"] = bqMillisNow()
			j.Statistics[statsKey] = stats
		})
	}()
	return job, nil
}

func bqJobTimeout(config map[string]any) (time.Duration, error) {
	raw, ok := config["jobTimeoutMs"]
	if !ok || raw == nil {
		return 0, nil
	}
	ms, err := strconv.ParseInt(fmt.Sprint(raw), 10, 64)
	if err != nil || ms < 0 {
		return 0, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for jobTimeoutMs: %v", raw)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// bqUpdateJob changes a stored job in place. A job deleted while it ran stays
// deleted.
func bqUpdateJob(key string, change func(*storedBQJob)) {
	defer bqJobRows.Lock(key)()
	job, ok := bqJobs.Get(key)
	if !ok {
		return
	}
	change(&job)
	bqJobs.Put(key, job)
}

// bqCancelRun asks a running job to stop.
func bqCancelRun(key string) {
	if run, ok := bqRunning.Load(key); ok {
		if r, ok := run.(*bqRun); ok {
			r.cancel(errBQJobCancelled)
		}
	}
}

// recoverBQJobs settles every job a restart caught unfinished. The goroutine
// carrying it out died with the previous process, so the job finishes DONE
// with the failure rather than staying RUNNING for a client to wait on forever.
func recoverBQJobs() {
	for _, job := range bqJobs.List() {
		if state, _ := job.Status["state"].(string); state == "DONE" {
			continue
		}
		proto := map[string]any{"reason": "backendError", "message": "The job did not finish before the BigQuery control plane restarted."}
		job.Status = map[string]any{"state": "DONE", "errorResult": proto, "errors": []any{proto}}
		if job.Statistics == nil {
			job.Statistics = map[string]any{}
		}
		job.Statistics["endTime"] = bqMillisNow()
		bqJobs.Put(bqJobKey(job.JobReference.ProjectID, job.JobReference.JobID), job)
	}
}

// bqWriteTable runs write under the table's lock, and only while ctx is live,
// so a job's checks against the destination and its write to it are one step.
func bqWriteTable(ctx context.Context, ref BQTableRef, write func() error) error {
	defer bqTableLocks.Lock(bqTableKey(ref.ProjectID, ref.DatasetID, ref.TableID))()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return write()
}
