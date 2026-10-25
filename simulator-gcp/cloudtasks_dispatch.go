package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"google.golang.org/grpc/codes"
)

// ctRunner dispatches the queues' tasks. What it holds in memory is what a
// dispatcher knows while it runs: each queue's token bucket and the tasks it
// has in flight. Everything a restart must keep is in the stores.
type ctRunner struct {
	ctx context.Context

	mu     sync.Mutex
	queues map[string]*ctQueueRun
}

type ctQueueRun struct {
	// leasing serializes the leasing of one queue's tasks, so two wakes
	// cannot both spend the same tokens.
	leasing sync.Mutex

	tokens   float64
	refilled time.Time
	filled   bool
	inFlight map[string]bool
}

var ctRun *ctRunner

// ctArrivals wakes a queue's dispatcher at the next moment a task becomes
// ready by itself — its schedule time or retry time arrives, a dispatch's
// lease left by a stopped simulator runs out, the token bucket refills, a
// task reaches its retention — and ctRunner.kick wakes it when a client acts.
var ctArrivals = msgq.NewWaiter(ctNextChange)

func newCTRunner(srv *sim.Server) *ctRunner {
	ctx, cancel := context.WithCancel(context.Background())
	c := &ctRunner{ctx: ctx, queues: map[string]*ctQueueRun{}}
	srv.StartBackground("Cloud Tasks dispatch", func(serverCtx context.Context) {
		<-serverCtx.Done()
		cancel()
	})
	ctArrivals.OnDue(func(name string) {
		if ctRun != nil {
			ctRun.dispatch(name)
		}
	})
	return c
}

// startCloudTasks wakes every stored queue, so a restarted simulator
// dispatches what its queues hold and re-arms their timers.
func startCloudTasks() {
	for _, q := range ctQueues.List() {
		ctRun.kick(q.Name)
	}
}

func (c *ctRunner) queue(name string) *ctQueueRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, ok := c.queues[name]
	if !ok {
		run = &ctQueueRun{inFlight: map[string]bool{}}
		c.queues[name] = run
	}
	return run
}

func (c *ctRunner) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.queues, name)
}

func (c *ctRunner) inFlight(name, id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, ok := c.queues[name]
	return ok && run.inFlight[id]
}

func (c *ctRunner) inFlightCount(run *ctQueueRun) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(run.inFlight)
}

func (c *ctRunner) setInFlight(run *ctQueueRun, id string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		run.inFlight[id] = true
		return
	}
	delete(run.inFlight, id)
}

// kick dispatches what the queue has ready and arms its next change.
func (c *ctRunner) kick(name string) {
	c.dispatch(name)
	ctArrivals.Arm(name)
}

// refill tops the bucket up at the queue's rate, up to its burst size; a
// bucket starts full.
func (run *ctQueueRun) refill(rl CTRateLimits, now time.Time) {
	burst := float64(rl.MaxBurstSize)
	if !run.filled {
		run.tokens, run.refilled, run.filled = burst, now, true
		return
	}
	run.tokens = min(burst, run.tokens+now.Sub(run.refilled).Seconds()*rl.MaxDispatchesPerSecond)
	run.refilled = now
}

// nextToken is when the bucket next holds a whole token.
func (run *ctQueueRun) nextToken(rl CTRateLimits, now time.Time) time.Time {
	run.refill(rl, now)
	if run.tokens >= 1 {
		return now
	}
	return now.Add(time.Duration((1 - run.tokens) / rl.MaxDispatchesPerSecond * float64(time.Second)))
}

