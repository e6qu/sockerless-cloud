package main

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"github.com/e6qu/sockerless-cloud/sim/msgq"

	// Audit logging reads Cloud Tasks' RPCs and their HTTP bindings from the
	// descriptors this links in.
	_ "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
)

// Cloud Tasks keeps each queue's tasks in a msgq queue: a task's next attempt
// is the message's availability, a dispatch is its lease. The queue's own
// rateLimits (a token bucket refilled at maxDispatchesPerSecond holding up to
// maxBurstSize, and at most maxConcurrentDispatches in flight) and state decide
// when a ready task goes out, and its retryConfig — or the task's own —
// decides when a failed one is tried again. Dispatches wake on the queue's
// change signal and its next-change timer (msgq.Waiter), never on a ticker.

// CTQueue is a Cloud Tasks queue as the API shows it.
type CTQueue struct {
	Name                     string                  `json:"name"`
	AppEngineRoutingOverride map[string]any          `json:"appEngineRoutingOverride,omitempty"`
	HTTPTarget               *CTHTTPTarget           `json:"httpTarget,omitempty"`
	RateLimits               *CTRateLimits           `json:"rateLimits,omitempty"`
	RetryConfig              *CTRetryConfig          `json:"retryConfig,omitempty"`
	State                    protoEnum[ctQueueState] `json:"state,omitempty"`
	PurgeTime                string                  `json:"purgeTime,omitempty"`
	StackdriverLoggingConfig *struct {
		SamplingRatio float64 `json:"samplingRatio,omitempty"`
	} `json:"stackdriverLoggingConfig,omitempty"`
}

type CTRateLimits struct {
	MaxDispatchesPerSecond  float64 `json:"maxDispatchesPerSecond,omitempty"`
	MaxBurstSize            int     `json:"maxBurstSize,omitempty"`
	MaxConcurrentDispatches int     `json:"maxConcurrentDispatches,omitempty"`
}

type CTRetryConfig struct {
	MaxAttempts      int    `json:"maxAttempts,omitempty"`
	MaxRetryDuration string `json:"maxRetryDuration,omitempty"`
	MinBackoff       string `json:"minBackoff,omitempty"`
	MaxBackoff       string `json:"maxBackoff,omitempty"`
	MaxDoublings     int    `json:"maxDoublings,omitempty"`
}

// CTHTTPTarget is a queue's override of its HTTP tasks' routing.
type CTHTTPTarget struct {
	URIOverride     *CTURIOverride          `json:"uriOverride,omitempty"`
	HTTPMethod      protoEnum[ctHTTPMethod] `json:"httpMethod,omitempty"`
	HeaderOverrides []CTHeaderOverride      `json:"headerOverrides,omitempty"`
	OAuthToken      *gcpOAuthToken          `json:"oauthToken,omitempty"`
	OidcToken       *gcpOIDCToken           `json:"oidcToken,omitempty"`
}

type CTURIOverride struct {
	Scheme       protoEnum[ctURIScheme] `json:"scheme,omitempty"`
	Host         *string                `json:"host,omitempty"`
	Port         string                 `json:"port,omitempty"`
	PathOverride *struct {
		Path string `json:"path"`
	} `json:"pathOverride,omitempty"`
	QueryOverride *struct {
		QueryParams string `json:"queryParams"`
	} `json:"queryOverride,omitempty"`
	URIOverrideEnforceMode protoEnum[ctURIEnforceMode] `json:"uriOverrideEnforceMode,omitempty"`
}

