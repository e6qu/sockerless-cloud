package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PaesslerAG/jsonpath"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/google/uuid"
)

var (
	errSSMStepTimedOut  = errors.New("the step timed out")
	errSSMStepCancelled = errors.New("the step was cancelled")
)

// Automation runs a step's onCancel workflow for at most two minutes
// (ssm-automation-actions.md.gz).
const ssmOnCancelLimit = 2 * time.Minute

// aws:executeAwsApi runs for at most 25 seconds (ssm-automation-action-executeAwsApi.md.gz).
const ssmExecuteAwsApiLimit = 25 * time.Second

// aws:sleep delays at most 604799 seconds (ssm-automation-action-sleep.md.gz).
const ssmSleepLimit = 604799 * time.Second

// Default timeouts of the actions that wait: one hour for
// aws:waitForAwsResourceProperty, seven days for aws:pause.
const (
	ssmWaitForPropertyTimeout = time.Hour
	ssmPauseTimeout           = 7 * 24 * time.Hour
)

// The waitForAwsResourceProperty reference states no polling cadence; the
// runner reads the property again at this interval.
const ssmWaitForPropertyInterval = 2 * time.Second

type ssmRunner struct {
	id      string
	runbook ssmRunbook
}

func (x *ssmRunner) exec() (SSMAutomationExecution, bool) {
	return ssmAutomations.Get(x.id)
}

func (x *ssmRunner) update(fn func(*SSMAutomationExecution)) {
	ssmAutomations.Update(x.id, fn)
}

// actor is who the execution's service calls run as.
func (x *ssmRunner) actor(exec SSMAutomationExecution) awsActor {
	id := exec.Identity
	switch {
	case id.Role != "":
		return awsActor{Service: "ssm.amazonaws.com", Source: ssmAutomationARN(exec.AutomationExecutionId), Role: id.Role}
	case id.AccessKeyID != "":
		return awsActor{AccessKeyID: id.AccessKeyID, SessionToken: id.SessionToken}
	}
	return awsActor{Service: id.Service, Source: id.Source}
}

func (x *ssmRunner) finish(status, failure string) {
	x.update(func(e *SSMAutomationExecution) {
		if ssmAutomationTerminal(e.Status) {
			return
		}
		e.Status, e.EndTime = status, ssmNowSeconds()
		if failure != "" {
			e.FailureMessage = failure
		}
		e.Outputs = x.executionOutputs(*e)
		e.Signals = nil
	})
}

// executionOutputs reads the runbook's outputs, each a stepName.outputName.
func (x *ssmRunner) executionOutputs(exec SSMAutomationExecution) map[string][]string {
	out := map[string][]string{}
	ip := ssmInterpolator{exec: exec}
	for _, ref := range x.runbook.Outputs {
		if v, err := ip.lookup(ref); err == nil {
			out[ref] = ssmValueStrings(v)
		}
	}
	return out
}

func ssmRunAutomation(ctx context.Context, id string) {
	exec, ok := ssmAutomations.Get(id)
	if !ok || ssmAutomationTerminal(exec.Status) {
		return
	}
	x := &ssmRunner{id: id}
	runbook, err := ssmParseRunbook(exec.Content, exec.Format)
	if err != nil {
		x.finish("Failed", err.Error())
		return
	}
	x.runbook = runbook
	if exec.Status == "Scheduled" && !x.awaitSchedule(ctx, exec.ScheduledTime) {
		return
	}
	if len(exec.ResolvedTargets) > 0 || len(exec.TargetMaps) > 0 {
		x.runParent(ctx)
		return
	}
	if exec.Identity.Role != "" {
		if _, err := awsAssumeServiceRole(x.actor(exec)); err != nil {
			x.finish("Failed", "The defined assume role is unable to be assumed.")
			return
		}
	}
	x.update(func(e *SSMAutomationExecution) {
		if e.Status == "Pending" || e.Status == "Scheduled" {
			e.Status = "InProgress"
		}
	})
	x.runSteps(ctx)
	if ctx.Err() != nil {
		return
	}
	if exec, _ = x.exec(); exec.Status == "RunbookInProgress" {
		x.runChangeRunbooks(ctx)
	}
}

// awaitSchedule holds a scheduled change request until its ScheduledTime.
func (x *ssmRunner) awaitSchedule(ctx context.Context, at float64) bool {
	wait := time.Until(time.UnixMilli(int64(at * 1000)))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		stopped := make(chan struct{})
		watchCtx, stopWatch := context.WithCancel(ctx)
		defer stopWatch()
		bg.JoinedGo(func() {
			_ = ssmAutomationChanges.Await(watchCtx, x.id, func() bool {
				e, ok := x.exec()
				if !ok || e.StopRequested != "" {
					close(stopped)
					return true
				}
				return false
			})
		})
		select {
		case <-ctx.Done():
			return false
		case <-stopped:
			x.finish("Cancelled", "")
			return false
		case <-timer.C:
		}
	}
	x.update(func(e *SSMAutomationExecution) {
		if e.Status == "Scheduled" {
			e.Status = "Pending"
		}
	})
	return true
}

