package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"google.golang.org/grpc/codes"

	// Audit logging reads Cloud Scheduler's RPCs and their HTTP bindings from
	// the descriptors this links in.
	_ "cloud.google.com/go/scheduler/apiv1/schedulerpb"
)

// Cloud Scheduler fires each job on its unix-cron schedule, read in the job's
// time zone, through sim/cron, and delivers each execution through
// sim/delivery under the job's RetryConfig: an HTTP request to an HTTP
// target, or a publish to a Pub/Sub target as the project's Cloud Scheduler
// service agent. An execution delays the next scheduled one until it ends;
// jobs:run dispatches one whatever is running.

// CSJob is a Cloud Scheduler job. Outstanding and Deferred are the
// bookkeeping of its scheduled executions, which the API does not show.
type CSJob struct {
	Name                string         `json:"name"`
	Description         string         `json:"description,omitempty"`
	PubsubTarget        *CSPubsub      `json:"pubsubTarget,omitempty"`
	AppEngineHTTPTarget map[string]any `json:"appEngineHttpTarget,omitempty"`
	HTTPTarget          *CSHTTP        `json:"httpTarget,omitempty"`
	Schedule            string         `json:"schedule,omitempty"`
	TimeZone            string         `json:"timeZone,omitempty"`
	UserUpdateTime      string         `json:"userUpdateTime,omitempty"`
	State               csEnum         `json:"state,omitempty"`
	Status              *CSStatus      `json:"status,omitempty"`
	ScheduleTime        string         `json:"scheduleTime,omitempty"`
	LastAttemptTime     string         `json:"lastAttemptTime,omitempty"`
	RetryConfig         *CSRetryConfig `json:"retryConfig,omitempty"`
	AttemptDeadline     string         `json:"attemptDeadline,omitempty"`

	Outstanding string    `json:"-"`
	Deferred    time.Time `json:"-"`
}

type CSPubsub struct {
	TopicName  string            `json:"topicName,omitempty"`
	Data       string            `json:"data,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type CSHTTP struct {
	URI        string            `json:"uri,omitempty"`
	HTTPMethod csEnum            `json:"httpMethod,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	OAuthToken *struct {
		ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
		Scope               string `json:"scope,omitempty"`
	} `json:"oauthToken,omitempty"`
	OidcToken *struct {
		ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
		Audience            string `json:"audience,omitempty"`
	} `json:"oidcToken,omitempty"`
}

// CSStatus is the google.rpc.Status of a job's last attempted execution;
// a successful one is the empty status.
type CSStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type CSRetryConfig struct {
	RetryCount         int    `json:"retryCount,omitempty"`
	MaxRetryDuration   string `json:"maxRetryDuration,omitempty"`
	MinBackoffDuration string `json:"minBackoffDuration,omitempty"`
	MaxBackoffDuration string `json:"maxBackoffDuration,omitempty"`
	MaxDoublings       int    `json:"maxDoublings,omitempty"`
}

// csEnum is an enum field, read from its name or, as the Go client's REST
// transport sends it, its number, and written as its name.
type csEnum string

// csEnumNames are the names of the enums a job carries, indexed by number.
var csEnumNames = map[string][]string{
	"httpMethod": {"HTTP_METHOD_UNSPECIFIED", "POST", "GET", "HEAD", "PUT", "DELETE", "PATCH", "OPTIONS"},
	"state":      {"STATE_UNSPECIFIED", "ENABLED", "PAUSED", "DISABLED", "UPDATE_FAILED"},
}

func (e *csEnum) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*e = csEnum(name)
		return nil
	}
	var number int
	if err := json.Unmarshal(b, &number); err != nil {
		return fmt.Errorf("enum value %s is neither a name nor a number", b)
	}
	*e = csEnum(fmt.Sprintf("#%d", number))
	return nil
}

// csResolveEnums names the enum numbers a request carried.
func csResolveEnums(job *CSJob) error {
	resolve := func(field string, e *csEnum) error {
		number, ok := strings.CutPrefix(string(*e), "#")
		if !ok {
			return nil
		}
		names := csEnumNames[field]
		for i, name := range names {
			if fmt.Sprint(i) == number {
				*e = csEnum(name)
				return nil
			}
		}
		return fmt.Errorf("%s %s is not a value of the enum", field, number)
	}
	if err := resolve("state", &job.State); err != nil {
		return err
	}
	if job.HTTPTarget != nil {
		return resolve("httpMethod", &job.HTTPTarget.HTTPMethod)
	}
	return nil
}

