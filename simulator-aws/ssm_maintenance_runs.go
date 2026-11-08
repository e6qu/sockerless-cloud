package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/signals"
	"github.com/google/uuid"
)

// A maintenance window execution runs the window's tasks in priority order,
// tasks of one priority in parallel, and starts no task after the window's
// end minus its Cutoff (CreateMaintenanceWindow in ssm.smithy.json.gz). Each
// task calls its service through that service's API as Systems Manager, with
// the task's service role when it names one.

type SSMWindowExecution struct {
	WindowId          string   `json:"WindowId"`
	WindowExecutionId string   `json:"WindowExecutionId"`
	Status            string   `json:"Status"`
	StatusDetails     string   `json:"StatusDetails,omitempty"`
	StartTime         float64  `json:"StartTime"`
	EndTime           float64  `json:"EndTime,omitempty"`
	TaskIds           []string `json:"TaskIds"`
	CancelRequested   bool     `json:"CancelRequested,omitempty"`
}

type SSMWindowTaskExecution struct {
	WindowExecutionId string         `json:"WindowExecutionId"`
	TaskExecutionId   string         `json:"TaskExecutionId"`
	WindowTaskId      string         `json:"WindowTaskId"`
	TaskArn           string         `json:"TaskArn"`
	Type              string         `json:"Type"`
	Priority          int            `json:"Priority"`
	ServiceRole       string         `json:"ServiceRole,omitempty"`
	MaxConcurrency    string         `json:"MaxConcurrency,omitempty"`
	MaxErrors         string         `json:"MaxErrors,omitempty"`
	TaskParameters    map[string]any `json:"TaskParameters,omitempty"`
	Status            string         `json:"Status"`
	StatusDetails     string         `json:"StatusDetails,omitempty"`
	StartTime         float64        `json:"StartTime"`
	EndTime           float64        `json:"EndTime,omitempty"`
}

type SSMWindowInvocation struct {
	WindowExecutionId string  `json:"WindowExecutionId"`
	TaskExecutionId   string  `json:"TaskExecutionId"`
	InvocationId      string  `json:"InvocationId"`
	ExecutionId       string  `json:"ExecutionId,omitempty"`
	TaskType          string  `json:"TaskType"`
	Parameters        string  `json:"Parameters,omitempty"`
	Status            string  `json:"Status"`
	StatusDetails     string  `json:"StatusDetails,omitempty"`
	StartTime         float64 `json:"StartTime"`
	EndTime           float64 `json:"EndTime,omitempty"`
	OwnerInformation  string  `json:"OwnerInformation,omitempty"`
	WindowTargetId    string  `json:"WindowTargetId,omitempty"`
}

var (
	ssmWindowExecutions     sim.Store[SSMWindowExecution]
	ssmWindowTaskExecutions sim.Store[SSMWindowTaskExecution]
	ssmWindowInvocations    sim.Store[SSMWindowInvocation]
	ssmCommandChanges       = signals.New[string]()
	ssmAutomationChanges    = signals.New[string]()
)

func registerSSMMaintenanceWindowRuns(srv *sim.Server) {
	ssmWindowExecutions = sim.MakeStore[SSMWindowExecution](srv.DB(), "ssm_maintenance_window_executions")
	ssmWindowTaskExecutions = sim.MakeStore[SSMWindowTaskExecution](srv.DB(), "ssm_maintenance_window_task_executions")
	ssmWindowInvocations = sim.MakeStore[SSMWindowInvocation](srv.DB(), "ssm_maintenance_window_invocations")
	startSSMMaintenanceWindows(srv)
}

func ssmNowSeconds() float64 {
	return float64(time.Now().UnixMilli()) / 1000
}

func ssmWindowExecutionDone(status string) bool {
	switch status {
	case "PENDING", "IN_PROGRESS", "CANCELLING":
		return false
	}
	return true
}

