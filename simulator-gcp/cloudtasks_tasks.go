package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"google.golang.org/grpc/codes"
)

// ctTaskError is a task request Cloud Tasks refuses, in its HTTP status and
// google.rpc code name.
type ctTaskError struct {
	status int
	code   string
	msg    string
}

func ctRefusal(status int, code, format string, args ...any) *ctTaskError {
	return &ctTaskError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

func (e *ctTaskError) write(w http.ResponseWriter) { GCPErrorf(w, e.status, e.code, "%s", e.msg) }

// rpcCode is the google.rpc.Code the refusal carries, for a batch's
// failedRequests.
func (e *ctTaskError) rpcCode() int {
	switch e.code {
	case "INVALID_ARGUMENT":
		return int(codes.InvalidArgument)
	case "NOT_FOUND":
		return int(codes.NotFound)
	case "ALREADY_EXISTS":
		return int(codes.AlreadyExists)
	case "UNIMPLEMENTED":
		return int(codes.Unimplemented)
	}
	return int(codes.Unknown)
}

// ctTaskRequest is CreateTaskRequest's body.
type ctTaskRequest struct {
	Parent       string                `json:"parent,omitempty"`
	Task         CTTask                `json:"task"`
	ResponseView protoEnum[ctTaskView] `json:"responseView,omitempty"`
}

// ctGeneratedTaskID is the id Cloud Tasks gives a task created without a
// name: a string of decimal digits.
func ctGeneratedTaskID() string {
	n, err := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil))
	if err != nil {
		panic(fmt.Errorf("read random task id: %w", err))
	}
	return fmt.Sprintf("%019d", n)
}

// ctTimestamp renders t truncated to the precision a Task field keeps.
func ctTimestamp(t time.Time, precision time.Duration) string {
	return protoJSONTimestamp(t.Truncate(precision))
}

// ctPrepareTask validates a task CreateTask adds to queue q and fills what
// Cloud Tasks sets on creation, returning the task's id.
func ctPrepareTask(q CTQueue, task *CTTask, now time.Time) (string, *ctTaskError) {
	invalid := func(format string, args ...any) (string, *ctTaskError) {
		return "", ctRefusal(http.StatusBadRequest, "INVALID_ARGUMENT", format, args...)
	}
	id := ctGeneratedTaskID()
	if task.Name != "" {
		var ok bool
		id, ok = strings.CutPrefix(task.Name, q.Name+"/tasks/")
		if !ok || !ctTaskIDPattern.MatchString(id) {
			return invalid("task name %q is not %s/tasks/TASK_ID with a TASK_ID of up to 500 letters, numbers, hyphens or underscores", task.Name, q.Name)
		}
	}
	task.Name = q.Name + "/tasks/" + id
	switch {
	case task.AppEngineHTTPRequest != nil:
		return "", ctRefusal(http.StatusNotImplemented, "UNIMPLEMENTED", "%s", ctAppEngineUnservedWhy)
	case task.HTTPRequest == nil:
		return invalid("a task needs an httpRequest or an appEngineHttpRequest")
	}
	hr := task.HTTPRequest
	if !strings.HasPrefix(hr.URL, "http://") && !strings.HasPrefix(hr.URL, "https://") {
		return invalid("httpRequest.url must begin with http:// or https://")
	}
	if len(hr.URL) > ctMaxURLLength {
		return invalid("httpRequest.url must not be longer than %d characters", ctMaxURLLength)
	}
	if hr.HTTPMethod == "" {
		hr.HTTPMethod = http.MethodPost
	}
	body, err := base64.StdEncoding.DecodeString(hr.Body)
	if err != nil {
		return invalid("httpRequest.body is not base64")
	}
	if method := string(hr.HTTPMethod); len(body) > 0 && method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return invalid("a request body is allowed only if the HTTP method is POST, PUT, or PATCH")
	}
	if hr.OAuthToken != nil && hr.OidcToken != nil {
		return invalid("httpRequest takes one of oauthToken and oidcToken")
	}
	if t := q.HTTPTarget; t != nil && len(t.HeaderOverrides) > 0 {
		if hr.Headers == nil {
			hr.Headers = map[string]string{}
		}
		for _, h := range t.HeaderOverrides {
			hr.Headers[h.Header.Key] = h.Header.Value
		}
	}
	size := len(task.Name) + len(hr.URL) + len(body)
	for k, v := range hr.Headers {
		size += len(k) + len(v)
	}
	if size > ctMaxTaskSize {
		return invalid("the task is larger than the maximum task size of 1 MiB")
	}
	deadline := ctDefaultDispatchDeadline
	if task.DispatchDeadline != "" {
		if deadline, err = psParseDuration(task.DispatchDeadline); err != nil {
			return invalid("dispatchDeadline: %v", err)
		}
		if deadline < ctMinDispatchDeadline || deadline > ctMaxDispatchDeadline {
			return invalid("dispatchDeadline must be between 15 seconds and 30 minutes for an HTTP task")
		}
	}
	task.DispatchDeadline = ctDuration(deadline.Truncate(time.Millisecond))
	scheduled := now
	if task.ScheduleTime != "" {
		at, err := time.Parse(time.RFC3339Nano, task.ScheduleTime)
		if err != nil {
			return invalid("scheduleTime %q is not an RFC 3339 timestamp", task.ScheduleTime)
		}
		if at.After(now.Add(ctMaxScheduleAhead)) {
			return invalid("scheduleTime must not be more than 30 days in the future")
		}
		if at.After(now) {
			scheduled = at
		}
	}
	task.ScheduleTime = ctTimestamp(scheduled, time.Microsecond)
	if task.RetryConfig != nil {
		if err := ctValidateRetryConfig("task.retryConfig", task.RetryConfig); err != nil {
			return invalid("%v", err)
		}
	}
	task.CreateTime = ctTimestamp(now, time.Second)
	task.DispatchCount, task.ResponseCount, task.FirstAttempt, task.LastAttempt, task.View = 0, 0, nil, nil, ""
	return id, nil
}