func (x *ssmRunner) runSteps(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		exec, ok := x.exec()
		if !ok || ssmAutomationTerminal(exec.Status) {
			return
		}
		switch exec.StopRequested {
		case "Cancel":
			x.finish("Cancelled", "")
			return
		case "Complete":
			if status, _ := x.criticalOutcome(exec); status != "" {
				x.finish("CompletedWithFailure", "")
			} else {
				x.finish("CompletedWithSuccess", "")
			}
			return
		}
		name := exec.Cursor
		if name == "" {
			status, failure := x.criticalOutcome(exec)
			if status == "" && exec.Subtype == "ChangeRequest" && len(exec.Runbooks) > 0 {
				x.update(func(e *SSMAutomationExecution) { e.Status = "RunbookInProgress" })
				return
			}
			if status == "" {
				status = "Success"
			}
			x.finish(status, failure)
			return
		}
		if exec.Mode == "Interactive" {
			started, ok := x.awaitStartStep(ctx)
			if !ok {
				continue
			}
			name = started
		}
		step := x.runbook.step(name)
		if step == nil {
			x.finish("Failed", fmt.Sprintf("The step %s isn't defined in the runbook.", name))
			return
		}
		status, next, interrupted := x.runStep(ctx, *step, false)
		if interrupted {
			return
		}
		switch status {
		case "Success":
			x.advance(*step, next)
		case "Failed", "TimedOut":
			switch {
			case step.OnFailure == "Continue":
				x.advance(*step, "")
			case strings.HasPrefix(step.OnFailure, "step:"):
				target := strings.TrimPrefix(step.OnFailure, "step:")
				x.update(func(e *SSMAutomationExecution) { e.Cursor = target })
			default:
				exec, _ := x.exec()
				x.finish(status, ssmLastFailure(exec, step.Name))
				return
			}
		case "Cancelled":
			if target, ok := strings.CutPrefix(step.OnCancel, "step:"); ok {
				if cleanup := x.runbook.step(target); cleanup != nil {
					x.runStep(ctx, *cleanup, true)
				}
			}
			if exec, _ := x.exec(); exec.StopRequested == "Cancel" || exec.Mode != "Interactive" {
				x.finish("Cancelled", "")
				return
			}
			x.update(func(e *SSMAutomationExecution) { e.StopStepID = "" })
			x.advance(*step, "")
		}
	}
}

// advance moves the cursor past a step: to the branch's choice, else nowhere
// after an isEnd step, else to its nextStep, else to the step after it.
func (x *ssmRunner) advance(step ssmRunbookStep, branchNext string) {
	next := branchNext
	switch {
	case next != "":
	case step.IsEnd:
	case step.NextStep != "":
		next = step.NextStep
	default:
		next = x.runbook.following(step.Name)
	}
	x.update(func(e *SSMAutomationExecution) { e.Cursor = next })
}

// criticalOutcome is the execution's status when a critical step failed or
// timed out (ssm-automation-handling-timeouts.md.gz), with that step's message.
func (x *ssmRunner) criticalOutcome(exec SSMAutomationExecution) (string, string) {
	for _, s := range exec.Steps {
		if s.IsCritical && (s.StepStatus == "Failed" || s.StepStatus == "TimedOut") {
			return s.StepStatus, s.FailureMessage
		}
	}
	return "", ""
}

func ssmLastFailure(exec SSMAutomationExecution, stepName string) string {
	for i := len(exec.Steps) - 1; i >= 0; i-- {
		if exec.Steps[i].StepName == stepName {
			return exec.Steps[i].FailureMessage
		}
	}
	return ""
}

// awaitStartStep waits, in Interactive mode, for the StartStep signal that
// names the step to run next.
func (x *ssmRunner) awaitStartStep(ctx context.Context) (string, bool) {
	x.update(func(e *SSMAutomationExecution) {
		if e.Status == "InProgress" || e.Status == "Pending" {
			e.Status = "Waiting"
		}
	})
	var started string
	_ = ssmAutomationChanges.Await(ctx, x.id, func() bool {
		e, ok := x.exec()
		if !ok || e.StopRequested != "" {
			return true
		}
		for _, s := range e.Signals {
			if s.Type == "StartStep" {
				started = firstOf(s.Payload["StepName"])
				return true
			}
		}
		return false
	})
	if started == "" {
		return "", false
	}
	x.update(func(e *SSMAutomationExecution) {
		e.Signals = ssmDropSignal(e.Signals, "StartStep")
		e.Status = "InProgress"
	})
	return started, true
}

func firstOf(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}

func ssmDropSignal(signals []ssmAutomationSignal, signalType string) []ssmAutomationSignal {
	for i, s := range signals {
		if s.Type == signalType {
			return append(append([]ssmAutomationSignal{}, signals[:i]...), signals[i+1:]...)
		}
	}
	return signals
}

// stepRecord returns the index of the StepExecution a run of step writes: the
// Pending one laid out at the start, or a new one when the step runs again.
func (x *ssmRunner) stepRecord(step ssmRunbookStep) int {
	index := -1
	x.update(func(e *SSMAutomationExecution) {
		for i := len(e.Steps) - 1; i >= 0; i-- {
			if e.Steps[i].StepName == step.Name {
				if e.Steps[i].StepStatus == "Pending" || e.Steps[i].StepStatus == "InProgress" || e.Steps[i].StepStatus == "Waiting" {
					index = i
				}
				break
			}
		}
		if index < 0 {
			e.Steps = append(e.Steps, ssmNewStepExecution(step))
			index = len(e.Steps) - 1
		}
	})
	return index
}