// ctNextChange is when the queue name next has a task to dispatch without
// anyone acting: a paused queue has none.
func ctNextChange(name string, now time.Time) (time.Time, bool) {
	q, ok := ctQueues.Get(name)
	if !ok || q.State != "RUNNING" || ctRun == nil {
		return time.Time{}, false
	}
	b, ok := ctBacklogs.Get(name)
	if !ok {
		return time.Time{}, false
	}
	next, has := b.Tasks.NextChange(ctPolicy, now)
	consider := func(at time.Time) {
		if at.After(now) && (!has || at.Before(next)) {
			next, has = at, true
		}
	}
	ready := false
	for _, m := range b.Tasks.Messages {
		consider(time.UnixMilli(m.EnqueuedAt).Add(ctTaskRetention))
		if !ready && b.Tasks.Available(m, ctPolicy, now) && !ctRun.inFlight(name, m.ID) {
			ready = true
		}
	}
	if ready {
		run := ctRun.queue(name)
		run.leasing.Lock()
		at := run.nextToken(*q.RateLimits, now)
		run.leasing.Unlock()
		consider(at)
	}
	return next, has
}

// dispatch leases as many ready tasks as the queue's token bucket and
// concurrency allow and sends each to its target.
func (c *ctRunner) dispatch(name string) {
	if c.ctx.Err() != nil {
		return
	}
	q, ok := ctQueues.Get(name)
	if !ok || q.State != "RUNNING" {
		return
	}
	run := c.queue(name)
	run.leasing.Lock()
	defer run.leasing.Unlock()
	now := time.Now()
	run.refill(*q.RateLimits, now)
	allowance := min(q.RateLimits.MaxConcurrentDispatches-c.inFlightCount(run), int(run.tokens))
	if allowance <= 0 {
		return
	}
	var leased []msgq.Message[ctStored]
	ctBacklogs.Update(name, func(b *ctBacklog) {
		got := b.Tasks.Receive(msgq.ReceiveOpts[ctStored]{
			Max:    allowance,
			Accept: func(m msgq.Message[ctStored]) bool { return !c.inFlight(name, m.ID) },
		}, ctPolicy, now)
		for _, m := range got.Expired {
			ctRemember(b, m, now)
		}
		for _, l := range got.Leased {
			m := b.Tasks.ByID(l.ID)
			ctStartAttempt(m, now)
			leased = append(leased, *m)
		}
	})
	run.tokens -= float64(len(leased))
	for _, m := range leased {
		c.send(name, run, m)
	}
}

// ctRunTask dispatches a task at once, whatever the queue's state, rate
// limits or the task's own schedule — RunTask's contract.
func ctRunTask(name, id string) (CTTask, bool) {
	now := time.Now()
	var started msgq.Message[ctStored]
	found := false
	ctBacklogs.Update(name, func(b *ctBacklog) {
		m := b.Tasks.ByID(id)
		if m == nil || ctExpired(*m, now) {
			return
		}
		m.Deliveries++
		if m.FirstDeliveredAt == 0 {
			m.FirstDeliveredAt = now.UnixMilli()
		}
		m.Leased, m.Receipt = true, sim.NewUUID()
		ctStartAttempt(m, now)
		started, found = *m, true
	})
	if !found {
		return CTTask{}, false
	}
	ctRun.send(name, ctRun.queue(name), started)
	return started.Payload.Task, true
}

// ctStartAttempt records a dispatch on a leased task and holds the lease for
// the dispatch deadline.
func ctStartAttempt(m *msgq.Message[ctStored], now time.Time) {
	t := &m.Payload.Task
	dispatched := ctTimestamp(now, time.Microsecond)
	t.DispatchCount = m.Deliveries
	if t.FirstAttempt == nil {
		t.FirstAttempt = &CTAttempt{DispatchTime: dispatched}
	}
	t.LastAttempt = &CTAttempt{ScheduleTime: t.ScheduleTime, DispatchTime: dispatched}
	m.AvailableAt = now.Add(ctMustDuration(t.DispatchDeadline)).UnixMilli()
}

func (c *ctRunner) send(name string, run *ctQueueRun, m msgq.Message[ctStored]) {
	c.setInFlight(run, m.ID, true)
	bg.Go(func() {
		settled := c.attempt(name, m)
		c.setInFlight(run, m.ID, false)
		if settled {
			c.kick(name)
		}
	})
}