// ctCreateTask adds a task to the queue named queueName.
func ctCreateTask(queueName string, task CTTask, now time.Time) (CTTask, *ctTaskError) {
	q, ok := ctQueues.Get(queueName)
	if !ok {
		return CTTask{}, ctRefusal(http.StatusNotFound, "NOT_FOUND", "Queue %s does not exist", queueName)
	}
	id, refused := ctPrepareTask(q, &task, now)
	if refused != nil {
		return CTTask{}, refused
	}
	at, _ := time.Parse(time.RFC3339Nano, task.ScheduleTime)
	var dup bool
	if !ctBacklogs.Update(queueName, func(b *ctBacklog) {
		if b.Tasks.ByID(id) != nil {
			dup = true
			return
		}
		if _, recent := b.Tasks.Dedup.Lookup(id, now); recent {
			dup = true
			return
		}
		b.Tasks.Enqueue(ctStored{Task: task}, msgq.EnqueueOpts{ID: id, Delay: at.Sub(now)}, ctPolicy, now)
	}) {
		return CTTask{}, ctRefusal(http.StatusNotFound, "NOT_FOUND", "Queue %s does not exist", queueName)
	}
	if dup {
		return CTTask{}, ctRefusal(http.StatusConflict, "ALREADY_EXISTS", "The task %s cannot be created because a task with this name existed too recently", task.Name)
	}
	return task, nil
}

// ctView is a task as a response view shows it. BASIC leaves out only an App
// Engine request's body, and the simulator holds no App Engine tasks.
func ctView(task CTTask, view protoEnum[ctTaskView]) CTTask {
	task.View = "BASIC"
	if view == "FULL" {
		task.View = "FULL"
	}
	return task
}

func ctResponseView(r *http.Request) (protoEnum[ctTaskView], error) {
	var view protoEnum[ctTaskView]
	raw := r.URL.Query().Get("responseView")
	if raw == "" {
		return view, nil
	}
	encoded := raw
	if raw[0] < '0' || raw[0] > '9' {
		encoded = `"` + raw + `"`
	}
	err := view.UnmarshalJSON([]byte(encoded))
	return view, err
}

func handleCTCreateTask(w http.ResponseWriter, r *http.Request) {
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	var req ctTaskRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		ctInvalid(w, "invalid request body: %v", err)
		return
	}
	task, refused := ctCreateTask(queueName, req.Task, time.Now().UTC())
	if refused != nil {
		refused.write(w)
		return
	}
	ctRun.kick(queueName)
	sim.WriteJSON(w, http.StatusOK, ctView(task, req.ResponseView))
}