func (x *ssmRunner) updateStep(index int, fn func(*SSMAutomationStep)) {
	x.update(func(e *SSMAutomationExecution) {
		if index < len(e.Steps) {
			fn(&e.Steps[index])
		}
	})
}

type ssmStepResult struct {
	values       map[string]any
	response     any
	next         string
	responseCode string
	failure      string
	failureType  string
}

func ssmStepFailed(format string, args ...any) ssmStepResult {
	return ssmStepResult{failure: fmt.Sprintf(format, args...), failureType: "Verification"}
}

// runStep runs one step through its attempts and records its status. It
// reports interrupted when the simulator stopped under it, leaving the step
// for the next process to run again.
func (x *ssmRunner) runStep(ctx context.Context, step ssmRunbookStep, onCancel bool) (status, next string, interrupted bool) {
	index := x.stepRecord(step)
	attempts := max(step.MaxAttempts, 1)
	stepID := ""
	x.update(func(e *SSMAutomationExecution) {
		if !onCancel && e.Status != "Cancelling" {
			e.Status = "InProgress"
		}
		e.CurrentStepName, e.CurrentAction = step.Name, step.Action
		stepID = e.Steps[index].StepExecID
	})
	x.updateStep(index, func(s *SSMAutomationStep) {
		s.StepStatus = "InProgress"
		if s.ExecStartTime == 0 {
			s.ExecStartTime = ssmNowSeconds()
		}
	})
	var result ssmStepResult
	for attempt := 1; attempt <= attempts; attempt++ {
		exec, _ := x.exec()
		resolved, err := (ssmInterpolator{exec: exec}).resolve(step.Inputs)
		if err != nil {
			result, status = ssmStepResult{failure: "Step fails when it is resolving inputs. " + err.Error(), failureType: "Verification"}, "Failed"
			x.updateStep(index, func(s *SSMAutomationStep) { s.FailureStage = "Inputs" })
			break
		}
		inputs, _ := resolved.(map[string]any)
		x.updateStep(index, func(s *SSMAutomationStep) { s.Inputs = ssmStepInputsWire(inputs) })
		stepCtx, release := x.stepContext(ctx, step, stepID, onCancel)
		result = x.runAction(stepCtx, step, inputs, index)
		cause := context.Cause(stepCtx)
		release()
		if ctx.Err() != nil {
			return "", "", true
		}
		switch {
		case errors.Is(cause, errSSMStepCancelled):
			status = "Cancelled"
		case errors.Is(cause, errSSMStepTimedOut):
			status = "TimedOut"
			result.failure = fmt.Sprintf("Step timed out while step is verifying the %s action. The step exceeded its timeoutSeconds of %d.", step.Action, step.TimeoutSeconds)
			result.failureType = "Verification"
		case result.failure != "":
			status = "Failed"
		default:
			status = "Success"
		}
		if status == "Success" || status == "Cancelled" {
			break
		}
	}
	values := result.values
	outputs := map[string][]string{}
	for name, v := range values {
		outputs[name] = ssmValueStrings(v)
	}
	if status == "Success" {
		if values == nil {
			values = map[string]any{}
		}
		for _, o := range step.Outputs {
			selected, err := jsonpath.Get(o.Selector, result.response)
			if err != nil {
				continue
			}
			typed, err := ssmConvertValue(o.Type, selected)
			if err != nil {
				continue
			}
			values[o.Name] = typed
			outputs[o.Name] = ssmValueStrings(typed)
		}
	}
	x.updateStep(index, func(s *SSMAutomationStep) {
		s.StepStatus = status
		s.ExecEndTime = ssmNowSeconds()
		s.Values = values
		if len(outputs) > 0 {
			s.Outputs = outputs
		}
		s.ResponseCode = result.responseCode
		if status != "Success" && status != "Cancelled" {
			s.FailureMessage = result.failure
			s.FailureType = result.failureType
			if s.FailureStage == "" {
				s.FailureStage = "Execution"
			}
		}
	})
	return status, result.next, false
}

func ssmStepInputsWire(inputs map[string]any) map[string]string {
	out := make(map[string]string, len(inputs))
	for k, v := range inputs {
		encoded, err := json.Marshal(v)
		if err == nil {
			out[k] = string(encoded)
		}
	}
	return out
}

// stepContext bounds an attempt by the step's timeout and ends it when the
// execution is cancelled or, in Interactive mode, the step is stopped. An
// onCancel step runs after the cancellation, for at most two minutes.
func (x *ssmRunner) stepContext(ctx context.Context, step ssmRunbookStep, stepID string, onCancel bool) (context.Context, func()) {
	stepCtx, cancel := context.WithCancelCause(ctx)
	timeout := time.Duration(step.TimeoutSeconds) * time.Second
	if onCancel && (timeout == 0 || timeout > ssmOnCancelLimit) {
		timeout = ssmOnCancelLimit
	}
	if timeout == 0 {
		switch step.Action {
		case "aws:waitForAwsResourceProperty":
			timeout = ssmWaitForPropertyTimeout
		case "aws:pause":
			timeout = ssmPauseTimeout
		}
	}
	timed, cancelTimeout := stepCtx, context.CancelFunc(func() {})
	if timeout > 0 {
		timed, cancelTimeout = context.WithTimeoutCause(stepCtx, timeout, errSSMStepTimedOut)
	}
	watchCtx, stopWatch := context.WithCancel(timed)
	done := make(chan struct{})
	bg.JoinedGo(func() {
		defer close(done)
		_ = ssmAutomationChanges.Await(watchCtx, x.id, func() bool {
			e, ok := x.exec()
			if !ok || (!onCancel && e.StopRequested == "Cancel") || (stepID != "" && e.StopStepID == stepID) {
				cancel(errSSMStepCancelled)
				return true
			}
			return false
		})
	})
	return timed, func() {
		stopWatch()
		<-done
		cancelTimeout()
		cancel(nil)
	}
}