// ssmStartWindowExecution records the run the schedule fired. A run that
// meets the window's previous execution still in progress is skipped as
// SKIPPED_OVERLAPPING.
func ssmStartWindowExecution(windowID string, scheduled time.Time) {
	m, ok := ssmWindows.Get(windowID)
	if !ok {
		return
	}
	now := ssmNowSeconds()
	exec := SSMWindowExecution{
		WindowId:          windowID,
		WindowExecutionId: uuid.New().String(),
		Status:            "IN_PROGRESS",
		StartTime:         now,
		TaskIds:           []string{},
	}
	for _, prior := range ssmWindowExecutions.List() {
		if prior.WindowId == windowID && !ssmWindowExecutionDone(prior.Status) {
			exec.Status, exec.EndTime = "SKIPPED_OVERLAPPING", now
			ssmWindowExecutions.Put(exec.WindowExecutionId, exec)
			return
		}
	}
	ssmWindowExecutions.Put(exec.WindowExecutionId, exec)
	windowEnd := scheduled.Add(time.Duration(m.Duration) * time.Hour)
	cutoff := windowEnd.Add(-time.Duration(m.Cutoff) * time.Hour)
	bg.Go(func() { ssmRunWindowExecution(m, exec.WindowExecutionId, cutoff) })
}

func ssmRunWindowExecution(m SSMMaintenanceWindow, execID string, cutoff time.Time) {
	tasks := ssmTasksForWindow(m.WindowId)
	statuses := []string{}
	for len(tasks) > 0 {
		priority := tasks[0].Priority
		var group []SSMMaintenanceWindowTask
		for len(tasks) > 0 && tasks[0].Priority == priority {
			group, tasks = append(group, tasks[0]), tasks[1:]
		}
		exec, ok := ssmWindowExecutions.Get(execID)
		if !ok || exec.CancelRequested || !time.Now().Before(cutoff) {
			break
		}
		results := make([]string, len(group))
		var wg sync.WaitGroup
		for i, task := range group {
			taskExecID := uuid.New().String()
			ssmWindowExecutions.Update(execID, func(e *SSMWindowExecution) { e.TaskIds = append(e.TaskIds, taskExecID) })
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = ssmRunWindowTask(m, execID, taskExecID, task, cutoff)
			}()
		}
		wg.Wait()
		statuses = append(statuses, results...)
	}
	ssmWindowExecutions.Update(execID, func(e *SSMWindowExecution) {
		e.EndTime = ssmNowSeconds()
		switch {
		case e.CancelRequested:
			e.Status = "CANCELLED"
		case ssmContains(statuses, "FAILED"):
			e.Status = "FAILED"
		case ssmContains(statuses, "TIMED_OUT"):
			e.Status = "TIMED_OUT"
		case ssmContains(statuses, "CANCELLED"):
			e.Status = "CANCELLED"
		default:
			e.Status = "SUCCESS"
		}
	})
}

func ssmContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func ssmTasksForWindow(windowID string) []SSMMaintenanceWindowTask {
	var tasks []SSMMaintenanceWindowTask
	for _, t := range ssmWindowTasks.List() {
		if t.WindowId == windowID {
			tasks = append(tasks, t)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority < tasks[j].Priority
		}
		return tasks[i].WindowTaskId < tasks[j].WindowTaskId
	})
	return tasks
}

// ssmWindowTaskTarget is one node a task runs on, with the window target that
// named it.
type ssmWindowTaskTarget struct {
	InstanceID       string
	WindowTargetID   string
	OwnerInformation string
}

// ssmWindowTaskTargets resolves a task's Targets: WindowTargetIds name the
// window's registered targets, the other keys name nodes directly. A task
// without targets runs once, targetless.
func ssmWindowTaskTargets(windowID string, targets []SSMTarget) ([]ssmWindowTaskTarget, bool) {
	if len(targets) == 0 {
		return nil, false
	}
	var out []ssmWindowTaskTarget
	seen := map[string]bool{}
	add := func(ids []string, targetID, owner string) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, ssmWindowTaskTarget{InstanceID: id, WindowTargetID: targetID, OwnerInformation: owner})
			}
		}
	}
	for _, t := range targets {
		if t.Key != "WindowTargetIds" {
			add(ssmTargetInstances([]SSMTarget{t}), "", "")
			continue
		}
		for _, targetID := range t.Values {
			registered, ok := ssmWindowTargets.Get(ssmWindowKey(windowID, targetID))
			if !ok {
				continue
			}
			add(ssmTargetInstances(registered.Targets), registered.WindowTargetId, registered.OwnerInformation)
		}
	}
	return out, true
}