// ctTaskOf returns the stored task id names in a queue, leaving out one past
// its retention that the queue has not yet expired.
func ctTaskOf(queueName, id string) (msgq.Message[ctStored], bool) {
	b, ok := ctBacklogs.Get(queueName)
	if !ok {
		return msgq.Message[ctStored]{}, false
	}
	m := b.Tasks.ByID(id)
	if m == nil || ctExpired(*m, time.Now()) {
		return msgq.Message[ctStored]{}, false
	}
	return *m, true
}

func ctExpired(m msgq.Message[ctStored], now time.Time) bool {
	return now.UnixMilli()-m.EnqueuedAt >= ctTaskRetention.Milliseconds()
}

func handleCTGetTask(w http.ResponseWriter, r *http.Request) {
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	view, err := ctResponseView(r)
	if err != nil {
		ctInvalid(w, "responseView: %v", err)
		return
	}
	m, ok := ctTaskOf(queueName, sim.PathParam(r, "task"))
	if !ok {
		ctNotFound(w, "Task", queueName+"/tasks/"+sim.PathParam(r, "task"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, ctView(m.Payload.Task, view))
}

func handleCTListTasks(w http.ResponseWriter, r *http.Request) {
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	view, err := ctResponseView(r)
	if err != nil {
		ctInvalid(w, "responseView: %v", err)
		return
	}
	if _, ok := ctQueues.Get(queueName); !ok {
		ctNotFound(w, "Queue", queueName)
		return
	}
	b, _ := ctBacklogs.Get(queueName)
	now := time.Now()
	tasks := []CTTask{}
	for _, m := range b.Tasks.Messages {
		if !ctExpired(m, now) {
			tasks = append(tasks, ctView(m.Payload.Task, view))
		}
	}
	page, next, ok := paginateList(w, r, tasks, gcpAsc(func(t CTTask) string { return t.Name }))
	if !ok {
		return
	}
	resp := map[string]any{"tasks": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ctDeleteTask removes a scheduled or dispatched task; one that succeeded or
// failed for good is already gone.
func ctDeleteTask(queueName, id string, now time.Time) *ctTaskError {
	found := false
	ctBacklogs.Update(queueName, func(b *ctBacklog) {
		removed := b.Tasks.Remove(func(m msgq.Message[ctStored]) bool { return m.ID == id && !ctExpired(m, now) })
		for _, m := range removed {
			ctRemember(b, m, now)
			found = true
		}
	})
	if !found {
		return ctRefusal(http.StatusNotFound, "NOT_FOUND", "Task %s/tasks/%s does not exist", queueName, id)
	}
	return nil
}

func handleCTDeleteTask(w http.ResponseWriter, r *http.Request) {
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	if refused := ctDeleteTask(queueName, sim.PathParam(r, "task"), time.Now().UTC()); refused != nil {
		refused.write(w)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleCTTaskAction serves tasks.run and the named spelling of tasks.buffer.
func handleCTTaskAction(w http.ResponseWriter, r *http.Request) {
	id, action, _ := strings.Cut(sim.PathParam(r, "taskAction"), ":")
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	switch action {
	case "run":
	case "buffer":
		handleCTBuffer(w, r)
		return
	default:
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown task method %q", action)
		return
	}
	var req struct {
		ResponseView protoEnum[ctTaskView] `json:"responseView,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		ctInvalid(w, "invalid request body: %v", err)
		return
	}
	task, ok := ctRunTask(queueName, id)
	if !ok {
		ctNotFound(w, "Task", queueName+"/tasks/"+id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ctView(task, req.ResponseView))
}

// handleCTBuffer serves both spellings of tasks.buffer.
func handleCTBuffer(w http.ResponseWriter, r *http.Request) {
	GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "%s", ctBufferUnservedWhy)
}

const ctTypePrefix = "type.googleapis.com/google.cloud.tasks.v2."

// ctBatchOperation answers a batch with the operation requestID names when one
// exists — a retried batch is not run again — or runs it and records the
// operation it finished as.
func ctBatchOperation(w http.ResponseWriter, r *http.Request, requestID string, run func() Operation) {
	if requestID == "" {
		requestID = sim.NewUUID()
	}
	name := gcpLocationOperationName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), requestID)
	if op, ok := crOperations.Get(name); ok {
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	op := run()
	op.Name = name
	crOperations.Put(name, op)
	sim.WriteJSON(w, http.StatusOK, op)
}

// ctBatchResult is the operation a batch finished as: SUCCEEDED, or
// PARTIALLY_SUCCEEDED with the failures by request index and, when nothing
// succeeded, the operation's ABORTED error.
func ctBatchResult(kind string, started time.Time, failed map[string]any, total int, response map[string]any) Operation {
	state := "SUCCEEDED"
	if len(failed) > 0 {
		state = "PARTIALLY_SUCCEEDED"
	}
	metadata := map[string]any{
		"@type":     ctTypePrefix + kind + "Metadata",
		"startTime": protoJSONTimestamp(started),
		"endTime":   protoJSONTimestamp(time.Now().UTC()),
		"state":     state,
	}
	if len(failed) > 0 {
		metadata["failedRequests"] = failed
	}
	op := Operation{Metadata: metadata, Done: true}
	if total > 0 && len(failed) == total {
		op.Error = &OperationError{Code: int(codes.Aborted),
			Message: "None of the requests succeeded, refer to " + kind + "Metadata.failed_requests for individual error details"}
		return op
	}
	op.Response = response
	return op
}

func handleCTBatchCreate(w http.ResponseWriter, r *http.Request) {
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	var req struct {
		Requests  []ctTaskRequest `json:"requests"`
		RequestID string          `json:"requestId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		ctInvalid(w, "invalid request body: %v", err)
		return
	}
	switch {
	case len(req.Requests) == 0:
		ctInvalid(w, "requests must name at least one task")
		return
	case len(req.Requests) > ctMaxBatchCreate:
		ctInvalid(w, "a batch creates at most %d tasks", ctMaxBatchCreate)
		return
	}
	for i, each := range req.Requests {
		if each.Parent != "" && each.Parent != queueName {
			ctInvalid(w, "requests[%d].parent %q is not the queue %s the batch addresses", i, each.Parent, queueName)
			return
		}
	}
	if _, ok := ctQueues.Get(queueName); !ok {
		ctNotFound(w, "Queue", queueName)
		return
	}
	ctBatchOperation(w, r, req.RequestID, func() Operation {
		started := time.Now().UTC()
		failed := map[string]any{}
		created := []CTTask{}
		for i, each := range req.Requests {
			task, refused := ctCreateTask(queueName, each.Task, time.Now().UTC())
			if refused != nil {
				failed[fmt.Sprint(i)] = map[string]any{"code": refused.rpcCode(), "message": refused.msg}
				continue
			}
			created = append(created, ctView(task, each.ResponseView))
		}
		ctRun.kick(queueName)
		return ctBatchResult("BatchCreateTasks", started, failed, len(req.Requests),
			map[string]any{"@type": ctTypePrefix + "BatchCreateTasksResponse", "tasks": created})
	})
}

func handleCTBatchDelete(w http.ResponseWriter, r *http.Request) {
	queueName := ctQueueName(r, sim.PathParam(r, "queue"))
	var req struct {
		Names     []string `json:"names"`
		RequestID string   `json:"requestId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		ctInvalid(w, "invalid request body: %v", err)
		return
	}
	switch {
	case len(req.Names) == 0:
		ctInvalid(w, "names must name at least one task")
		return
	case len(req.Names) > ctMaxBatchDelete:
		ctInvalid(w, "a batch deletes at most %d tasks", ctMaxBatchDelete)
		return
	}
	ctBatchOperation(w, r, req.RequestID, func() Operation {
		started := time.Now().UTC()
		failed := map[string]any{}
		for i, name := range req.Names {
			id, ok := strings.CutPrefix(name, queueName+"/tasks/")
			var refused *ctTaskError
			if !ok {
				refused = ctRefusal(http.StatusBadRequest, "INVALID_ARGUMENT", "task %q is not in the queue %s", name, queueName)
			} else {
				refused = ctDeleteTask(queueName, id, time.Now().UTC())
			}
			if refused != nil {
				failed[fmt.Sprint(i)] = map[string]any{"code": refused.rpcCode(), "message": refused.msg}
			}
		}
		return ctBatchResult("BatchDeleteTasks", started, failed, len(req.Names),
			map[string]any{"@type": "type.googleapis.com/google.protobuf.Empty"})
	})
}