func (x *ssmRunner) runAction(ctx context.Context, step ssmRunbookStep, inputs map[string]any, index int) ssmStepResult {
	exec, _ := x.exec()
	actor := x.actor(exec)
	switch step.Action {
	case "aws:executeAwsApi":
		callCtx, cancel := context.WithTimeout(ctx, ssmExecuteAwsApiLimit)
		defer cancel()
		response, failure := ssmCallAPI(callCtx, actor, inputs)
		if failure != "" {
			return ssmStepFailed("Step fails when it is Execution. %s", failure)
		}
		return ssmStepResult{response: response}
	case "aws:assertAwsResourceProperty":
		response, failure := ssmCallAPI(ctx, actor, ssmWithoutKeys(inputs, "PropertySelector", "DesiredValues"))
		if failure != "" {
			return ssmStepFailed("Step fails when it is Execution. %s", failure)
		}
		matched, actual, err := ssmPropertyMatches(response, inputs)
		if err != "" {
			return ssmStepFailed("%s", err)
		}
		if !matched {
			return ssmStepFailed("Step fails when it is Verification. The property value %s doesn't match the desired values %s.", actual, ssmValueString(inputs["DesiredValues"]))
		}
		return ssmStepResult{}
	case "aws:waitForAwsResourceProperty":
		return x.waitForProperty(ctx, actor, inputs)
	case "aws:sleep":
		return ssmSleep(ctx, inputs)
	case "aws:branch":
		return x.branch(step, inputs)
	case "aws:pause":
		return x.pause(ctx, step, index)
	case "aws:runCommand":
		return x.runCommand(ctx, actor, inputs)
	case "aws:invokeLambdaFunction":
		return ssmInvokeLambda(ctx, actor, inputs)
	case "aws:executeAutomation":
		return x.executeAutomation(ctx, exec, inputs)
	}
	return ssmStepResult{
		failure:     fmt.Sprintf("Step fails when it is Execution. The action %s isn't run by this simulator.", step.Action),
		failureType: "Unknown",
	}
}