// ssmTargetInstances lists the managed nodes Targets select: InstanceIds by
// ID, tag:<key> and tag-key by the Amazon EC2 instance's tags.
func ssmTargetInstances(targets []SSMTarget) []string {
	var ids []string
	for _, t := range targets {
		switch {
		case t.Key == "InstanceIds":
			ids = append(ids, t.Values...)
		case t.Key == "tag-key" || strings.HasPrefix(t.Key, "tag:"):
			nodes := ssmManagedInstances.List()
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].InstanceId < nodes[j].InstanceId })
			for _, node := range nodes {
				matched := false
				if key, ok := strings.CutPrefix(t.Key, "tag:"); ok {
					matched = ssmNodeHasTag(node, []string{key}, t.Values)
				} else {
					matched = ssmNodeHasTag(node, t.Values, nil)
				}
				if matched {
					ids = append(ids, node.InstanceId)
				}
			}
		}
	}
	return ids
}

// ssmRateLimit reads a MaxConcurrency or MaxErrors value, a count or a
// percentage of total.
func ssmRateLimit(value string, total, fallback int) int {
	if value == "" {
		return fallback
	}
	if pct, ok := strings.CutSuffix(value, "%"); ok {
		n, err := strconv.Atoi(pct)
		if err != nil {
			return fallback
		}
		return (total*n + 99) / 100
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func ssmRunWindowTask(m SSMMaintenanceWindow, execID, taskExecID string, task SSMMaintenanceWindowTask, cutoff time.Time) string {
	params := task.TaskInvocationParameters
	role := task.ServiceRoleArn
	if params != nil && params.RunCommand != nil && params.RunCommand.ServiceRoleArn != "" {
		role = params.RunCommand.ServiceRoleArn
	}
	if role == "" {
		role = ssmEnsureServiceLinkedRole()
	}
	record := SSMWindowTaskExecution{
		WindowExecutionId: execID,
		TaskExecutionId:   taskExecID,
		WindowTaskId:      task.WindowTaskId,
		TaskArn:           task.TaskArn,
		Type:              task.Type,
		Priority:          task.Priority,
		ServiceRole:       role,
		MaxConcurrency:    task.MaxConcurrency,
		MaxErrors:         task.MaxErrors,
		TaskParameters:    task.TaskParameters,
		Status:            "IN_PROGRESS",
		StartTime:         ssmNowSeconds(),
	}
	key := execID + "/" + taskExecID
	ssmWindowTaskExecutions.Put(key, record)

	targets, targeted := ssmWindowTaskTargets(m.WindowId, task.Targets)
	if !targeted {
		targets = []ssmWindowTaskTarget{{}}
	}
	concurrency := max(ssmRateLimit(task.MaxConcurrency, len(targets), 1), 1)
	maxErrors := ssmRateLimit(task.MaxErrors, len(targets), 0)
	if !targeted {
		concurrency, maxErrors = 1, 1
	}
	actor := awsActor{Service: "ssm.amazonaws.com", Source: ssmWindowARN(m.WindowId), Role: role}
	cancelAtCutoff := task.CutoffBehavior == "CANCEL_TASK"

	var (
		mu       sync.Mutex
		failures int
		statuses []string
		wg       sync.WaitGroup
	)
	slots := make(chan struct{}, concurrency)
	timedOut := false
	for _, target := range targets {
		slots <- struct{}{}
		mu.Lock()
		stop := failures > maxErrors
		mu.Unlock()
		exec, _ := ssmWindowExecutions.Get(execID)
		if stop || exec.CancelRequested {
			<-slots
			break
		}
		if cancelAtCutoff && !time.Now().Before(cutoff) {
			<-slots
			timedOut = true
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			status := ssmRunWindowInvocation(actor, execID, taskExecID, task, target, cutoff, cancelAtCutoff)
			mu.Lock()
			defer mu.Unlock()
			statuses = append(statuses, status)
			if status != "SUCCESS" {
				failures++
			}
		}()
	}
	wg.Wait()
	status := "SUCCESS"
	switch {
	case timedOut || ssmContains(statuses, "TIMED_OUT"):
		status = "TIMED_OUT"
	case ssmContains(statuses, "FAILED"):
		status = "FAILED"
	case ssmContains(statuses, "CANCELLED"):
		status = "CANCELLED"
	}
	ssmWindowTaskExecutions.Update(key, func(t *SSMWindowTaskExecution) {
		t.Status, t.EndTime = status, ssmNowSeconds()
	})
	return status
}

func ssmRunWindowInvocation(actor awsActor, execID, taskExecID string, task SSMMaintenanceWindowTask, target ssmWindowTaskTarget, cutoff time.Time, cancelAtCutoff bool) string {
	inv := SSMWindowInvocation{
		WindowExecutionId: execID,
		TaskExecutionId:   taskExecID,
		InvocationId:      uuid.New().String(),
		TaskType:          task.Type,
		Status:            "IN_PROGRESS",
		StartTime:         ssmNowSeconds(),
		OwnerInformation:  target.OwnerInformation,
		WindowTargetId:    target.WindowTargetID,
	}
	key := execID + "/" + taskExecID + "/" + inv.InvocationId
	ssmWindowInvocations.Put(key, inv)
	ctx := context.Background()
	var (
		executionID, status, details string
		parameters                   any
	)
	switch task.Type {
	case "RUN_COMMAND":
		parameters, executionID, status, details = ssmWindowRunCommand(ctx, actor, task, target.InstanceID, cutoff, cancelAtCutoff)
	case "LAMBDA":
		parameters, executionID, status, details = ssmWindowLambda(ctx, actor, task)
	case "STEP_FUNCTIONS":
		parameters, executionID, status, details = ssmWindowStepFunctions(ctx, actor, task)
	case "AUTOMATION":
		parameters, executionID, status, details = ssmWindowAutomation(ctx, actor, task)
	default:
		status, details = "FAILED", "Unsupported task type "+task.Type
	}
	encoded, _ := json.Marshal(parameters)
	if len(details) > 250 {
		details = details[:250]
	}
	ssmWindowInvocations.Update(key, func(i *SSMWindowInvocation) {
		i.ExecutionId, i.Status, i.StatusDetails = executionID, status, details
		i.Parameters = string(encoded)
		i.EndTime = ssmNowSeconds()
	})
	return status
}

func ssmCallFailure(status int, body []byte, err error) (string, bool) {
	if err != nil {
		return err.Error(), true
	}
	if status >= http.StatusBadRequest {
		code, message := awsJSONError(body)
		return strings.TrimSpace(code + ": " + message), true
	}
	return "", false
}

// ssmWindowRunCommand sends the document to one node and waits for the
// command. Under CANCEL_TASK it cancels a command still running at the cutoff,
// which leaves the invocation TIMED_OUT.
func ssmWindowRunCommand(ctx context.Context, actor awsActor, task SSMMaintenanceWindowTask, instanceID string, cutoff time.Time, cancelAtCutoff bool) (any, string, string, string) {
	input := map[string]any{"DocumentName": task.TaskArn, "InstanceIds": []string{instanceID}}
	if rc := task.TaskInvocationParameters.runCommand(); rc != nil {
		if rc.Parameters != nil {
			input["Parameters"] = rc.Parameters
		}
		for name, value := range map[string]string{"Comment": rc.Comment, "DocumentVersion": rc.DocumentVersion,
			"DocumentHash": rc.DocumentHash, "DocumentHashType": rc.DocumentHashType,
			"OutputS3BucketName": rc.OutputS3BucketName, "OutputS3KeyPrefix": rc.OutputS3KeyPrefix} {
			if value != "" {
				input[name] = value
			}
		}
		if rc.TimeoutSeconds > 0 {
			input["TimeoutSeconds"] = rc.TimeoutSeconds
		}
	} else if len(task.TaskParameters) > 0 {
		input["Parameters"] = ssmTaskParameterValues(task.TaskParameters)
	}
	status, body, err := awsCallJSON(ctx, actor, "ssm", "AmazonSSM.SendCommand", "application/x-amz-json-1.1", input)
	if failure, failed := ssmCallFailure(status, body, err); failed {
		return input, "", "FAILED", failure
	}
	var sent struct {
		Command struct {
			CommandId string `json:"CommandId"`
		} `json:"Command"`
	}
	if json.Unmarshal(body, &sent) != nil || sent.Command.CommandId == "" {
		return input, "", "FAILED", "Run Command returned no command"
	}
	commandID := sent.Command.CommandId
	settled := func() bool {
		cmd, ok := ssmCommands.Get(commandID)
		return !ok || ssmCommandDone(cmd.Status)
	}
	waitCtx := ctx
	if cancelAtCutoff {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithDeadline(ctx, cutoff)
		defer cancel()
	}
	if ssmCommandChanges.Await(waitCtx, commandID, settled) != nil {
		_, _, _ = awsCallJSON(ctx, actor, "ssm", "AmazonSSM.CancelCommand", "application/x-amz-json-1.1", map[string]any{"CommandId": commandID})
		return input, commandID, "TIMED_OUT", ""
	}
	cmd, _ := ssmCommands.Get(commandID)
	switch cmd.Status {
	case "Success":
		return input, commandID, "SUCCESS", ""
	case "TimedOut":
		return input, commandID, "TIMED_OUT", cmd.StatusDetail
	case "Cancelled":
		return input, commandID, "CANCELLED", cmd.StatusDetail
	}
	return input, commandID, "FAILED", cmd.StatusDetail
}

func ssmCommandDone(status string) bool {
	switch status {
	case "Pending", "InProgress", "Delayed", "Cancelling":
		return false
	}
	return true
}

// ssmTaskParameterValues reads TaskParameters, a map of
// MaintenanceWindowTaskParameterValueExpression, as Run Command parameters.
func ssmTaskParameterValues(params map[string]any) map[string][]string {
	out := map[string][]string{}
	for name, raw := range params {
		expr, _ := raw.(map[string]any)
		values, _ := expr["Values"].([]any)
		for _, v := range values {
			if s, ok := v.(string); ok {
				out[name] = append(out[name], s)
			}
		}
	}
	return out
}

func ssmWindowLambda(ctx context.Context, actor awsActor, task SSMMaintenanceWindowTask) (any, string, string, string) {
	path := "/2015-03-31/functions/" + url.PathEscape(task.TaskArn) + "/invocations"
	header := http.Header{"X-Amz-Invocation-Type": {"RequestResponse"}, "Content-Type": {"application/json"}}
	var payload []byte
	parameters := map[string]any{"FunctionName": task.TaskArn}
	if lp := task.TaskInvocationParameters.lambda(); lp != nil {
		if lp.Qualifier != "" {
			path += "?Qualifier=" + url.QueryEscape(lp.Qualifier)
			parameters["Qualifier"] = lp.Qualifier
		}
		if lp.ClientContext != "" {
			header.Set("X-Amz-Client-Context", lp.ClientContext)
		}
		if lp.Payload != "" {
			decoded, err := base64.StdEncoding.DecodeString(lp.Payload)
			if err != nil {
				return parameters, "", "FAILED", "Payload is not base64"
			}
			payload = decoded
		}
	}
	response, err := awsCallResponse(ctx, actor, "lambda", http.MethodPost, path, header, payload)
	if err != nil {
		return parameters, "", "FAILED", err.Error()
	}
	requestID := response.Header().Get("X-Amzn-Requestid")
	if failure, failed := ssmCallFailure(response.StatusCode, response.Body.Bytes(), nil); failed {
		return parameters, requestID, "FAILED", failure
	}
	if functionError := response.Header().Get("X-Amz-Function-Error"); functionError != "" {
		return parameters, requestID, "FAILED", functionError
	}
	return parameters, requestID, "SUCCESS", ""
}

func ssmWindowStepFunctions(ctx context.Context, actor awsActor, task SSMMaintenanceWindowTask) (any, string, string, string) {
	input := map[string]any{"stateMachineArn": task.TaskArn, "input": "{}"}
	if sp := task.TaskInvocationParameters.stepFunctions(); sp != nil {
		if sp.Input != "" {
			input["input"] = sp.Input
		}
		if sp.Name != "" {
			input["name"] = sp.Name
		}
	}
	status, body, err := awsCallJSON(ctx, actor, "states", "AWSStepFunctions.StartExecution", "application/x-amz-json-1.0", input)
	if failure, failed := ssmCallFailure(status, body, err); failed {
		return input, "", "FAILED", failure
	}
	var started struct {
		ExecutionArn string `json:"executionArn"`
	}
	if json.Unmarshal(body, &started) != nil || started.ExecutionArn == "" {
		return input, "", "FAILED", "AWS Step Functions returned no execution"
	}
	arn := started.ExecutionArn
	_ = sfnExecutionChanges.Await(ctx, arn, func() bool {
		execution, ok := sfnExecutions.Get(arn)
		return !ok || execution.Status != "RUNNING"
	})
	execution, _ := sfnExecutions.Get(arn)
	switch execution.Status {
	case "SUCCEEDED":
		return input, arn, "SUCCESS", ""
	case "TIMED_OUT":
		return input, arn, "TIMED_OUT", execution.Cause
	case "ABORTED":
		return input, arn, "CANCELLED", execution.Cause
	}
	return input, arn, "FAILED", strings.TrimSpace(execution.Error + " " + execution.Cause)
}

func ssmWindowAutomation(ctx context.Context, actor awsActor, task SSMMaintenanceWindowTask) (any, string, string, string) {
	input := map[string]any{"DocumentName": task.TaskArn}
	if ap := task.TaskInvocationParameters.automation(); ap != nil {
		if ap.DocumentVersion != "" {
			input["DocumentVersion"] = ap.DocumentVersion
		}
		if ap.Parameters != nil {
			input["Parameters"] = ap.Parameters
		}
	}
	status, body, err := awsCallJSON(ctx, actor, "ssm", "AmazonSSM.StartAutomationExecution", "application/x-amz-json-1.1", input)
	if failure, failed := ssmCallFailure(status, body, err); failed {
		return input, "", "FAILED", failure
	}
	var started struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
	}
	if json.Unmarshal(body, &started) != nil || started.AutomationExecutionId == "" {
		return input, "", "FAILED", "Automation returned no execution"
	}
	id := started.AutomationExecutionId
	_ = ssmAutomationChanges.Await(ctx, id, func() bool {
		execution, ok := ssmAutomations.Get(id)
		return !ok || (execution.Status != "Pending" && execution.Status != "InProgress" && execution.Status != "Waiting")
	})
	execution, _ := ssmAutomations.Get(id)
	switch execution.Status {
	case "Success":
		return input, id, "SUCCESS", ""
	case "TimedOut":
		return input, id, "TIMED_OUT", ""
	case "Cancelled":
		return input, id, "CANCELLED", ""
	}
	return input, id, "FAILED", execution.Status
}

func handleSSMCancelMaintenanceWindowExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	found := ssmWindowExecutions.Update(req.WindowExecutionId, func(e *SSMWindowExecution) {
		if !ssmWindowExecutionDone(e.Status) {
			e.CancelRequested, e.Status = true, "CANCELLING"
		}
	})
	if !found {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"WindowExecutionId": req.WindowExecutionId})
}

type ssmWindowFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

func ssmStatusFilterMatches(filters []ssmWindowFilter, status string) bool {
	for _, f := range filters {
		if f.Key == "STATUS" && !ssmContains(f.Values, status) {
			return false
		}
	}
	return true
}

func ssmWriteWindowPage(w http.ResponseWriter, rows []map[string]any, idField, nextToken string, maxResults int, field string) {
	if rows == nil {
		rows = []map[string]any{}
	}
	page, next, pageOK := awsKeyPage(w, ssmBadToken, rows, awsFieldKey(idField), nextToken, maxResults, 50)
	if !pageOK {
		return
	}
	resp := map[string]any{field: page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmSetOptional(out map[string]any, name string, value any) {
	switch v := value.(type) {
	case string:
		if v != "" {
			out[name] = v
		}
	case float64:
		if v != 0 {
			out[name] = v
		}
	}
}

func handleSSMDescribeMaintenanceWindowExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId   string            `json:"WindowId"`
		Filters    []ssmWindowFilter `json:"Filters"`
		NextToken  string            `json:"NextToken"`
		MaxResults int               `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindows.Get(req.WindowId); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	var before, after time.Time
	for _, f := range req.Filters {
		for _, v := range f.Values {
			t, err := ssmWindowDate(v)
			if err != nil {
				AWSErrorf(w, "ValidationException", http.StatusBadRequest, "Invalid filter value %s", v)
				return
			}
			switch f.Key {
			case "ExecutedBefore":
				before = t
			case "ExecutedAfter":
				after = t
			}
		}
	}
	execs := ssmWindowExecutions.Filter(func(e SSMWindowExecution) bool {
		start := time.UnixMilli(int64(e.StartTime * 1000))
		return e.WindowId == req.WindowId &&
			(before.IsZero() || start.Before(before)) && (after.IsZero() || start.After(after))
	})
	sort.Slice(execs, func(i, j int) bool { return execs[i].StartTime > execs[j].StartTime })
	var rows []map[string]any
	for _, e := range execs {
		row := map[string]any{"WindowId": e.WindowId, "WindowExecutionId": e.WindowExecutionId, "Status": e.Status, "StartTime": e.StartTime}
		ssmSetOptional(row, "StatusDetails", e.StatusDetails)
		ssmSetOptional(row, "EndTime", e.EndTime)
		rows = append(rows, row)
	}
	ssmWriteWindowPage(w, rows, "WindowExecutionId", req.NextToken, req.MaxResults, "WindowExecutions")
}

func handleSSMGetMaintenanceWindowExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	e, ok := ssmWindowExecutions.Get(req.WindowExecutionId)
	if !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	out := map[string]any{"WindowExecutionId": e.WindowExecutionId, "TaskIds": e.TaskIds, "Status": e.Status, "StartTime": e.StartTime}
	ssmSetOptional(out, "StatusDetails", e.StatusDetails)
	ssmSetOptional(out, "EndTime", e.EndTime)
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSSMDescribeMaintenanceWindowExecutionTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string            `json:"WindowExecutionId"`
		Filters           []ssmWindowFilter `json:"Filters"`
		NextToken         string            `json:"NextToken"`
		MaxResults        int               `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindowExecutions.Get(req.WindowExecutionId); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	var rows []map[string]any
	for _, t := range ssmWindowTaskExecutions.ListPrefix(req.WindowExecutionId + "/") {
		task := t.Item
		if !ssmStatusFilterMatches(req.Filters, task.Status) {
			continue
		}
		row := map[string]any{
			"WindowExecutionId": task.WindowExecutionId, "TaskExecutionId": task.TaskExecutionId,
			"Status": task.Status, "StartTime": task.StartTime, "TaskArn": task.TaskArn, "TaskType": task.Type,
		}
		ssmSetOptional(row, "StatusDetails", task.StatusDetails)
		ssmSetOptional(row, "EndTime", task.EndTime)
		rows = append(rows, row)
	}
	ssmWriteWindowPage(w, rows, "TaskExecutionId", req.NextToken, req.MaxResults, "WindowExecutionTaskIdentities")
}

func handleSSMGetMaintenanceWindowExecutionTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
		TaskId            string `json:"TaskId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ssmWindowTaskExecutions.Get(req.WindowExecutionId + "/" + req.TaskId)
	if !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution task %s doesn't exist.", req.TaskId)
		return
	}
	out := map[string]any{
		"WindowExecutionId": t.WindowExecutionId, "TaskExecutionId": t.TaskExecutionId,
		"TaskArn": t.TaskArn, "Type": t.Type, "Priority": t.Priority, "Status": t.Status, "StartTime": t.StartTime,
	}
	ssmSetOptional(out, "ServiceRole", t.ServiceRole)
	ssmSetOptional(out, "MaxConcurrency", t.MaxConcurrency)
	ssmSetOptional(out, "MaxErrors", t.MaxErrors)
	ssmSetOptional(out, "StatusDetails", t.StatusDetails)
	ssmSetOptional(out, "EndTime", t.EndTime)
	if len(t.TaskParameters) > 0 {
		out["TaskParameters"] = []map[string]any{t.TaskParameters}
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func ssmWindowInvocationWire(i SSMWindowInvocation) map[string]any {
	out := map[string]any{
		"WindowExecutionId": i.WindowExecutionId, "TaskExecutionId": i.TaskExecutionId,
		"InvocationId": i.InvocationId, "TaskType": i.TaskType, "Status": i.Status, "StartTime": i.StartTime,
	}
	for name, value := range map[string]any{"ExecutionId": i.ExecutionId, "Parameters": i.Parameters,
		"StatusDetails": i.StatusDetails, "EndTime": i.EndTime, "OwnerInformation": i.OwnerInformation,
		"WindowTargetId": i.WindowTargetId} {
		ssmSetOptional(out, name, value)
	}
	return out
}

func handleSSMDescribeMaintenanceWindowExecutionTaskInvocations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string            `json:"WindowExecutionId"`
		TaskId            string            `json:"TaskId"`
		Filters           []ssmWindowFilter `json:"Filters"`
		NextToken         string            `json:"NextToken"`
		MaxResults        int               `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindowTaskExecutions.Get(req.WindowExecutionId + "/" + req.TaskId); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution task %s doesn't exist.", req.TaskId)
		return
	}
	var rows []map[string]any
	for _, keyed := range ssmWindowInvocations.ListPrefix(req.WindowExecutionId + "/" + req.TaskId + "/") {
		if ssmStatusFilterMatches(req.Filters, keyed.Item.Status) {
			rows = append(rows, ssmWindowInvocationWire(keyed.Item))
		}
	}
	ssmWriteWindowPage(w, rows, "InvocationId", req.NextToken, req.MaxResults, "WindowExecutionTaskInvocationIdentities")
}

