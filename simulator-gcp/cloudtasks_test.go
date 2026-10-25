package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

const ctTestParent = "/v2/projects/unit-proj/locations/us-central1"

type ctReceived struct {
	method string
	path   string
	header http.Header
	body   string
	at     time.Time
}

// ctReceiver answers the nth request it receives (from 0) with answer(n) and
// hands every request to the test; release, when set, holds each answer until
// the test sends on it.
func ctReceiver(t *testing.T, answer func(n int) int, release chan struct{}) (*httptest.Server, chan ctReceived) {
	t.Helper()
	received := make(chan ctReceived, 32)
	var n atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- ctReceived{method: r.Method, path: r.URL.RequestURI(), header: r.Header.Clone(), body: string(body), at: time.Now()}
		if release != nil {
			<-release
		}
		w.WriteHeader(answer(int(n.Add(1)) - 1))
	}))
	t.Cleanup(server.Close)
	return server, received
}

func ctAwait(t *testing.T, received chan ctReceived) ctReceived {
	t.Helper()
	select {
	case r := <-received:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the target received no task")
		return ctReceived{}
	}
}

func ctCall(t *testing.T, srv *sim.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func ctQueueBody(id, extra string) string {
	body := `{"name":"projects/unit-proj/locations/us-central1/queues/` + id + `"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func ctTaskBody(url, extra string) string {
	body := `{"task":{"httpRequest":{"url":"` + url + `"}`
	if extra != "" {
		body += "," + extra
	}
	return body + "}}"
}

func TestCloudTasksBackoffFollowsTheDocumentedSequence(t *testing.T) {
	backoff := googleRetryBackoff(10*time.Second, 300*time.Second, 3)
	want := []time.Duration{10, 20, 40, 80, 160, 240, 300, 300}
	for i, w := range want {
		if got := backoff(i + 1); got != w*time.Second {
			t.Errorf("retry %d waits %v, want %v", i+1, got, w*time.Second)
		}
	}
}

func TestCloudTasksStopsRetryingOnceBothLimitsAreReached(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rc         CTRetryConfig
		attempts   int
		sinceFirst time.Duration
		exhausted  bool
	}{
		{"attempts left", CTRetryConfig{MaxAttempts: 3}, 2, time.Hour, false},
		{"attempts used, unlimited duration", CTRetryConfig{MaxAttempts: 3}, 3, 0, true},
		{"attempts used, duration left", CTRetryConfig{MaxAttempts: 3, MaxRetryDuration: "60s"}, 3, 30 * time.Second, false},
		{"both used", CTRetryConfig{MaxAttempts: 3, MaxRetryDuration: "60s"}, 3, time.Minute, true},
		{"unlimited attempts, duration passed", CTRetryConfig{MaxAttempts: -1, MaxRetryDuration: "60s"}, 50, time.Minute, true},
		{"both unlimited", CTRetryConfig{MaxAttempts: -1}, 1000, 30 * 24 * time.Hour, false},
	} {
		if got := ctExhausted(tc.rc, tc.attempts, tc.sinceFirst); got != tc.exhausted {
			t.Errorf("%s: exhausted = %v, want %v", tc.name, got, tc.exhausted)
		}
	}
}

func TestCloudTasksBurstSizeFollowsTheRate(t *testing.T) {
	for rate, want := range map[float64]int{0.5: 1, 1: 1, 2.5: 3, 10: 10, 100: 100, 500: 100} {
		if got := ctBurstSize(rate); got != want {
			t.Errorf("ctBurstSize(%g) = %d, want %d", rate, got, want)
		}
	}
}

func TestCloudTasksURIOverride(t *testing.T) {
	host := "override.example"
	always := CTURIOverride{Host: &host, Port: "8443", PathOverride: &struct {
		Path string `json:"path"`
	}{Path: "/new"}}
	if got := ctOverrideURI("http://task.example/old?q=1", always); got != "https://override.example:8443/new?q=1" {
		t.Errorf("ALWAYS = %s", got)
	}
	always.Scheme = "HTTP"
	always.Port = "0"
	if got := ctOverrideURI("https://task.example:9000/old", always); got != "http://override.example/new" {
		t.Errorf("ALWAYS with HTTP and port 0 = %s", got)
	}
	ifMissing := CTURIOverride{Host: &host, Port: "8443", URIOverrideEnforceMode: "IF_NOT_EXISTS",
		QueryOverride: &struct {
			QueryParams string `json:"queryParams"`
		}{QueryParams: "from=queue"}}
	if got := ctOverrideURI("http://task.example/old", ifMissing); got != "http://task.example:8443/old?from=queue" {
		t.Errorf("IF_NOT_EXISTS = %s", got)
	}
}

func buildCloudTasksTestSimulator(t *testing.T, cfg sim.Config) *sim.Server {
	t.Helper()
	bg.Await()
	t.Setenv("SIM_RUNTIME", "process")
	cfg.Provider, cfg.ListenAddr, cfg.LogLevel = "gcp", ":0", "error"
	srv, err := buildSimulator(cfg)
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(bg.Await)
	t.Cleanup(srv.StopBackground)
	return srv
}

// TestCloudTasksDispatchesWithHeadersAndRetriesWithBackoff proves an HTTP
// task reaches its target with its method, headers and body and the
// X-CloudTasks headers, and that a non-2xx answer is retried after the
// queue's backoff until the target accepts it.
func TestCloudTasksDispatchesWithHeadersAndRetriesWithBackoff(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	statuses := []int{http.StatusNotFound, http.StatusServiceUnavailable, http.StatusOK}
	target, received := ctReceiver(t, func(n int) int { return statuses[min(n, len(statuses)-1)] }, nil)
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("retrying",
		`"retryConfig":{"maxAttempts":5,"minBackoff":"0.2s","maxBackoff":"10s","maxDoublings":1}`))
	body := base64.StdEncoding.EncodeToString([]byte(`{"order":7}`))
	code, task := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/retrying/tasks",
		`{"task":{"name":"projects/unit-proj/locations/us-central1/queues/retrying/tasks/order-7",
		"httpRequest":{"url":"`+target.URL+`/orders","httpMethod":"PUT","headers":{"Content-Type":"application/json","X-Team":"fulfilment"},"body":"`+body+`"}}}`)
	if code != http.StatusOK || task["dispatchDeadline"] != "600s" || task["view"] != "BASIC" {
		t.Fatalf("create task → %d %v", code, task)
	}

	first := ctAwait(t, received)
	if first.method != http.MethodPut || first.path != "/orders" || first.body != `{"order":7}` ||
		first.header.Get("Content-Type") != "application/json" || first.header.Get("X-Team") != "fulfilment" {
		t.Fatalf("first attempt = %+v", first)
	}
	for header, want := range map[string]string{
		"User-Agent": "Google-Cloud-Tasks", "X-CloudTasks-QueueName": "retrying", "X-CloudTasks-TaskName": "order-7",
		"X-CloudTasks-TaskRetryCount": "0", "X-CloudTasks-TaskExecutionCount": "0", "X-CloudTasks-TaskPreviousResponse": "",
	} {
		if got := first.header.Get(header); got != want {
			t.Errorf("first attempt %s = %q, want %q", header, got, want)
		}
	}
	if first.header.Get("X-CloudTasks-TaskETA") == "" {
		t.Error("the first attempt carries no X-CloudTasks-TaskETA")
	}

	second := ctAwait(t, received)
	if gap := second.at.Sub(first.at); gap < 200*time.Millisecond {
		t.Errorf("the first retry came %v after the attempt, before the 0.2s minBackoff", gap)
	}
	if second.header.Get("X-CloudTasks-TaskRetryCount") != "1" || second.header.Get("X-CloudTasks-TaskExecutionCount") != "1" ||
		second.header.Get("X-CloudTasks-TaskPreviousResponse") != "404" {
		t.Errorf("second attempt headers = %v", second.header)
	}
	third := ctAwait(t, received)
	if gap := third.at.Sub(second.at); gap < 400*time.Millisecond {
		t.Errorf("the second retry came %v after the first, before the doubled 0.4s", gap)
	}
	if third.header.Get("X-CloudTasks-TaskExecutionCount") != "1" || third.header.Get("X-CloudTasks-TaskPreviousResponse") != "503" {
		t.Errorf("a 5xx answer counts as an execution: third attempt headers = %v", third.header)
	}
	bg.Await()
	if code, _ := ctCall(t, srv, http.MethodGet, ctTestParent+"/queues/retrying/tasks/order-7", ""); code != http.StatusNotFound {
		t.Errorf("a task its target accepted is still in the queue: GET → %d", code)
	}
	if code, _ := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/retrying/tasks",
		`{"task":{"name":"projects/unit-proj/locations/us-central1/queues/retrying/tasks/order-7","httpRequest":{"url":"`+target.URL+`"}}}`); code != http.StatusConflict {
		t.Errorf("re-creating an executed task's name → %d, want 409 inside the deduplication window", code)
	}
}

// TestCloudTasksDeletesATaskWhoseAttemptsRunOut proves a task that fails
// maxAttempts times is deleted and its last attempt recorded until then.
func TestCloudTasksDeletesATaskWhoseAttemptsRunOut(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	target, received := ctReceiver(t, func(int) int { return http.StatusInternalServerError }, nil)
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("failing",
		`"retryConfig":{"maxAttempts":2,"minBackoff":"0.1s","maxBackoff":"0.1s"}`))
	_, task := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/failing/tasks", ctTaskBody(target.URL, ""))
	name, _ := task["name"].(string)
	id := name[strings.LastIndex(name, "/")+1:]
	if strings.Trim(id, "0123456789") != "" {
		t.Errorf("an unnamed task got the id %q, not a generated number", id)
	}
	ctAwait(t, received)
	ctAwait(t, received)
	bg.Await()
	if code, _ := ctCall(t, srv, http.MethodGet, "/v2/"+name, ""); code != http.StatusNotFound {
		t.Errorf("a task with no attempts left is still in the queue: GET → %d", code)
	}
}

// TestCloudTasksRateLimitSpacesDispatches proves the token bucket: a burst of
// maxBurstSize tasks goes out at once and the rest at maxDispatchesPerSecond.
func TestCloudTasksRateLimitSpacesDispatches(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	target, received := ctReceiver(t, func(int) int { return http.StatusOK }, nil)
	_, queue := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("throttled",
		`"rateLimits":{"maxDispatchesPerSecond":2}`))
	if rl, _ := queue["rateLimits"].(map[string]any); rl["maxBurstSize"] != float64(2) || rl["maxConcurrentDispatches"] != float64(1000) {
		t.Fatalf("rateLimits = %v", queue["rateLimits"])
	}
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/throttled:pause", "{}")
	for range 4 {
		ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/throttled/tasks", ctTaskBody(target.URL, ""))
	}
	start := time.Now()
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/throttled:resume", "{}")
	var at []time.Duration
	for range 4 {
		at = append(at, ctAwait(t, received).at.Sub(start))
	}
	if at[1] > 400*time.Millisecond {
		t.Errorf("the burst of two took %v", at[1])
	}
	if at[2] < 400*time.Millisecond || at[3] < 900*time.Millisecond {
		t.Errorf("dispatches after the burst came at %v and %v, faster than 2 per second", at[2], at[3])
	}
}

// TestCloudTasksConcurrencyLimitHoldsTasksUntilAnAttemptEnds proves no more
// than maxConcurrentDispatches tasks are in flight at once.
func TestCloudTasksConcurrencyLimitHoldsTasksUntilAnAttemptEnds(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	release := make(chan struct{})
	target, received := ctReceiver(t, func(int) int { return http.StatusOK }, release)
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("serial", `"rateLimits":{"maxConcurrentDispatches":1}`))
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/serial/tasks", ctTaskBody(target.URL+"/first", ""))
	first := ctAwait(t, received)
	_, second := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/serial/tasks", ctTaskBody(target.URL+"/second", ""))
	name, _ := second["name"].(string)
	if _, held := ctCall(t, srv, http.MethodGet, "/v2/"+name, ""); held["dispatchCount"] != nil {
		t.Fatalf("a second task was dispatched while the first was in flight: %v", held)
	}
	release <- struct{}{}
	if next := ctAwait(t, received); first.path != "/first" || next.path != "/second" {
		t.Fatalf("dispatched %s then %s", first.path, next.path)
	}
	release <- struct{}{}
}

// TestCloudTasksPausedQueueHoldsTasksAndRunForcesOne proves a paused queue
// dispatches nothing until resumed, and that RunTask dispatches a task
// whatever the queue's state.
func TestCloudTasksPausedQueueHoldsTasksAndRunForcesOne(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	target, received := ctReceiver(t, func(int) int { return http.StatusOK }, nil)
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("held", ""))
	if _, q := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/held:pause", "{}"); q["state"] != "PAUSED" {
		t.Fatalf("pause → %v", q)
	}
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/held/tasks", `{"task":{"name":"projects/unit-proj/locations/us-central1/queues/held/tasks/forced","httpRequest":{"url":"`+target.URL+`/forced"}}}`)
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/held/tasks", `{"task":{"name":"projects/unit-proj/locations/us-central1/queues/held/tasks/waiting","httpRequest":{"url":"`+target.URL+`/waiting"}}}`)
	if _, waiting := ctCall(t, srv, http.MethodGet, ctTestParent+"/queues/held/tasks/waiting", ""); waiting["dispatchCount"] != nil {
		t.Fatalf("a paused queue dispatched a task: %v", waiting)
	}
	code, run := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/held/tasks/forced:run", "{}")
	if code != http.StatusOK || run["dispatchCount"] != float64(1) {
		t.Fatalf("run → %d %v", code, run)
	}
	if got := ctAwait(t, received); got.path != "/forced" {
		t.Fatalf("run dispatched %s", got.path)
	}
	if _, q := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/held:resume", "{}"); q["state"] != "RUNNING" {
		t.Fatalf("resume → %v", q)
	}
	if got := ctAwait(t, received); got.path != "/waiting" {
		t.Fatalf("resume dispatched %s", got.path)
	}
}

// TestCloudTasksPurgeAndNamedTaskDeduplication proves purge deletes the
// queue's tasks and records the purge time, and that a task name stays taken
// while its task exists and after it is deleted or purged.
func TestCloudTasksPurgeAndNamedTaskDeduplication(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("dedup", ""))
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup:pause", "{}")
	named := func(id string) string {
		return `{"task":{"name":"projects/unit-proj/locations/us-central1/queues/dedup/tasks/` + id + `","httpRequest":{"url":"https://hooks.example/` + id + `"}}}`
	}
	if code, _ := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup/tasks", named("a")); code != http.StatusOK {
		t.Fatalf("create a → %d", code)
	}
	if code, _ := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup/tasks", named("a")); code != http.StatusConflict {
		t.Errorf("create a again → %d, want 409", code)
	}
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup/tasks", named("b"))
	if code, _ := ctCall(t, srv, http.MethodDelete, ctTestParent+"/queues/dedup/tasks/b", ""); code != http.StatusOK {
		t.Fatalf("delete b → %d", code)
	}
	if code, _ := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup/tasks", named("b")); code != http.StatusConflict {
		t.Errorf("create a deleted task's name → %d, want 409", code)
	}
	_, purged := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup:purge", "{}")
	if purged["purgeTime"] == nil {
		t.Errorf("purge recorded no purgeTime: %v", purged)
	}
	if _, list := ctCall(t, srv, http.MethodGet, ctTestParent+"/queues/dedup/tasks", ""); len(list["tasks"].([]any)) != 0 {
		t.Errorf("tasks after purge = %v", list["tasks"])
	}
	if code, _ := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/dedup/tasks", named("a")); code != http.StatusConflict {
		t.Errorf("create a purged task's name → %d, want 409", code)
	}
}

// TestCloudTasksScheduledTaskWaitsForItsScheduleTime proves a task scheduled
// ahead is dispatched no earlier than its scheduleTime.
func TestCloudTasksScheduledTaskWaitsForItsScheduleTime(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	target, received := ctReceiver(t, func(int) int { return http.StatusOK }, nil)
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("later", ""))
	at := time.Now().Add(700 * time.Millisecond).UTC()
	_, task := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/later/tasks",
		ctTaskBody(target.URL, `"scheduleTime":"`+at.Format(time.RFC3339Nano)+`"`))
	if task["scheduleTime"] != protoJSONTimestamp(at.Truncate(time.Microsecond)) {
		t.Errorf("scheduleTime = %v, want %s", task["scheduleTime"], at.Format(time.RFC3339Nano))
	}
	got := ctAwait(t, received)
	if got.at.Before(at.Truncate(time.Millisecond)) {
		t.Errorf("dispatched at %v, before the scheduleTime %v", got.at, at)
	}
}

// TestCloudTasksRestartedSimulatorDispatchesStoredTasks proves a simulator
// restarted over its stored state re-arms its queues: a task scheduled before
// the restart is dispatched after it.
func TestCloudTasksRestartedSimulatorDispatchesStoredTasks(t *testing.T) {
	dir := t.TempDir()
	target, received := ctReceiver(t, func(int) int { return http.StatusOK }, nil)
	first := buildCloudTasksTestSimulator(t, sim.Config{Persist: true, DataDir: dir})
	ctCall(t, first, http.MethodPost, ctTestParent+"/queues", ctQueueBody("durable", ""))
	at := time.Now().Add(time.Second).UTC()
	ctCall(t, first, http.MethodPost, ctTestParent+"/queues/durable/tasks", ctTaskBody(target.URL+"/after-restart", `"scheduleTime":"`+at.Format(time.RFC3339Nano)+`"`))
	first.StopBackground()
	bg.Await()

	buildCloudTasksTestSimulator(t, sim.Config{Persist: true, DataDir: dir})
	startCloudTasks()
	if got := ctAwait(t, received); got.path != "/after-restart" || got.at.Before(at.Truncate(time.Millisecond)) {
		t.Fatalf("after the restart the target received %s at %v (scheduled %v)", got.path, got.at, at)
	}
}

// TestCloudTasksQueueOverridesTheTasksTarget proves a queue's httpTarget
// replaces its tasks' URI components, method and headers.
func TestCloudTasksQueueOverridesTheTasksTarget(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	target, received := ctReceiver(t, func(int) int { return http.StatusOK }, nil)
	host, port, _ := strings.Cut(strings.TrimPrefix(target.URL, "http://"), ":")
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("routed",
		`"httpTarget":{"uriOverride":{"scheme":"HTTP","host":"`+host+`","port":"`+port+`","pathOverride":{"path":"/routed"}},
		"httpMethod":"PATCH","headerOverrides":[{"header":{"key":"X-Route","value":"queue"}}]}`))
	_, task := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/routed/tasks", ctTaskBody("https://unreachable.example/original", ""))
	if headers, _ := task["httpRequest"].(map[string]any)["headers"].(map[string]any); headers["X-Route"] != "queue" {
		t.Errorf("the task does not carry the queue's header override: %v", task["httpRequest"])
	}
	got := ctAwait(t, received)
	if got.method != http.MethodPatch || got.path != "/routed" || got.header.Get("X-Route") != "queue" {
		t.Fatalf("dispatched %s %s with %v", got.method, got.path, got.header)
	}
}

// TestCloudTasksBatchCreateAndDelete proves the batch verbs answer a finished
// operation carrying each request's outcome.
func TestCloudTasksBatchCreateAndDelete(t *testing.T) {
	srv := buildCloudTasksTestSimulator(t, sim.Config{})
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues", ctQueueBody("batched", ""))
	ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/batched:pause", "{}")
	task := func(id string) string {
		return `{"task":{"name":"projects/unit-proj/locations/us-central1/queues/batched/tasks/` + id + `","httpRequest":{"url":"https://hooks.example/"}}}`
	}
	// The operations store outlives one simulator build, so each run names
	// its own batch.
	requestID := sim.NewUUID()
	code, op := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/batched/tasks:batchCreate",
		`{"requestId":"`+requestID+`","requests":[`+task("one")+`,`+task("two")+`,`+task("one")+`]}`)
	if code != http.StatusOK || op["done"] != true {
		t.Fatalf("batchCreate → %d %v", code, op)
	}
	metadata, _ := op["metadata"].(map[string]any)
	failed, _ := metadata["failedRequests"].(map[string]any)
	if metadata["state"] != "PARTIALLY_SUCCEEDED" || len(failed) != 1 || failed["2"] == nil {
		t.Errorf("batchCreate metadata = %v", metadata)
	}
	if tasks, _ := op["response"].(map[string]any)["tasks"].([]any); len(tasks) != 2 {
		t.Errorf("batchCreate response = %v", op["response"])
	}
	if _, again := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/batched/tasks:batchCreate",
		`{"requestId":"`+requestID+`","requests":[`+task("three")+`]}`); again["name"] != op["name"] {
		t.Errorf("a retried requestId answered a new operation: %v", again)
	}
	_, deleted := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/batched/tasks:batchDelete",
		`{"names":["projects/unit-proj/locations/us-central1/queues/batched/tasks/one","projects/unit-proj/locations/us-central1/queues/batched/tasks/two"]}`)
	if deleted["metadata"].(map[string]any)["state"] != "SUCCEEDED" {
		t.Errorf("batchDelete = %v", deleted)
	}
	_, none := ctCall(t, srv, http.MethodPost, ctTestParent+"/queues/batched/tasks:batchDelete",
		`{"names":["projects/unit-proj/locations/us-central1/queues/batched/tasks/one"]}`)
	if errorStatus, _ := none["error"].(map[string]any); errorStatus["code"] != float64(10) {
		t.Errorf("a batch whose every delete failed = %v, want an ABORTED error", none)
	}
}