type CTHeaderOverride struct {
	Header struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"header"`
}

// CTTask is a task as the API shows it.
type CTTask struct {
	Name                 string                `json:"name"`
	AppEngineHTTPRequest map[string]any        `json:"appEngineHttpRequest,omitempty"`
	HTTPRequest          *CTHTTPRequest        `json:"httpRequest,omitempty"`
	ScheduleTime         string                `json:"scheduleTime,omitempty"`
	CreateTime           string                `json:"createTime,omitempty"`
	DispatchDeadline     string                `json:"dispatchDeadline,omitempty"`
	DispatchCount        int                   `json:"dispatchCount,omitempty"`
	ResponseCount        int                   `json:"responseCount,omitempty"`
	FirstAttempt         *CTAttempt            `json:"firstAttempt,omitempty"`
	LastAttempt          *CTAttempt            `json:"lastAttempt,omitempty"`
	View                 protoEnum[ctTaskView] `json:"view,omitempty"`
	RetryConfig          *CTRetryConfig        `json:"retryConfig,omitempty"`
}

type CTHTTPRequest struct {
	URL        string                  `json:"url,omitempty"`
	HTTPMethod protoEnum[ctHTTPMethod] `json:"httpMethod,omitempty"`
	Headers    map[string]string       `json:"headers,omitempty"`
	Body       string                  `json:"body,omitempty"`
	OAuthToken *gcpOAuthToken          `json:"oauthToken,omitempty"`
	OidcToken  *gcpOIDCToken           `json:"oidcToken,omitempty"`
}

// CTAttempt is the status of one task attempt.
type CTAttempt struct {
	ScheduleTime   string    `json:"scheduleTime,omitempty"`
	DispatchTime   string    `json:"dispatchTime,omitempty"`
	ResponseTime   string    `json:"responseTime,omitempty"`
	ResponseStatus *CSStatus `json:"responseStatus,omitempty"`
}

// ctStored is a task as its queue holds it: the API's view and the counts the
// X-CloudTasks headers report, which the API does not show.
type ctStored struct {
	Task CTTask `json:"task"`
	// Executions counts the attempts the handler answered with anything but
	// a 5xx.
	Executions       int `json:"executions,omitempty"`
	PreviousResponse int `json:"previousResponse,omitempty"`
}

// ctBacklog holds one queue's tasks.
type ctBacklog struct {
	Tasks msgq.Queue[ctStored] `json:"tasks"`
}

type ctQueueState struct{}

func (ctQueueState) enumNames() map[int32]string {
	return map[int32]string{1: "RUNNING", 2: "PAUSED", 3: "DISABLED"}
}

type ctHTTPMethod struct{}

func (ctHTTPMethod) enumNames() map[int32]string {
	return map[int32]string{1: "POST", 2: "GET", 3: "HEAD", 4: "PUT", 5: "DELETE", 6: "PATCH", 7: "OPTIONS"}
}

type ctURIScheme struct{}

func (ctURIScheme) enumNames() map[int32]string { return map[int32]string{1: "HTTP", 2: "HTTPS"} }

type ctURIEnforceMode struct{}

func (ctURIEnforceMode) enumNames() map[int32]string {
	return map[int32]string{1: "IF_NOT_EXISTS", 2: "ALWAYS"}
}

type ctTaskView struct{}

func (ctTaskView) enumNames() map[int32]string { return map[int32]string{1: "BASIC", 2: "FULL"} }

var (
	ctQueues   sim.Store[CTQueue]
	ctBacklogs sim.Store[ctBacklog]
)

// The defaults and limits configuring-queues, the queue reference and the
// quotas page state.
const (
	ctDefaultMaxDispatchesPerSecond  = 500.0
	ctMaxDispatchesPerSecond         = 500.0
	ctDefaultMaxConcurrentDispatches = 1000
	ctMaxConcurrentDispatches        = 5000
	ctDefaultMaxAttempts             = 100
	ctDefaultMinBackoff              = "0.100s"
	ctDefaultMaxBackoff              = "3600s"
	ctDefaultMaxDoublings            = 16
	ctDefaultDispatchDeadline        = 10 * time.Minute
	ctMinDispatchDeadline            = 15 * time.Second
	ctMaxDispatchDeadline            = 30 * time.Minute
	ctTaskRetention                  = 31 * 24 * time.Hour
	ctMaxScheduleAhead               = 30 * 24 * time.Hour
	ctDedupWindow                    = 24 * time.Hour
	ctMaxTaskSize                    = 1 << 20
	ctMaxBatchCreate                 = 100
	ctMaxBatchDelete                 = 1000
	ctMaxURLLength                   = 2083
	ctCmekUnservedWhy                = "the simulator encrypts no Cloud Tasks queue with a customer-managed Cloud KMS key"
	ctAppEngineUnservedWhy           = "the simulator serves no App Engine application for an App Engine task to reach"
	ctBufferUnservedWhy              = "the simulator does not serve BufferTask: whether the request body binds raw or as a BufferTaskRequest is not sourced"
)

var (
	ctQueueIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,100}$`)
	ctTaskIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,500}$`)
)

// ctPolicy is the msgq configuration every queue shares: tasks live at most
// ctTaskRetention, and a dispatch's receipt stays good until the task is
// dispatched again, however long the handler took to answer.
var ctPolicy = msgq.Policy{Retention: ctTaskRetention, ReceiptOutlivesLease: true}

func registerCloudTasks(srv *sim.Server) {
	ctQueues = sim.MakeStore[CTQueue](srv.DB(), "cloudtasks_queues")
	ctBacklogs = sim.MakeStore[ctBacklog](srv.DB(), "cloudtasks_backlogs")
	ctRun = newCTRunner(srv)

	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues", handleCTCreateQueue)
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues", handleCTListQueues)
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}", handleCTGetQueue)
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/queues/{queue}", handleCTPatchQueue)
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}", handleCTDeleteQueue)
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queueAction}", handleCTQueueAction)

	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", handleCTCreateTask)
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", handleCTListTasks)
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks:batchCreate", handleCTBatchCreate)
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks:batchDelete", handleCTBatchDelete)
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks:buffer", handleCTBuffer)
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", handleCTGetTask)
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", handleCTDeleteTask)
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{taskAction}", handleCTTaskAction)

	cmek := func(w http.ResponseWriter, r *http.Request) {
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no cloudtasks cmekConfig: %s", ctCmekUnservedWhy)
	}
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/cmekConfig", cmek)
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/cmekConfig", cmek)
}

func ctParent(r *http.Request) string {
	return fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

func ctQueueName(r *http.Request, id string) string { return ctParent(r) + "/queues/" + id }

func ctNotFound(w http.ResponseWriter, what, name string) {
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %s does not exist", what, name)
}

func ctInvalid(w http.ResponseWriter, format string, args ...any) {
	GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", format, args...)
}

// ctDuration renders d as protojson renders a google.protobuf.Duration.
func ctDuration(d time.Duration) string {
	secs := d / time.Second
	nanos := d % time.Second
	switch {
	case nanos == 0:
		return fmt.Sprintf("%ds", secs)
	case nanos%time.Millisecond == 0:
		return fmt.Sprintf("%d.%03ds", secs, nanos/time.Millisecond)
	case nanos%time.Microsecond == 0:
		return fmt.Sprintf("%d.%06ds", secs, nanos/time.Microsecond)
	}
	return fmt.Sprintf("%d.%09ds", secs, nanos)
}

// ctNormalizeDuration reads a duration field and writes it back canonically;
// a zero duration reads as unset, as proto3 has no other spelling for it.
func ctNormalizeDuration(field string, value *string) error {
	if *value == "" {
		return nil
	}
	d, err := psParseDuration(*value)
	if err != nil {
		return fmt.Errorf("%s: %v", field, err)
	}
	*value = ""
	if d > 0 {
		*value = ctDuration(d)
	}
	return nil
}

func ctMustDuration(value string) time.Duration {
	d, _ := psParseDuration(value)
	return d
}

// ctBurstSize is the maxBurstSize Cloud Tasks derives from a dispatch rate:
// at least the rate, as the RateLimits reference states, and 100 at the
// default 500 per second, as the configuring-queues page shows.
func ctBurstSize(rate float64) int {
	return max(1, min(100, int(math.Ceil(rate))))
}

// ctValidateQueue checks a queue's configuration and fills what it leaves
// unset with Cloud Tasks' defaults.
func ctValidateQueue(q *CTQueue) error {
	rl := q.RateLimits
	if rl == nil {
		rl = &CTRateLimits{}
		q.RateLimits = rl
	}
	switch {
	case rl.MaxDispatchesPerSecond < 0 || rl.MaxDispatchesPerSecond > ctMaxDispatchesPerSecond:
		return fmt.Errorf("rateLimits.maxDispatchesPerSecond must be between 0 and %g", ctMaxDispatchesPerSecond)
	case rl.MaxConcurrentDispatches < 0 || rl.MaxConcurrentDispatches > ctMaxConcurrentDispatches:
		return fmt.Errorf("rateLimits.maxConcurrentDispatches must be between 0 and %d", ctMaxConcurrentDispatches)
	}
	if rl.MaxDispatchesPerSecond == 0 {
		rl.MaxDispatchesPerSecond = ctDefaultMaxDispatchesPerSecond
	}
	if rl.MaxConcurrentDispatches == 0 {
		rl.MaxConcurrentDispatches = ctDefaultMaxConcurrentDispatches
	}
	rl.MaxBurstSize = ctBurstSize(rl.MaxDispatchesPerSecond)

	if q.RetryConfig == nil {
		q.RetryConfig = &CTRetryConfig{}
	}
	if err := ctValidateRetryConfig("retryConfig", q.RetryConfig); err != nil {
		return err
	}
	if s := q.StackdriverLoggingConfig; s != nil && (s.SamplingRatio < 0 || s.SamplingRatio > 1) {
		return fmt.Errorf("stackdriverLoggingConfig.samplingRatio must be between 0.0 and 1.0")
	}
	if t := q.HTTPTarget; t != nil {
		if o := t.URIOverride; o != nil {
			if o.Host != nil && *o.Host == "" {
				return fmt.Errorf("httpTarget.uriOverride.host must not be empty")
			}
			if o.Port != "" {
				var port int
				if _, err := fmt.Sscan(o.Port, &port); err != nil || port < 0 || port > 65535 {
					return fmt.Errorf("httpTarget.uriOverride.port %q is not a port", o.Port)
				}
			}
		}
		if t.OAuthToken != nil && t.OidcToken != nil {
			return fmt.Errorf("httpTarget takes one of oauthToken and oidcToken")
		}
		if t.OAuthToken != nil && (t.OAuthToken.ServiceAccountEmail == "" || t.OAuthToken.Scope == "") {
			return fmt.Errorf("a queue-level oauthToken needs both serviceAccountEmail and scope")
		}
		if t.OidcToken != nil && (t.OidcToken.ServiceAccountEmail == "" || t.OidcToken.Audience == "") {
			return fmt.Errorf("a queue-level oidcToken needs both serviceAccountEmail and audience")
		}
		for _, h := range t.HeaderOverrides {
			if h.Header.Key == "" {
				return fmt.Errorf("httpTarget.headerOverrides needs a key for every header")
			}
		}
	}
	return nil
}

// ctValidateRetryConfig checks a queue's or a task's retry configuration and
// fills the fields it leaves unset with the defaults.
func ctValidateRetryConfig(field string, rc *CTRetryConfig) error {
	if rc.MaxAttempts < -1 {
		return fmt.Errorf("%s.maxAttempts must be at least -1", field)
	}
	if rc.MaxDoublings < 0 {
		return fmt.Errorf("%s.maxDoublings must not be negative", field)
	}
	for name, value := range map[string]*string{"maxRetryDuration": &rc.MaxRetryDuration, "minBackoff": &rc.MinBackoff, "maxBackoff": &rc.MaxBackoff} {
		if err := ctNormalizeDuration(field+"."+name, value); err != nil {
			return err
		}
	}
	if rc.MaxAttempts == 0 {
		rc.MaxAttempts = ctDefaultMaxAttempts
	}
	if rc.MinBackoff == "" {
		rc.MinBackoff = ctDefaultMinBackoff
	}
	if rc.MaxBackoff == "" {
		rc.MaxBackoff = ctDefaultMaxBackoff
	}
	if rc.MaxDoublings == 0 {
		rc.MaxDoublings = ctDefaultMaxDoublings
	}
	if ctMustDuration(rc.MinBackoff) > ctMustDuration(rc.MaxBackoff) {
		return fmt.Errorf("%s.minBackoff must not exceed maxBackoff", field)
	}
	return nil
}

func ctReadQueue(r *http.Request) (CTQueue, error) {
	var q CTQueue
	err := sim.ReadJSON(r, &q)
	return q, err
}

func handleCTCreateQueue(w http.ResponseWriter, r *http.Request) {
	q, err := ctReadQueue(r)
	if err != nil {
		ctInvalid(w, "invalid request body: %v", err)
		return
	}
	parent := ctParent(r)
	id, ok := strings.CutPrefix(q.Name, parent+"/queues/")
	if !ok || !ctQueueIDPattern.MatchString(id) {
		ctInvalid(w, "queue name %q is not %s/queues/QUEUE_ID with a QUEUE_ID of up to 100 letters, numbers or hyphens", q.Name, parent)
		return
	}
	if q.AppEngineRoutingOverride != nil {
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "%s", ctAppEngineUnservedWhy)
		return
	}
	if err := ctValidateQueue(&q); err != nil {
		ctInvalid(w, "%v", err)
		return
	}
	q.State, q.PurgeTime = "RUNNING", ""
	created := false
	ctQueues.Upsert(q.Name, func(existing *CTQueue) {
		if existing.Name != "" {
			return
		}
		*existing, created = q, true
	})
	if !created {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Queue %s already exists", q.Name)
		return
	}
	ctBacklogs.Upsert(q.Name, func(*ctBacklog) {})
	sim.WriteJSON(w, http.StatusOK, q)
}

func handleCTGetQueue(w http.ResponseWriter, r *http.Request) {
	name := ctQueueName(r, sim.PathParam(r, "queue"))
	q, ok := ctQueues.Get(name)
	if !ok {
		ctNotFound(w, "Queue", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, q)
}

func handleCTListQueues(w http.ResponseWriter, r *http.Request) {
	filter, err := gcpParseFilterExpr(r.URL.Query().Get("filter"))
	if err != nil {
		ctInvalid(w, "%v", err)
		return
	}
	var queues []CTQueue
	for _, row := range ctQueues.ListPrefix(ctParent(r) + "/queues/") {
		doc, err := listq.ToDoc(row.Item)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
			return
		}
		if filter.Eval(doc) {
			queues = append(queues, row.Item)
		}
	}
	page, next, ok := paginateList(w, r, queues, gcpAsc(func(q CTQueue) string { return q.Name }))
	if !ok {
		return
	}
	resp := map[string]any{"queues": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ctCamelPath reads an update mask path, in either the snake_case of the
// proto or the lowerCamelCase of its JSON mapping, as lowerCamelCase.
func ctCamelPath(path string) string {
	var b strings.Builder
	upper := false
	for _, c := range strings.TrimSpace(path) {
		if c == '_' {
			upper = true
			continue
		}
		if upper {
			c = unicode.ToUpper(c)
			upper = false
		}
		b.WriteRune(c)
	}
	return b.String()
}

// ctQueueMaskable names the update mask paths UpdateQueue honours and the
// change each makes.
var ctQueueMaskable = map[string]func(dst, src *CTQueue){
	"rateLimits":  func(dst, src *CTQueue) { dst.RateLimits = src.RateLimits },
	"retryConfig": func(dst, src *CTQueue) { dst.RetryConfig = src.RetryConfig },
	"httpTarget":  func(dst, src *CTQueue) { dst.HTTPTarget = src.HTTPTarget },
	"stackdriverLoggingConfig": func(dst, src *CTQueue) {
		dst.StackdriverLoggingConfig = src.StackdriverLoggingConfig
	},
	"appEngineRoutingOverride": func(dst, src *CTQueue) { dst.AppEngineRoutingOverride = src.AppEngineRoutingOverride },
	"rateLimits.maxDispatchesPerSecond": func(dst, src *CTQueue) {
		ctRateLimitsOf(dst).MaxDispatchesPerSecond = ctRateLimitsOf(src).MaxDispatchesPerSecond
	},
	"rateLimits.maxConcurrentDispatches": func(dst, src *CTQueue) {
		ctRateLimitsOf(dst).MaxConcurrentDispatches = ctRateLimitsOf(src).MaxConcurrentDispatches
	},
	"rateLimits.maxBurstSize": func(*CTQueue, *CTQueue) {},
	"retryConfig.maxAttempts": func(dst, src *CTQueue) { ctRetryOf(dst).MaxAttempts = ctRetryOf(src).MaxAttempts },
	"retryConfig.maxRetryDuration": func(dst, src *CTQueue) {
		ctRetryOf(dst).MaxRetryDuration = ctRetryOf(src).MaxRetryDuration
	},
	"retryConfig.minBackoff":   func(dst, src *CTQueue) { ctRetryOf(dst).MinBackoff = ctRetryOf(src).MinBackoff },
	"retryConfig.maxBackoff":   func(dst, src *CTQueue) { ctRetryOf(dst).MaxBackoff = ctRetryOf(src).MaxBackoff },
	"retryConfig.maxDoublings": func(dst, src *CTQueue) { ctRetryOf(dst).MaxDoublings = ctRetryOf(src).MaxDoublings },
	"stackdriverLoggingConfig.samplingRatio": func(dst, src *CTQueue) {
		dst.StackdriverLoggingConfig = src.StackdriverLoggingConfig
	},
	"httpTarget.uriOverride": func(dst, src *CTQueue) { ctTargetOf(dst).URIOverride = ctTargetOf(src).URIOverride },
	"httpTarget.httpMethod":  func(dst, src *CTQueue) { ctTargetOf(dst).HTTPMethod = ctTargetOf(src).HTTPMethod },
	"httpTarget.headerOverrides": func(dst, src *CTQueue) {
		ctTargetOf(dst).HeaderOverrides = ctTargetOf(src).HeaderOverrides
	},
	"httpTarget.oauthToken": func(dst, src *CTQueue) { ctTargetOf(dst).OAuthToken = ctTargetOf(src).OAuthToken },
	"httpTarget.oidcToken":  func(dst, src *CTQueue) { ctTargetOf(dst).OidcToken = ctTargetOf(src).OidcToken },
}

func ctRateLimitsOf(q *CTQueue) *CTRateLimits {
	if q.RateLimits == nil {
		q.RateLimits = &CTRateLimits{}
	}
	return q.RateLimits
}

func ctRetryOf(q *CTQueue) *CTRetryConfig {
	if q.RetryConfig == nil {
		q.RetryConfig = &CTRetryConfig{}
	}
	return q.RetryConfig
}

func ctTargetOf(q *CTQueue) *CTHTTPTarget {
	if q.HTTPTarget == nil {
		q.HTTPTarget = &CTHTTPTarget{}
	}
	return q.HTTPTarget
}

// handleCTPatchQueue serves UpdateQueue, which creates the queue when it does
// not exist. An empty mask updates every field.
func handleCTPatchQueue(w http.ResponseWriter, r *http.Request) {
	name := ctQueueName(r, sim.PathParam(r, "queue"))
	if !ctQueueIDPattern.MatchString(sim.PathParam(r, "queue")) {
		ctInvalid(w, "queue name %q has a QUEUE_ID that is not up to 100 letters, numbers or hyphens", name)
		return
	}
	patch, err := ctReadQueue(r)
	if err != nil {
		ctInvalid(w, "invalid request body: %v", err)
		return
	}
	if patch.Name != "" && patch.Name != name {
		ctInvalid(w, "queue.name %q does not match the queue %s the request addresses", patch.Name, name)
		return
	}
	existing, exists := ctQueues.Get(name)
	updated := existing
	if !exists {
		updated = CTQueue{Name: name, State: "RUNNING"}
	}
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		for _, path := range strings.Split(mask, ",") {
			apply, known := ctQueueMaskable[ctCamelPath(path)]
			if !known {
				ctInvalid(w, "updateMask path %q names no field UpdateQueue changes", path)
				return
			}
			apply(&updated, &patch)
		}
	} else {
		state, purge := updated.State, updated.PurgeTime
		updated = patch
		updated.Name, updated.State, updated.PurgeTime = name, state, purge
	}
	if updated.AppEngineRoutingOverride != nil {
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "%s", ctAppEngineUnservedWhy)
		return
	}
	if err := ctValidateQueue(&updated); err != nil {
		ctInvalid(w, "%v", err)
		return
	}
	ctQueues.Upsert(name, func(q *CTQueue) {
		state, purge := updated.State, updated.PurgeTime
		if q.Name != "" {
			state, purge = q.State, q.PurgeTime
		}
		*q = updated
		q.State, q.PurgeTime = state, purge
		updated = *q
	})
	ctBacklogs.Upsert(name, func(*ctBacklog) {})
	ctRun.kick(name)
	sim.WriteJSON(w, http.StatusOK, updated)
}

func handleCTDeleteQueue(w http.ResponseWriter, r *http.Request) {
	name := ctQueueName(r, sim.PathParam(r, "queue"))
	if !ctQueues.Delete(name) {
		ctNotFound(w, "Queue", name)
		return
	}
	ctBacklogs.Delete(name)
	ctRun.forget(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleCTQueueAction serves the queue colon verbs: pause, resume, purge and
// the IAM triple.
func handleCTQueueAction(w http.ResponseWriter, r *http.Request) {
	id, action, _ := strings.Cut(sim.PathParam(r, "queueAction"), ":")
	name := ctQueueName(r, id)
	switch action {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		if _, ok := ctQueues.Get(name); !ok && action != "testIamPermissions" {
			ctNotFound(w, "Queue", name)
			return
		}
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, action)
		return
	case "pause", "resume", "purge":
	default:
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown queue method %q", action)
		return
	}
	var q CTQueue
	now := time.Now().UTC()
	if !ctQueues.Update(name, func(stored *CTQueue) {
		switch action {
		case "pause":
			stored.State = "PAUSED"
		case "resume":
			stored.State = "RUNNING"
		case "purge":
			stored.PurgeTime = protoJSONTimestamp(now.Truncate(time.Microsecond))
		}
		q = *stored
	}) {
		ctNotFound(w, "Queue", name)
		return
	}
	switch action {
	case "purge":
		ctBacklogs.Update(name, func(b *ctBacklog) {
			purged := b.Tasks.Remove(func(m msgq.Message[ctStored]) bool {
				return m.EnqueuedAt <= now.UnixMilli()
			})
			for _, m := range purged {
				ctRemember(b, m, now)
			}
		})
	case "resume":
		ctRun.kick(name)
	}
	sim.WriteJSON(w, http.StatusOK, q)
}

// ctRemember keeps a task's id from being reused for the deduplication window
// after the task leaves its queue, by deletion, success, exhaustion, purge or
// expiry alike.
func ctRemember(b *ctBacklog, m msgq.Message[ctStored], now time.Time) {
	if b.Tasks.Dedup == nil {
		b.Tasks.Dedup = msgq.Dedup{}
	}
	b.Tasks.Dedup[m.ID] = msgq.DedupRecord{ID: m.ID, Seq: m.Seq, ExpiresAt: now.Add(ctDedupWindow).UnixMilli()}
}