func handleSSMGetMaintenanceWindowExecutionTaskInvocation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
		TaskId            string `json:"TaskId"`
		InvocationId      string `json:"InvocationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	i, ok := ssmWindowInvocations.Get(req.WindowExecutionId + "/" + req.TaskId + "/" + req.InvocationId)
	if !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution task invocation %s doesn't exist.", req.InvocationId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ssmWindowInvocationWire(i))
}

func handleSSMDescribeMaintenanceWindowSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId     string            `json:"WindowId"`
		Targets      []SSMTarget       `json:"Targets"`
		ResourceType string            `json:"ResourceType"`
		Filters      []ssmWindowFilter `json:"Filters"`
		NextToken    string            `json:"NextToken"`
		MaxResults   int               `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var before, after time.Time
	for _, f := range req.Filters {
		for _, v := range f.Values {
			t, err := ssmWindowDate(v)
			if err != nil {
				AWSErrorf(w, "ValidationException", http.StatusBadRequest, "Invalid filter value %s", v)
				return
			}
			switch f.Key {
			case "ScheduledBefore":
				before = t
			case "ScheduledAfter":
				after = t
			}
		}
	}
	targeted := ssmWindowsForTargets(req.Targets, req.ResourceType)
	now := time.Now()
	var rows []map[string]any
	for _, m := range ssmWindows.List() {
		if (req.WindowId != "" && m.WindowId != req.WindowId) || (len(req.Targets) > 0 && !targeted[m.WindowId]) {
			continue
		}
		run, ok := ssmWindowNextRun(m, now)
		if !ok || (!before.IsZero() && !run.Before(before)) || (!after.IsZero() && !run.After(after)) {
			continue
		}
		rows = append(rows, map[string]any{"WindowId": m.WindowId, "Name": m.Name, "ExecutionTime": ssmWindowTimeWire(run)})
	}
	ssmWriteWindowPage(w, rows, "WindowId", req.NextToken, req.MaxResults, "ScheduledWindowExecutions")
}