// csExecution is one execution of a job: an occurrence of its schedule, or a
// jobs:run.
type csExecution struct {
	Job          string
	ScheduleTime time.Time
	Scheduled    bool
}

var (
	csJobs       sim.Store[CSJob]
	csSchedules  sim.Store[cron.Record]
	csExecutions *delivery.Dispatcher[csExecution]
)

const (
	csDefaultMinBackoff     = 5 * time.Second
	csDefaultMaxBackoff     = time.Hour
	csDefaultMaxDoublings   = 5
	csMaxRetryCount         = 5
	csDefaultHTTPDeadline   = 3 * time.Minute
	csMinHTTPDeadline       = 15 * time.Second
	csMaxHTTPDeadline       = 30 * time.Minute
	csCmekUnservedWhy       = "the simulator encrypts no Cloud Scheduler job with a customer-managed Cloud KMS key"
	csAppEngineUnservedWhy  = "the simulator serves no App Engine application for an appEngineHttpTarget to reach"
	csGrocUnservedWhy       = "the simulator reads unix-cron schedules, not the English-like (groc) format"
	csJobNotFoundStatusText = "NOT_FOUND"
)

var csJobIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,500}$`)

func registerCloudScheduler(srv *sim.Server) {
	csJobs = sim.MakeStore[CSJob](srv.DB(), "cloudscheduler_jobs")
	csSchedules = sim.MakeStore[cron.Record](srv.DB(), "cloudscheduler_job_schedules")
	store := sim.MakeStore[delivery.Item[csExecution]](srv.DB(), "cloudscheduler_executions")
	csExecutions = delivery.New(srv, "Cloud Scheduler executions", store, delivery.Handler[csExecution]{
		Policy:  csExecutionPolicy,
		Attempt: csAttemptExecution,
		Finish:  csFinishExecution,
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/jobs", handleCSCreateJob)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/jobs", handleCSListJobs)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/jobs/{job}", handleCSPatchJob)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/jobs/{job}", handleCSDeleteJob)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/jobs/{jobAction}", handleCSJobAction)

	cmek := func(w http.ResponseWriter, r *http.Request) {
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no cloudscheduler cmekConfig: %s", csCmekUnservedWhy)
	}
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/cmekConfig", cmek)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/cmekConfig", cmek)
}

// startCloudScheduler resumes the executions a previous run left and fires
// the jobs' schedules.
func startCloudScheduler(srv *sim.Server) {
	csExecutions.Resume()
	cron.NewTicker(csSchedules, csScheduleEntries).Start(srv, "Cloud Scheduler schedules", time.Second)
}

func csJobName(r *http.Request, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"), id)
}

// csLocation reads a time zone as Cloud Scheduler does: a tz database name,
// UTC when unset, and "utc" in any case.
func csLocation(tz string) (*time.Location, error) {
	if tz == "" || strings.EqualFold(tz, "utc") {
		return time.UTC, nil
	}
	return time.LoadLocation(tz)
}

func csParseSchedule(job CSJob) (cron.Schedule, error) {
	loc, err := csLocation(job.TimeZone)
	if err != nil {
		return cron.Schedule{}, fmt.Errorf("timeZone %q is not a tz database name", job.TimeZone)
	}
	return cron.Parse(job.Schedule, cron.Vixie, loc)
}

// csScheduleIsCron reports whether a schedule is in the unix-cron format, the
// five fields of digits and cron punctuation, month and day names aside.
func csScheduleIsCron(schedule string) bool {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		for _, c := range strings.ToUpper(f) {
			if !strings.ContainsRune("0123456789*,-/ABCDEFGHIJKLMNOPQRSTUVWXYZ", c) {
				return false
			}
		}
	}
	return !strings.Contains(strings.ToLower(schedule), "every")
}

// csValidate checks a job the way CreateJob and UpdateJob do, answering the
// HTTP status and the reason a job is refused.
func csValidate(job *CSJob) (int, string) {
	targets := 0
	for _, set := range []bool{job.HTTPTarget != nil, job.PubsubTarget != nil, job.AppEngineHTTPTarget != nil} {
		if set {
			targets++
		}
	}
	switch {
	case targets != 1:
		return http.StatusBadRequest, "a job needs exactly one of httpTarget, pubsubTarget and appEngineHttpTarget"
	case job.AppEngineHTTPTarget != nil:
		return http.StatusNotImplemented, csAppEngineUnservedWhy
	case job.Schedule == "":
		return http.StatusBadRequest, "schedule is required"
	case utf8.RuneCountInString(job.Description) > 500:
		return http.StatusBadRequest, "description must not contain more than 500 characters"
	}
	if !csScheduleIsCron(job.Schedule) {
		return http.StatusNotImplemented, csGrocUnservedWhy
	}
	if _, err := csParseSchedule(*job); err != nil {
		return http.StatusBadRequest, fmt.Sprintf("schedule %q is invalid: %v", job.Schedule, err)
	}
	if job.RetryConfig != nil {
		rc := job.RetryConfig
		if rc.RetryCount < 0 || rc.RetryCount > csMaxRetryCount {
			return http.StatusBadRequest, "retryCount must be between 0 and 5"
		}
		if rc.MaxDoublings < 0 {
			return http.StatusBadRequest, "maxDoublings must not be negative"
		}
		for _, d := range []string{rc.MaxRetryDuration, rc.MinBackoffDuration, rc.MaxBackoffDuration} {
			if d == "" {
				continue
			}
			if _, err := psParseDuration(d); err != nil {
				return http.StatusBadRequest, err.Error()
			}
		}
	}
	project := strings.Split(job.Name, "/")[1]
	if t := job.PubsubTarget; t != nil {
		parts := strings.Split(t.TopicName, "/")
		switch {
		case len(parts) != 4 || parts[0] != "projects" || parts[2] != "topics" || parts[3] == "":
			return http.StatusBadRequest, fmt.Sprintf("topicName %q is not projects/PROJECT_ID/topics/TOPIC_ID", t.TopicName)
		case parts[1] != project:
			return http.StatusBadRequest, "the topic must be in the same project as the job"
		case t.Data == "" && len(t.Attributes) == 0:
			return http.StatusBadRequest, "a Pub/Sub message must contain either non-empty data, or at least one attribute"
		}
		if _, err := base64.StdEncoding.DecodeString(t.Data); err != nil {
			return http.StatusBadRequest, "pubsubTarget.data is not base64"
		}
	}
	if t := job.HTTPTarget; t != nil {
		if !strings.HasPrefix(t.URI, "http://") && !strings.HasPrefix(t.URI, "https://") {
			return http.StatusBadRequest, "httpTarget.uri must begin with http:// or https://"
		}
		if t.HTTPMethod == "" || t.HTTPMethod == "HTTP_METHOD_UNSPECIFIED" {
			t.HTTPMethod = http.MethodPost
		}
		switch string(t.HTTPMethod) {
		case http.MethodPost, http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions:
		default:
			return http.StatusBadRequest, fmt.Sprintf("httpMethod %q is not an HTTP method", t.HTTPMethod)
		}
		if t.Body != "" {
			if method := string(t.HTTPMethod); method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
				return http.StatusBadRequest, "a request body is allowed only if the HTTP method is POST, PUT, or PATCH"
			}
			if _, err := base64.StdEncoding.DecodeString(t.Body); err != nil {
				return http.StatusBadRequest, "httpTarget.body is not base64"
			}
		}
		if t.OAuthToken != nil && t.OidcToken != nil {
			return http.StatusBadRequest, "httpTarget takes one of oauthToken and oidcToken"
		}
		if job.AttemptDeadline == "" {
			job.AttemptDeadline = fmt.Sprintf("%ds", int(csDefaultHTTPDeadline/time.Second))
		}
		deadline, err := psParseDuration(job.AttemptDeadline)
		if err != nil {
			return http.StatusBadRequest, err.Error()
		}
		if deadline < csMinHTTPDeadline || deadline > csMaxHTTPDeadline {
			return http.StatusBadRequest, "attemptDeadline must be between 15 seconds and 30 minutes for an HTTP target"
		}
	}
	return 0, ""
}

// csWithDefaults fills the fields a job's RetryConfig leaves unset with the
// defaults Cloud Scheduler documents. A job without a RetryConfig keeps none
// and is not retried.
func csWithDefaults(job *CSJob) {
	if job.RetryConfig == nil {
		return
	}
	rc := job.RetryConfig
	if rc.MaxRetryDuration == "" {
		rc.MaxRetryDuration = "0s"
	}
	if rc.MinBackoffDuration == "" {
		rc.MinBackoffDuration = fmt.Sprintf("%ds", int(csDefaultMinBackoff/time.Second))
	}
	if rc.MaxBackoffDuration == "" {
		rc.MaxBackoffDuration = fmt.Sprintf("%ds", int(csDefaultMaxBackoff/time.Second))
	}
	if rc.MaxDoublings == 0 {
		rc.MaxDoublings = csDefaultMaxDoublings
	}
}

// csView is a job as the API shows it: its next scheduled time while it is
// enabled.
func csView(job CSJob) CSJob {
	job.ScheduleTime = ""
	if job.State != "ENABLED" {
		return job
	}
	if schedule, err := csParseSchedule(job); err == nil {
		if next, ok := schedule.Next(time.Now()); ok {
			job.ScheduleTime = protoJSONTimestamp(next)
		}
	}
	return job
}

func csReadJob(r *http.Request, job *CSJob) error {
	if err := sim.ReadJSON(r, job); err != nil {
		return err
	}
	return csResolveEnums(job)
}

func csRefuse(w http.ResponseWriter, status int, reason string) {
	if status == http.StatusNotImplemented {
		GCPErrorf(w, status, "UNIMPLEMENTED", "%s", reason)
		return
	}
	GCPErrorf(w, status, "INVALID_ARGUMENT", "%s", reason)
}

func handleCSCreateJob(w http.ResponseWriter, r *http.Request) {
	var job CSJob
	if err := csReadJob(r, &job); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	parent := fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	if job.Name == "" {
		job.Name = parent + "/jobs/" + strings.ReplaceAll(sim.NewUUID(), "-", "")
	}
	id, ok := strings.CutPrefix(job.Name, parent+"/jobs/")
	if !ok || !csJobIDPattern.MatchString(id) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "job name %q is not %s/jobs/JOB_ID with a JOB_ID of letters, numbers, hyphens or underscores", job.Name, parent)
		return
	}
	if status, reason := csValidate(&job); status != 0 {
		csRefuse(w, status, reason)
		return
	}
	csWithDefaults(&job)
	job.State = "ENABLED"
	job.Status, job.LastAttemptTime, job.Outstanding, job.Deferred = nil, "", "", time.Time{}
	job.UserUpdateTime = nowTimestamp()
	created := false
	csJobs.Upsert(job.Name, func(existing *CSJob) {
		if existing.Name != "" {
			return
		}
		*existing, created = job, true
	})
	if !created {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Job %s already exists", job.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, csView(job))
}

func csGetJob(w http.ResponseWriter, name string) (CSJob, bool) {
	job, ok := csJobs.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, csJobNotFoundStatusText, "Job not found: %s", name)
	}
	return job, ok
}

// handleCSGetJob serves jobs.get; Cloud Run Admin v1 publishes the colon
// verbs that share its URI.
func handleCSGetJob(w http.ResponseWriter, r *http.Request, id string) {
	if job, ok := csGetJob(w, csJobName(r, id)); ok {
		sim.WriteJSON(w, http.StatusOK, csView(job))
	}
}

func handleCSListJobs(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/jobs/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	var jobs []CSJob
	for _, row := range csJobs.ListPrefix(prefix) {
		jobs = append(jobs, csView(row.Item))
	}
	page, next, ok := paginateList(w, r, jobs, gcpAsc(func(j CSJob) string { return j.Name }))
	if !ok {
		return
	}
	resp := map[string]any{"jobs": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// csMaskable names the update mask paths a job's fields answer to.
var csMaskable = map[string]func(dst, src *CSJob){
	"description":      func(dst, src *CSJob) { dst.Description = src.Description },
	"schedule":         func(dst, src *CSJob) { dst.Schedule = src.Schedule },
	"time_zone":        func(dst, src *CSJob) { dst.TimeZone = src.TimeZone },
	"attempt_deadline": func(dst, src *CSJob) { dst.AttemptDeadline = src.AttemptDeadline },
	"retry_config":     func(dst, src *CSJob) { dst.RetryConfig = src.RetryConfig },
	"http_target": func(dst, src *CSJob) {
		dst.HTTPTarget, dst.PubsubTarget, dst.AppEngineHTTPTarget = src.HTTPTarget, nil, nil
	},
	"pubsub_target": func(dst, src *CSJob) {
		dst.PubsubTarget, dst.HTTPTarget, dst.AppEngineHTTPTarget = src.PubsubTarget, nil, nil
	},
	"app_engine_http_target": func(dst, src *CSJob) {
		dst.AppEngineHTTPTarget, dst.HTTPTarget, dst.PubsubTarget = src.AppEngineHTTPTarget, nil, nil
	},
}

// csMaskPath reads one update mask path as the snake_case top-level field it
// changes; a nested path changes its whole top-level field.
func csMaskPath(path string) string {
	top, _, _ := strings.Cut(strings.TrimSpace(path), ".")
	var b strings.Builder
	for i, c := range top {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			c += 'a' - 'A'
		}
		b.WriteRune(c)
	}
	return b.String()
}

func handleCSPatchJob(w http.ResponseWriter, r *http.Request) {
	name := csJobName(r, sim.PathParam(r, "job"))
	var patch CSJob
	if err := csReadJob(r, &patch); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	existing, ok := csGetJob(w, name)
	if !ok {
		return
	}
	updated := existing
	var paths []string
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		paths = strings.Split(mask, ",")
	} else {
		for path, set := range map[string]bool{
			"description": patch.Description != "", "schedule": patch.Schedule != "", "time_zone": patch.TimeZone != "",
			"attempt_deadline": patch.AttemptDeadline != "", "retry_config": patch.RetryConfig != nil,
			"http_target": patch.HTTPTarget != nil, "pubsub_target": patch.PubsubTarget != nil,
			"app_engine_http_target": patch.AppEngineHTTPTarget != nil,
		} {
			if set {
				paths = append(paths, path)
			}
		}
	}
	for _, path := range paths {
		apply, known := csMaskable[csMaskPath(path)]
		if !known {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "updateMask path %q names no field a job update changes", path)
			return
		}
		apply(&updated, &patch)
	}
	if status, reason := csValidate(&updated); status != 0 {
		csRefuse(w, status, reason)
		return
	}
	csWithDefaults(&updated)
	updated.UserUpdateTime = nowTimestamp()
	if !csJobs.Update(name, func(job *CSJob) {
		outstanding, deferred, state, status, last := job.Outstanding, job.Deferred, job.State, job.Status, job.LastAttemptTime
		*job = updated
		job.Outstanding, job.Deferred, job.State, job.Status, job.LastAttemptTime = outstanding, deferred, state, status, last
		updated = *job
	}) {
		GCPErrorf(w, http.StatusNotFound, csJobNotFoundStatusText, "Job not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, csView(updated))
}

func handleCSDeleteJob(w http.ResponseWriter, r *http.Request) {
	name := csJobName(r, sim.PathParam(r, "job"))
	if _, ok := csGetJob(w, name); !ok {
		return
	}
	csJobs.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleCSJobAction serves jobs:pause, jobs:resume and jobs:run. Pausing a
// paused job and resuming an enabled one leave it as it is, which the Google
// provider relies on when it reconciles a job's paused attribute.
func handleCSJobAction(w http.ResponseWriter, r *http.Request) {
	id, action, _ := strings.Cut(sim.PathParam(r, "jobAction"), ":")
	if action == "setIamPolicy" || action == "testIamPermissions" {
		// Cloud Run Admin v1 publishes a job's IAM writes at this URI.
		cloudRunV1JobIAM(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "location"), id, action)
		return
	}
	states := map[string]csEnum{"pause": "PAUSED", "resume": "ENABLED", "run": ""}
	state, known := states[action]
	if !known {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown job action %q", action)
		return
	}
	name := csJobName(r, id)
	var job CSJob
	if !csJobs.Update(name, func(j *CSJob) {
		if state != "" {
			j.State = state
		}
		job = *j
	}) {
		GCPErrorf(w, http.StatusNotFound, csJobNotFoundStatusText, "Job not found: %s", name)
		return
	}
	if action == "run" {
		csStartExecution(name, time.Now().UTC(), false)
		job, _ = csJobs.Get(name)
	}
	sim.WriteJSON(w, http.StatusOK, csView(job))
}

// csScheduleEntries fires every job's schedule; a paused job keeps its place.
func csScheduleEntries() []cron.Entry {
	var entries []cron.Entry
	for _, job := range csJobs.List() {
		schedule, err := csParseSchedule(job)
		if err != nil {
			continue
		}
		name := job.Name
		entries = append(entries, cron.Entry{
			Key:    name,
			Spec:   job.Schedule + "|" + job.TimeZone + "|" + job.UserUpdateTime,
			Paused: job.State != "ENABLED",
			Next:   schedule.Next,
			Fire:   func(scheduled time.Time) { csStartExecution(name, scheduled, true) },
		})
	}
	return entries
}

// csStartExecution dispatches one execution of a job. A scheduled one waits
// for the job's previous scheduled execution to end, as Cloud Scheduler never
// lets two be outstanding.
func csStartExecution(name string, scheduleTime time.Time, scheduled bool) {
	id := sim.NewUUID()
	start := true
	csJobs.Update(name, func(job *CSJob) {
		if !scheduled {
			return
		}
		if job.Outstanding != "" {
			job.Deferred, start = scheduleTime, false
			return
		}
		job.Outstanding = id
	})
	if start {
		csExecutions.Submit(id, csExecution{Job: name, ScheduleTime: scheduleTime, Scheduled: scheduled})
	}
}

// csBackoff is the wait before retry n: minBackoffDuration doubled
// maxDoublings times, then grown by the last doubled interval each retry,
// never more than maxBackoffDuration.
func csBackoff(rc CSRetryConfig) func(int) time.Duration {
	minWait, _ := psParseDuration(rc.MinBackoffDuration)
	maxWait, _ := psParseDuration(rc.MaxBackoffDuration)
	doublings := rc.MaxDoublings
	return func(retry int) time.Duration {
		step := retry - 1
		var wait time.Duration
		if step <= doublings {
			wait = minWait << step
		} else {
			wait = (minWait << doublings) * time.Duration(step-doublings+1)
		}
		if wait < 0 || wait > maxWait {
			return maxWait
		}
		return wait
	}
}

// csExecutionPolicy retries a failed execution until retryCount retries and
// maxRetryDuration have both run out.
func csExecutionPolicy(e csExecution) delivery.Policy {
	job, ok := csJobs.Get(e.Job)
	if !ok || job.RetryConfig == nil {
		return delivery.Policy{MaxAttempts: 1}
	}
	rc := *job.RetryConfig
	maxAge, _ := psParseDuration(rc.MaxRetryDuration)
	return delivery.Policy{
		MaxAttempts: rc.RetryCount + 1,
		MaxAge:      maxAge,
		Backoff:     csBackoff(rc),
		BothLimits:  true,
	}
}

var errCSJobDeleted = errors.New("the job was deleted")

func csAttemptExecution(ctx context.Context, item *delivery.Item[csExecution]) delivery.Outcome {
	e := item.Payload
	job, ok := csJobs.Get(e.Job)
	if !ok {
		return delivery.Permanent(errCSJobDeleted)
	}
	var outcome delivery.Outcome
	var status CSStatus
	switch {
	case job.PubsubTarget != nil:
		outcome, status = csPublish(ctx, job)
	case job.HTTPTarget != nil:
		outcome, status = csRequest(ctx, job, e)
	default:
		return delivery.Permanent(errors.New(csAppEngineUnservedWhy))
	}
	csJobs.Update(e.Job, func(j *CSJob) {
		j.LastAttemptTime, j.Status = protoJSONTimestamp(item.LastAttemptAt), &status
	})
	return outcome
}

// csPublish publishes the job's message to its topic as the project's Cloud
// Scheduler service agent.
func csPublish(ctx context.Context, job CSJob) (delivery.Outcome, CSStatus) {
	project := strings.Split(job.Name, "/")[1]
	number, ok := crmProjectNumber(project)
	if !ok {
		return delivery.Retryable(fmt.Errorf("project %s is not active", project)), CSStatus{Code: int(codes.PermissionDenied)}
	}
	t := job.PubsubTarget
	message := map[string]any{}
	if t.Data != "" {
		message["data"] = t.Data
	}
	if len(t.Attributes) > 0 {
		message["attributes"] = t.Attributes
	}
	err := gcpCallAs(ctx, gcpServiceAgent(number, "cloudscheduler"), http.MethodPost, "/v1/"+t.TopicName+":publish",
		map[string]any{"messages": []any{message}}, nil)
	if err != nil {
		var refused *gcpCallError
		if errors.As(err, &refused) {
			return delivery.Retryable(err).WithStatus(refused.Code), CSStatus{Code: csCode(refused.Code), Message: refused.Message}
		}
		return delivery.Retryable(err), CSStatus{Code: int(codes.Unavailable)}
	}
	return delivery.Delivered(), CSStatus{}
}

// csRequest sends the job's HTTP request: the target's method, headers and
// body, the headers Cloud Scheduler sets, and an OIDC or OAuth token for the
// target's service account; any answer outside 2xx fails the attempt.
func csRequest(ctx context.Context, job CSJob, e csExecution) (delivery.Outcome, CSStatus) {
	t := job.HTTPTarget
	deadline, err := psParseDuration(job.AttemptDeadline)
	if err != nil || deadline == 0 {
		deadline = csDefaultHTTPDeadline
	}
	body, _ := base64.StdEncoding.DecodeString(t.Body)
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	method := string(t.HTTPMethod)
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, t.URI, strings.NewReader(string(body)))
	if err != nil {
		return delivery.Retryable(err), CSStatus{Code: int(codes.InvalidArgument)}
	}
	for key, value := range t.Headers {
		req.Header.Set(key, value)
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("User-Agent", "Google-Cloud-Scheduler")
	req.Header.Set("X-CloudScheduler", "true")
	req.Header.Set("X-CloudScheduler-JobName", job.Name[strings.LastIndex(job.Name, "/")+1:])
	if e.Scheduled {
		req.Header.Set("X-CloudScheduler-ScheduleTime", e.ScheduleTime.UTC().Format(time.RFC3339))
	}
	now := time.Now()
	switch {
	case t.OidcToken != nil:
		audience := t.OidcToken.Audience
		if audience == "" {
			audience = t.URI
		}
		req.Header.Set("Authorization", "Bearer "+signServiceAccountIDToken(t.OidcToken.ServiceAccountEmail, audience, true, now, now.Add(time.Hour)))
	case t.OAuthToken != nil:
		scope := t.OAuthToken.Scope
		if scope == "" {
			scope = "https://www.googleapis.com/auth/cloud-platform"
		}
		req.Header.Set("Authorization", "Bearer "+signWithAccessKey(map[string]any{
			"iss": googleTokenIssuer, "aud": simAccessTokenAudience, "sub": t.OAuthToken.ServiceAccountEmail,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "scope": scope,
		}))
	}
	resp, err := csHTTPClient.Do(req)
	if err != nil {
		code := codes.Unavailable
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = codes.DeadlineExceeded
		}
		return delivery.Retryable(err), CSStatus{Code: int(code)}
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return delivery.Delivered().WithStatus(resp.StatusCode), CSStatus{}
	}
	return delivery.Retryable(fmt.Errorf("%s answered %d", t.URI, resp.StatusCode)).WithStatus(resp.StatusCode),
		CSStatus{Code: csCode(resp.StatusCode)}
}

var csHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// csFinishExecution ends a scheduled execution and starts the occurrence it
// delayed, if any.
func csFinishExecution(item delivery.Item[csExecution], _ delivery.Reason) {
	if !item.Payload.Scheduled {
		return
	}
	var deferred time.Time
	csJobs.Update(item.Payload.Job, func(job *CSJob) {
		if job.Outstanding != item.ID {
			return
		}
		deferred = job.Deferred
		job.Outstanding, job.Deferred = "", time.Time{}
	})
	if !deferred.IsZero() {
		csStartExecution(item.Payload.Job, deferred, true)
	}
}

// csCode is the google.rpc.Code a job's status records for an HTTP status.
func csCode(status int) int {
	if status == http.StatusUnauthorized {
		return int(codes.Unauthenticated)
	}
	if code, ok := auditHTTPStatusCodes[status]; ok {
		return int(code)
	}
	if status >= 500 {
		return int(codes.Internal)
	}
	return int(codes.Unknown)
}