// attempt sends one dispatch and records its outcome, reporting whether it
// did. A simulator that stops mid-dispatch records nothing: the lease runs
// out and the next start dispatches the task again.
func (c *ctRunner) attempt(name string, m msgq.Message[ctStored]) bool {
	q, ok := ctQueues.Get(name)
	if !ok {
		return false
	}
	outcome := deliverPush(c.ctx, ctRequest(q, m), delivery.Success2xx)
	if c.ctx.Err() != nil {
		return false
	}
	ctSettleAttempt(q, m, outcome, time.Now())
	return true
}

// ctRequest is the request Cloud Tasks sends for a task: the task's own,
// overridden by the queue's httpTarget, with the X-CloudTasks headers and the
// token the target asks for.
func ctRequest(q CTQueue, m msgq.Message[ctStored]) delivery.Request {
	t := m.Payload.Task
	hr := t.HTTPRequest
	target, method := hr.URL, string(hr.HTTPMethod)
	oidc, oauth := hr.OidcToken, hr.OAuthToken
	if ht := q.HTTPTarget; ht != nil {
		if ht.URIOverride != nil {
			target = ctOverrideURI(target, *ht.URIOverride)
		}
		if ht.HTTPMethod != "" {
			method = string(ht.HTTPMethod)
		}
		if ht.OidcToken != nil || ht.OAuthToken != nil {
			oidc, oauth = ht.OidcToken, ht.OAuthToken
		}
	}
	if method == "" {
		method = http.MethodPost
	}
	var body []byte
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		body, _ = base64.StdEncoding.DecodeString(hr.Body)
	}
	header := http.Header{}
	for key, value := range hr.Headers {
		switch canonical := http.CanonicalHeaderKey(key); {
		case canonical == "Host", canonical == "Content-Length",
			strings.HasPrefix(canonical, "X-Google-"), strings.HasPrefix(canonical, "X-Appengine-"):
		default:
			header.Set(key, value)
		}
	}
	header.Set("User-Agent", "Google-Cloud-Tasks")
	header.Set("X-CloudTasks-QueueName", ctShortName(q.Name))
	header.Set("X-CloudTasks-TaskName", ctShortName(t.Name))
	header.Set("X-CloudTasks-TaskRetryCount", strconv.Itoa(max(m.Deliveries-1, 0)))
	header.Set("X-CloudTasks-TaskExecutionCount", strconv.Itoa(m.Payload.Executions))
	if eta, err := time.Parse(time.RFC3339Nano, t.LastAttempt.ScheduleTime); err == nil {
		header.Set("X-CloudTasks-TaskETA", fmt.Sprintf("%d.%06d", eta.Unix(), eta.Nanosecond()/1000))
	}
	if m.Payload.PreviousResponse != 0 {
		header.Set("X-CloudTasks-TaskPreviousResponse", strconv.Itoa(m.Payload.PreviousResponse))
	}
	if auth := gcpPushAuthorization(oidc, oauth, target, time.Now()); auth != "" {
		header.Set("Authorization", auth)
	}
	return delivery.Request{
		Method:  method,
		URL:     target,
		Header:  header,
		Body:    body,
		Timeout: ctMustDuration(t.DispatchDeadline),
	}
}

func ctShortName(name string) string { return name[strings.LastIndex(name, "/")+1:] }

// ctOverrideURI applies a queue's uriOverride to a task's URL: in ALWAYS mode
// (the default) every component the override sets replaces the task's, with
// an unset scheme read as HTTPS; in IF_NOT_EXISTS mode a component replaces
// only one the task's URL leaves out.
func ctOverrideURI(raw string, o CTURIOverride) string {
	u, err := url.Parse(raw)
	if err != nil {
		u = &url.URL{}
	}
	always := o.URIOverrideEnforceMode != "IF_NOT_EXISTS"
	host, port := u.Hostname(), u.Port()
	if always || u.Scheme == "" {
		u.Scheme = "https"
		if o.Scheme == "HTTP" {
			u.Scheme = "http"
		}
	}
	if o.Host != nil && (always || host == "") {
		host = *o.Host
	}
	if o.Port != "" && (always || port == "") {
		port = o.Port
		if port == "0" {
			port = ""
		}
	}
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	if o.PathOverride != nil && (always || u.Path == "" || u.Path == "/") {
		u.Path, u.RawPath = o.PathOverride.Path, ""
	}
	if o.QueryOverride != nil && (always || u.RawQuery == "") {
		u.RawQuery = o.QueryOverride.QueryParams
	}
	return u.String()
}