func ssmWithoutKeys(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

// ssmBotoServices maps an AWS SDK for Python (Boto3) client namespace, which
// aws:executeAwsApi's Service names, to the simulator's service integration
// name where the two differ.
var ssmBotoServices = map[string]string{
	"events":                  "eventbridge",
	"logs":                    "cloudwatchlogs",
	"stepfunctions":           "sfn",
	"elbv2":                   "elasticloadbalancingv2",
	"elb":                     "elasticloadbalancing",
	"application-autoscaling": "applicationautoscaling",
	"acm-pca":                 "acmpca",
}

// ssmCallAPI calls the operation Service and Api name with the other inputs as
// its request, and returns its response or the error as Boto3 words it.
func ssmCallAPI(ctx context.Context, actor awsActor, inputs map[string]any) (any, string) {
	service, _ := inputs["Service"].(string)
	api, _ := inputs["Api"].(string)
	if service == "" || api == "" {
		return nil, "The Service and Api inputs are required."
	}
	request := ssmWithoutKeys(inputs, "Service", "Api")
	name := strings.ToLower(service)
	if mapped, ok := ssmBotoServices[name]; ok {
		name = mapped
	}
	if !awsSDKImplemented(name) {
		return nil, fmt.Sprintf("The service %s isn't served by this simulator.", service)
	}
	operation := strings.ToLower(api[:1]) + api[1:]
	response, callErr := awsSDKInvoke(ctx, actor, name, operation, request)
	if callErr != nil {
		code := callErr.Name
		if i := strings.LastIndex(code, "."); i >= 0 {
			code = code[i+1:]
		}
		return nil, fmt.Sprintf("An error occurred (%s) when calling the %s operation: %s", code, api, callErr.Cause)
	}
	return response, ""
}

// ssmPropertyMatches reads PropertySelector from response and reports whether
// every value it selects is one of DesiredValues. A Boolean is compared as
// True or False.
func ssmPropertyMatches(response any, inputs map[string]any) (bool, string, string) {
	selector, _ := inputs["PropertySelector"].(string)
	if selector == "" {
		return false, "", "The PropertySelector input is required."
	}
	desired := ssmValueStrings(inputs["DesiredValues"])
	if len(desired) == 0 {
		return false, "", "The DesiredValues input is required."
	}
	selected, err := jsonpath.Get(selector, response)
	if err != nil {
		return false, "", fmt.Sprintf("Step fails when it is Verification. The PropertySelector %s selects nothing in the response.", selector)
	}
	values := []any{selected}
	if list, ok := selected.([]any); ok {
		values = list
	}
	actual := make([]string, 0, len(values))
	all := len(values) > 0
	for _, v := range values {
		s := ssmValueString(v)
		if b, ok := v.(bool); ok {
			s = strings.ToUpper(strconv.FormatBool(b)[:1]) + strconv.FormatBool(b)[1:]
		}
		actual = append(actual, s)
		found := false
		for _, d := range desired {
			if d == s || (strings.EqualFold(d, s) && (s == "True" || s == "False")) {
				found = true
			}
		}
		all = all && found
	}
	return all, ssmValueString(actual), ""
}

func (x *ssmRunner) waitForProperty(ctx context.Context, actor awsActor, inputs map[string]any) ssmStepResult {
	request := ssmWithoutKeys(inputs, "PropertySelector", "DesiredValues")
	for {
		response, failure := ssmCallAPI(ctx, actor, request)
		if ctx.Err() != nil {
			return ssmStepResult{}
		}
		if failure != "" {
			return ssmStepFailed("Step fails when it is Execution. %s", failure)
		}
		matched, _, err := ssmPropertyMatches(response, inputs)
		if err != "" {
			return ssmStepFailed("%s", err)
		}
		if matched {
			return ssmStepResult{}
		}
		timer := time.NewTimer(ssmWaitForPropertyInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ssmStepResult{}
		case <-timer.C:
		}
	}
}

func ssmSleep(ctx context.Context, inputs map[string]any) ssmStepResult {
	var wait time.Duration
	switch {
	case inputs["Duration"] != nil:
		d, err := ssmISODuration(ssmValueString(inputs["Duration"]))
		if err != nil {
			return ssmStepFailed("Step fails when it is Execution. %v", err)
		}
		wait = d
	case inputs["Timestamp"] != nil:
		at, err := time.Parse(time.RFC3339, ssmValueString(inputs["Timestamp"]))
		if err != nil {
			return ssmStepFailed("Step fails when it is Execution. %q isn't an ISO 8601 timestamp.", ssmValueString(inputs["Timestamp"]))
		}
		wait = time.Until(at)
	default:
		return ssmStepFailed("Step fails when it is Execution. aws:sleep needs a Duration or a Timestamp.")
	}
	if wait > ssmSleepLimit {
		return ssmStepFailed("Step fails when it is Execution. Automation supports a maximum delay of 604799 seconds.")
	}
	if wait <= 0 {
		return ssmStepResult{}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	return ssmStepResult{}
}

// branch evaluates the Choices in order and names the first true choice's
// NextStep, else the Default, else the step after the branch
// (ssm-automation-action-branch.md.gz, ssm-automation-branch-condition.md.gz).
func (x *ssmRunner) branch(step ssmRunbookStep, inputs map[string]any) ssmStepResult {
	choices, _ := inputs["Choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		matched, err := ssmEvalChoice(choice)
		if err != nil {
			return ssmStepFailed("Step fails when it is Execution. %v", err)
		}
		if matched {
			next, _ := choice["NextStep"].(string)
			return ssmStepResult{next: next}
		}
	}
	if d, ok := inputs["Default"].(string); ok && d != "" {
		return ssmStepResult{next: d}
	}
	return ssmStepResult{next: x.runbook.following(step.Name)}
}

func ssmEvalChoice(c map[string]any) (bool, error) {
	if and, ok := c["And"].([]any); ok {
		for _, raw := range and {
			sub, _ := raw.(map[string]any)
			ok, err := ssmEvalChoice(sub)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	if or, ok := c["Or"].([]any); ok {
		for _, raw := range or {
			sub, _ := raw.(map[string]any)
			ok, err := ssmEvalChoice(sub)
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if not, ok := c["Not"].(map[string]any); ok {
		ok, err := ssmEvalChoice(not)
		return !ok, err
	}
	variable, present := c["Variable"]
	if !present {
		return false, fmt.Errorf("a choice needs a Variable")
	}
	left := ssmValueString(variable)
	if list, ok := variable.([]any); ok && len(list) == 1 {
		left = ssmValueString(list[0])
	}
	if list, ok := variable.([]string); ok && len(list) == 1 {
		left = list[0]
	}
	number := func(v any) (float64, error) {
		return strconv.ParseFloat(strings.TrimSpace(ssmValueString(v)), 64)
	}
	for op, right := range c {
		r := ssmValueString(right)
		switch op {
		case "StringEquals":
			return left == r, nil
		case "EqualsIgnoreCase":
			return strings.EqualFold(left, r), nil
		case "StartsWith":
			return strings.HasPrefix(left, r), nil
		case "EndsWith":
			return strings.HasSuffix(left, r), nil
		case "Contains":
			return strings.Contains(left, r), nil
		case "BooleanEquals":
			lb, lok := ssmAnyBool(left)
			rb, rok := ssmAnyBool(right)
			if !lok || !rok {
				return false, fmt.Errorf("BooleanEquals compares Booleans, not %q and %q", left, r)
			}
			return lb == rb, nil
		case "NumericEquals", "NumericGreater", "NumericLesser", "NumericGreaterOrEquals", "NumericLesserOrEquals":
			ln, lerr := number(left)
			rn, rerr := number(right)
			if lerr != nil || rerr != nil {
				return false, fmt.Errorf("%s compares numbers, not %q and %q", op, left, r)
			}
			switch op {
			case "NumericEquals":
				return ln == rn, nil
			case "NumericGreater":
				return ln > rn, nil
			case "NumericLesser":
				return ln < rn, nil
			case "NumericGreaterOrEquals":
				return ln >= rn, nil
			}
			return ln <= rn, nil
		}
	}
	return false, fmt.Errorf("the choice names no supported operation")
}

// pause holds the execution Waiting until a Resume signal names the step
// (ssm-automation-action-pause.md.gz).
func (x *ssmRunner) pause(ctx context.Context, step ssmRunbookStep, index int) ssmStepResult {
	x.updateStep(index, func(s *SSMAutomationStep) { s.StepStatus = "Waiting" })
	x.update(func(e *SSMAutomationExecution) {
		if e.Status == "InProgress" {
			e.Status = "Waiting"
		}
	})
	resumed := false
	_ = ssmAutomationChanges.Await(ctx, x.id, func() bool {
		e, ok := x.exec()
		if !ok {
			return true
		}
		for _, s := range e.Signals {
			if s.Type == "Resume" && firstOf(s.Payload["StepName"]) == step.Name {
				resumed = true
				return true
			}
		}
		return false
	})
	x.update(func(e *SSMAutomationExecution) {
		e.Signals = ssmDropSignal(e.Signals, "Resume")
		if e.Status == "Waiting" {
			e.Status = "InProgress"
		}
	})
	if resumed {
		x.updateStep(index, func(s *SSMAutomationStep) { s.StepStatus = "InProgress" })
	}
	return ssmStepResult{}
}

// runCommand sends a command through SendCommand and waits for it to finish
// (ssm-automation-action-runcommand.md.gz).
func (x *ssmRunner) runCommand(ctx context.Context, actor awsActor, inputs map[string]any) ssmStepResult {
	request := map[string]any{}
	for k, v := range inputs {
		request[k] = v
	}
	if params, ok := inputs["Parameters"].(map[string]any); ok {
		converted := map[string][]string{}
		for k, v := range params {
			converted[k] = ssmValueStrings(v)
		}
		request["Parameters"] = converted
	}
	if ids, ok := inputs["InstanceIds"]; ok {
		request["InstanceIds"] = ssmValueStrings(ids)
	}
	status, body, err := awsCallJSON(ctx, actor, "ssm", "AmazonSSM.SendCommand", "application/x-amz-json-1.1", request)
	if failure, failed := ssmCallFailure(status, body, err); failed {
		return ssmStepFailed("Step fails when it is Execution. %s", failure)
	}
	var sent struct {
		Command struct {
			CommandId string `json:"CommandId"`
		} `json:"Command"`
	}
	if json.Unmarshal(body, &sent) != nil || sent.Command.CommandId == "" {
		return ssmStepFailed("Step fails when it is Execution. SendCommand returned no command.")
	}
	commandID := sent.Command.CommandId
	_ = ssmCommandChanges.Await(ctx, commandID, func() bool {
		cmd, ok := ssmCommands.Get(commandID)
		return !ok || (cmd.Status != "Pending" && cmd.Status != "InProgress")
	})
	if ctx.Err() != nil {
		_, _, _ = awsCallJSON(context.Background(), actor, "ssm", "AmazonSSM.CancelCommand", "application/x-amz-json-1.1",
			map[string]any{"CommandId": commandID})
		return ssmStepResult{}
	}
	cmd, _ := ssmCommands.Get(commandID)
	values := map[string]any{"CommandId": commandID, "Status": cmd.Status}
	if len(cmd.Invocations) == 1 {
		inv := cmd.Invocations[0]
		values["ResponseCode"] = int64(inv.ResponseCode)
		values["Output"] = inv.StandardOutput
	}
	result := ssmStepResult{values: values, response: values}
	if cmd.Status != "Success" {
		result.failure = fmt.Sprintf("Step fails when it is Poll action status for completion. The command %s finished with status %s.", commandID, cmd.Status)
		result.failureType = "Verification"
	}
	return result
}

// ssmInvokeLambda invokes the function and fails the step on a function error
// (ssm-automation-action-lamb.md.gz).
func ssmInvokeLambda(ctx context.Context, actor awsActor, inputs map[string]any) ssmStepResult {
	function := ssmValueString(inputs["FunctionName"])
	if function == "" {
		return ssmStepFailed("Step fails when it is Execution. The FunctionName input is required.")
	}
	payload := []byte(ssmValueString(inputs["Payload"]))
	if input, ok := inputs["InputPayload"]; ok {
		encoded, err := json.Marshal(input)
		if err != nil {
			return ssmStepFailed("Step fails when it is Execution. InputPayload isn't JSON.")
		}
		payload = encoded
	}
	invocationType := ssmValueString(inputs["InvocationType"])
	if invocationType == "" {
		invocationType = "RequestResponse"
	}
	header := http.Header{"X-Amz-Invocation-Type": {invocationType}, "Content-Type": {"application/json"}}
	if logType := ssmValueString(inputs["LogType"]); logType != "" {
		header.Set("X-Amz-Log-Type", logType)
	}
	if clientContext := ssmValueString(inputs["ClientContext"]); clientContext != "" {
		header.Set("X-Amz-Client-Context", clientContext)
	}
	path := "/2015-03-31/functions/" + url.PathEscape(function) + "/invocations"
	if qualifier := ssmValueString(inputs["Qualifier"]); qualifier != "" {
		path += "?Qualifier=" + url.QueryEscape(qualifier)
	}
	response, err := awsCallResponse(ctx, actor, "lambda", http.MethodPost, path, header, payload)
	if err != nil {
		return ssmStepFailed("Step fails when it is Execution. %v", err)
	}
	body := response.Body.Bytes()
	if response.StatusCode >= http.StatusBadRequest {
		code, message := awsJSONError(body)
		return ssmStepFailed("Step fails when it is Execution. An error occurred (%s) when calling the Invoke operation: %s", code, message)
	}
	values := map[string]any{"StatusCode": int64(response.StatusCode), "Payload": string(body)}
	if fnErr := response.Header().Get("X-Amz-Function-Error"); fnErr != "" {
		values["FunctionError"] = fnErr
	}
	if logs := response.Header().Get("X-Amz-Log-Result"); logs != "" {
		values["LogResult"] = logs
	}
	selectable := map[string]any{}
	for k, v := range values {
		selectable[k] = v
	}
	var decoded any
	if len(body) > 0 && json.Unmarshal(body, &decoded) == nil {
		selectable["Payload"] = decoded
	}
	result := ssmStepResult{values: values, response: selectable, responseCode: strconv.Itoa(response.StatusCode)}
	if fnErr, ok := values["FunctionError"]; ok {
		result.failure = fmt.Sprintf("Step fails when it is Execution. The Lambda function returned a %s error: %s", fnErr, string(body))
		result.failureType = "Verification"
	}
	return result
}

// executeAutomation runs a secondary runbook as a child automation and waits
// for it to finish (ssm-automation-action-executeAutomation.md.gz).
func (x *ssmRunner) executeAutomation(ctx context.Context, parent SSMAutomationExecution, inputs map[string]any) ssmStepResult {
	params := map[string][]string{}
	if runtime, ok := inputs["RuntimeParameters"].(map[string]any); ok {
		for k, v := range runtime {
			params[k] = ssmValueStrings(v)
		}
	}
	request := map[string]any{"DocumentName": ssmValueString(inputs["DocumentName"]), "Parameters": params}
	for _, key := range []string{"DocumentVersion", "TargetParameterName", "MaxConcurrency", "MaxErrors"} {
		if v := ssmValueString(inputs[key]); v != "" {
			request[key] = v
		}
	}
	for _, key := range []string{"Targets", "TargetMaps", "Tags", "TargetLocations"} {
		if v, ok := inputs[key]; ok {
			request[key] = v
		}
	}
	status, body, err := awsCallJSON(ctx, x.actor(parent), "ssm", "AmazonSSM.StartAutomationExecution", "application/x-amz-json-1.1", request)
	if failure, failed := ssmCallFailure(status, body, err); failed {
		return ssmStepFailed("Step fails when it is Execution. %s", failure)
	}
	var started struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
	}
	if json.Unmarshal(body, &started) != nil || started.AutomationExecutionId == "" {
		return ssmStepFailed("Step fails when it is Execution. StartAutomationExecution returned no execution.")
	}
	childID := started.AutomationExecutionId
	ssmAutomations.Update(childID, func(e *SSMAutomationExecution) { e.ParentId = parent.AutomationExecutionId })
	child := ssmAwaitAutomation(ctx, childID)
	if ctx.Err() != nil {
		ssmAutomations.Update(childID, func(e *SSMAutomationExecution) {
			if !ssmAutomationTerminal(e.Status) {
				e.StopRequested, e.Status = "Cancel", "Cancelling"
			}
		})
		return ssmStepResult{}
	}
	var output []string
	for _, key := range ssmSortedKeys(child.Outputs) {
		output = append(output, child.Outputs[key]...)
	}
	values := map[string]any{"ExecutionId": childID, "Status": child.Status, "Output": output}
	result := ssmStepResult{values: values, response: values}
	if child.Status != "Success" {
		result.failure = fmt.Sprintf("Step fails when it is Execution. The secondary automation %s finished with status %s.", childID, child.Status)
		result.failureType = "Verification"
	}
	return result
}

func ssmAwaitAutomation(ctx context.Context, id string) SSMAutomationExecution {
	_ = ssmAutomationChanges.Await(ctx, id, func() bool {
		e, ok := ssmAutomations.Get(id)
		return !ok || ssmAutomationTerminal(e.Status)
	})
	e, _ := ssmAutomations.Get(id)
	return e
}

// ssmStartChildAutomation starts a child of parent with the parent's runbook
// and identity, the parameters overridden by overrides.
func ssmStartChildAutomation(ctx context.Context, parent SSMAutomationExecution, overrides map[string][]string, target string) (string, string) {
	params := map[string][]string{}
	for k, v := range parent.Parameters {
		params[k] = v
	}
	for k, v := range overrides {
		params[k] = v
	}
	child := SSMAutomationExecution{
		AutomationExecutionId: uuid.New().String(),
		DocumentName:          parent.DocumentName,
		DocumentVersion:       parent.DocumentVersion,
		Content:               parent.Content,
		Format:                parent.Format,
		Mode:                  "Auto",
		ExecutedBy:            parent.ExecutedBy,
		Identity:              parent.Identity,
		ParentId:              parent.AutomationExecutionId,
		TargetParameterName:   parent.TargetParameterName,
		Target:                target,
	}
	if failure := ssmPrepareAutomation(ctx, &child, params); failure != "" {
		return "", failure
	}
	ssmAutomations.Put(child.AutomationExecutionId, child)
	ssmStartAutomationRunner(child.AutomationExecutionId)
	return child.AutomationExecutionId, ""
}

// runParent runs a rate-controlled automation: a child per target, sent to one
// target first and then exponentially more up to MaxConcurrency, and no more
// once the errors exceed MaxErrors (ssm-running-automations-scale-controls.md.gz).
func (x *ssmRunner) runParent(ctx context.Context) {
	parent, _ := x.exec()
	x.update(func(e *SSMAutomationExecution) {
		if e.Status == "Pending" {
			e.Status = "InProgress"
		}
	})
	type item struct {
		overrides map[string][]string
		target    string
	}
	var items []item
	for _, m := range parent.TargetMaps {
		items = append(items, item{overrides: m})
	}
	for _, v := range parent.ResolvedTargets {
		items = append(items, item{overrides: map[string][]string{parent.TargetParameterName: {v}}, target: v})
	}
	existing := ssmAutomations.Filter(func(c SSMAutomationExecution) bool { return c.ParentId == x.id })
	concurrency := max(ssmRateLimit(parent.MaxConcurrency, len(items), 10), 1)
	maxErrors := ssmRateLimit(parent.MaxErrors, len(items), len(items))
	failures, batch, next := 0, 1, 0
	var children []string
	for _, c := range existing {
		children = append(children, c.AutomationExecutionId)
		next++
	}
	for next < len(items) && failures <= maxErrors && ctx.Err() == nil {
		if e, _ := x.exec(); e.StopRequested != "" {
			break
		}
		size := min(batch, concurrency, len(items)-next)
		var started []string
		for _, it := range items[next : next+size] {
			id, failure := ssmStartChildAutomation(ctx, parent, it.overrides, it.target)
			if failure != "" {
				x.finish("Failed", failure)
				return
			}
			started = append(started, id)
		}
		next += size
		children = append(children, started...)
		for _, id := range started {
			if child := ssmAwaitAutomation(ctx, id); child.Status != "Success" {
				failures++
			}
		}
		batch *= 2
	}
	if ctx.Err() != nil {
		return
	}
	failures = 0
	cancelled := false
	for _, id := range children {
		child := ssmAwaitAutomation(ctx, id)
		switch child.Status {
		case "Success":
		case "Cancelled":
			cancelled = true
		default:
			failures++
		}
	}
	if ctx.Err() != nil {
		return
	}
	latest, _ := x.exec()
	switch {
	case cancelled || latest.StopRequested == "Cancel":
		x.finish("Cancelled", "")
	case failures > 0:
		x.finish("Failed", fmt.Sprintf("%d of %d child automations failed.", failures, len(children)))
	default:
		x.finish("Success", "")
	}
}

// runChangeRunbooks runs a change request's runbooks in order once its change
// template's steps succeeded.
func (x *ssmRunner) runChangeRunbooks(ctx context.Context) {
	exec, _ := x.exec()
	for _, rb := range exec.Runbooks {
		doc, ok := ssmDocuments.Get(rb.DocumentName)
		if !ok {
			x.finish("Failed", "The runbook "+rb.DocumentName+" no longer exists.")
			return
		}
		version, ok := ssmDocVersion(doc, rb.DocumentVersion)
		if !ok {
			x.finish("Failed", "The runbook "+rb.DocumentName+" version "+rb.DocumentVersion+" no longer exists.")
			return
		}
		child := exec
		child.DocumentName, child.DocumentVersion = rb.DocumentName, version.DocumentVersion
		child.Content, child.Format = version.Content, version.DocumentFormat
		child.Parameters = nil
		id, failure := ssmStartChildAutomation(ctx, child, rb.Parameters, "")
		if failure != "" {
			x.finish("Failed", failure)
			return
		}
		result := ssmAwaitAutomation(ctx, id)
		if ctx.Err() != nil {
			return
		}
		if result.Status != "Success" {
			x.finish("Failed", fmt.Sprintf("The runbook %s finished with status %s.", rb.DocumentName, result.Status))
			return
		}
	}
	x.update(func(e *SSMAutomationExecution) {
		if !ssmAutomationTerminal(e.Status) {
			e.Status, e.EndTime = "Success", ssmNowSeconds()
		}
	})
}

// ssmReadParameterStore reads a String or StringList parameter a runbook
// parameter's default names, as the caller.
func ssmReadParameterStore(ctx context.Context, identity ssmAutomationIdentity, name string) (any, string) {
	actor := (&ssmRunner{}).actor(SSMAutomationExecution{Identity: identity})
	response, callErr := awsSDKInvoke(ctx, actor, "ssm", "getParameter", map[string]any{"Name": name})
	if callErr != nil {
		return nil, fmt.Sprintf("the Parameter Store parameter %s can't be read: %s", name, callErr.Cause)
	}
	value, err := jsonpath.Get("$.Parameter.Value", response)
	if err != nil {
		return nil, "the Parameter Store parameter " + name + " has no value"
	}
	kind, _ := jsonpath.Get("$.Parameter.Type", response)
	if kind == "SecureString" {
		return nil, "a runbook parameter can't reference the SecureString parameter " + name
	}
	if kind == "StringList" {
		var list []any
		for _, item := range strings.Split(ssmValueString(value), ",") {
			list = append(list, item)
		}
		return list, ""
	}
	return value, ""
}