// ssmWindowsForTargets names the windows with a registered target of
// resourceType that names one of the requested targets' values under the
// same key.
func ssmWindowsForTargets(targets []SSMTarget, resourceType string) map[string]bool {
	windows := map[string]bool{}
	for _, registered := range ssmWindowTargets.List() {
		if resourceType != "" && registered.ResourceType != resourceType {
			continue
		}
		for _, have := range registered.Targets {
			for _, want := range targets {
				if have.Key != want.Key {
					continue
				}
				for _, v := range want.Values {
					if ssmContains(have.Values, v) {
						windows[registered.WindowId] = true
					}
				}
			}
		}
	}
	return windows
}

func handleSSMDescribeMaintenanceWindowsForTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Targets      []SSMTarget `json:"Targets"`
		ResourceType string      `json:"ResourceType"`
		NextToken    string      `json:"NextToken"`
		MaxResults   int         `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	owners := ssmWindowsForTargets(req.Targets, req.ResourceType)
	var rows []map[string]any
	for _, m := range ssmWindows.List() {
		if owners[m.WindowId] {
			rows = append(rows, map[string]any{"WindowId": m.WindowId, "Name": m.Name})
		}
	}
	ssmWriteWindowPage(w, rows, "WindowId", req.NextToken, req.MaxResults, "WindowIdentities")
}