// ctEffectiveRetry is the retry configuration that governs a task: its own,
// or its queue's.
func ctEffectiveRetry(q CTQueue, t CTTask) CTRetryConfig {
	if t.RetryConfig != nil {
		return *t.RetryConfig
	}
	return *q.RetryConfig
}

// ctExhausted reports whether a failed task has no attempt left: Cloud Tasks
// stops once maxAttempts attempts were made and maxRetryDuration has passed
// since the first, each limit counting as reached when unlimited, and retries
// a task with both unlimited until its retention ends.
func ctExhausted(rc CTRetryConfig, attempts int, sinceFirst time.Duration) bool {
	maxDuration := ctMustDuration(rc.MaxRetryDuration)
	if rc.MaxAttempts == -1 && maxDuration == 0 {
		return false
	}
	attemptsDone := rc.MaxAttempts == -1 || attempts >= rc.MaxAttempts
	durationDone := maxDuration == 0 || sinceFirst >= maxDuration
	return attemptsDone && durationDone
}

// ctBackoff is the wait before retry n under rc.
func ctBackoff(rc CTRetryConfig) func(int) time.Duration {
	return googleRetryBackoff(ctMustDuration(rc.MinBackoff), ctMustDuration(rc.MaxBackoff), rc.MaxDoublings)
}

// ctSettleAttempt records how an attempt ended: a 2xx deletes the task; any
// other answer, or none, schedules the retry its retry configuration allows,
// or deletes the task when none is left. An attempt whose receipt no longer
// names the task — it was deleted, purged or run again — changes nothing.
func ctSettleAttempt(q CTQueue, sent msgq.Message[ctStored], outcome delivery.Outcome, now time.Time) {
	ctBacklogs.Update(q.Name, func(b *ctBacklog) {
		m := b.Tasks.ByReceipt(sent.Receipt, ctPolicy, now)
		if m == nil {
			return
		}
		t := &m.Payload.Task
		status := CSStatus{}
		switch {
		case outcome.Status != 0:
			t.ResponseCount++
			if outcome.Status < 500 {
				m.Payload.Executions++
			}
			m.Payload.PreviousResponse = outcome.Status
			if !outcome.OK() {
				status = CSStatus{Code: rpcCodeForHTTPStatus(outcome.Status), Message: http.StatusText(outcome.Status)}
			}
		case errors.Is(outcome.Err, context.DeadlineExceeded):
			status = CSStatus{Code: int(codes.DeadlineExceeded), Message: "the target did not answer within the dispatch deadline"}
		default:
			status = CSStatus{Code: int(codes.Unavailable), Message: outcome.Err.Error()}
		}
		if t.LastAttempt != nil {
			t.LastAttempt.ResponseTime = ctTimestamp(now, time.Microsecond)
			t.LastAttempt.ResponseStatus = &status
		}
		rc := ctEffectiveRetry(q, *t)
		sinceFirst := now.Sub(time.UnixMilli(m.FirstDeliveredAt))
		if outcome.OK() || ctExhausted(rc, m.Deliveries, sinceFirst) {
			if done, ok := b.Tasks.Settle(sent.Receipt, ctPolicy, now); ok {
				ctRemember(b, done, now)
			}
			return
		}
		wait := ctBackoff(rc)(m.Deliveries)
		t.ScheduleTime = ctTimestamp(now.Add(wait), time.Microsecond)
		b.Tasks.Abandon(sent.Receipt, msgq.Policy{
			ReceiptOutlivesLease: true,
			Backoff:              func(int) time.Duration { return wait },
		}, now)
	})
}
