package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Additional CloudWatch Logs control-plane operations: log-stream deletion,
// metric filters, subscription filters, retention deletion, the legacy
// log-group tag verbs, UntagResource, export tasks, and data-protection
// policies. These complement the core log-group/stream/event handlers in
// cloudwatch.go. All operate on real stores at CloudWatch Logs API fidelity.

// CWMetricTransformation mirrors the SDK MetricTransformation shape.
type CWMetricTransformation struct {
	MetricName      string            `json:"metricName"`
	MetricNamespace string            `json:"metricNamespace"`
	MetricValue     string            `json:"metricValue"`
	DefaultValue    *float64          `json:"defaultValue,omitempty"`
	Unit            string            `json:"unit,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
}

// CWMetricFilter mirrors the SDK MetricFilter shape.
type CWMetricFilter struct {
	FilterName            string                   `json:"filterName"`
	FilterPattern         string                   `json:"filterPattern"`
	LogGroupName          string                   `json:"logGroupName"`
	CreationTime          int64                    `json:"creationTime"`
	MetricTransformations []CWMetricTransformation `json:"metricTransformations"`
}

// CWSubscriptionFilter mirrors the SDK SubscriptionFilter shape.
type CWSubscriptionFilter struct {
	FilterName     string `json:"filterName"`
	FilterPattern  string `json:"filterPattern"`
	LogGroupName   string `json:"logGroupName"`
	DestinationArn string `json:"destinationArn"`
	RoleArn        string `json:"roleArn,omitempty"`
	Distribution   string `json:"distribution,omitempty"`
	CreationTime   int64  `json:"creationTime"`
}

// CWExportTask mirrors the SDK ExportTask shape.
type CWExportTask struct {
	TaskId            string                    `json:"taskId"`
	TaskName          string                    `json:"taskName,omitempty"`
	LogGroupName      string                    `json:"logGroupName"`
	From              int64                     `json:"from"`
	To                int64                     `json:"to"`
	Destination       string                    `json:"destination"`
	DestinationPrefix string                    `json:"destinationPrefix,omitempty"`
	Status            CWExportTaskStatus        `json:"status"`
	ExecutionInfo     CWExportTaskExecutionInfo `json:"executionInfo"`
}

type CWExportTaskStatus struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type CWExportTaskExecutionInfo struct {
	CreationTime   int64 `json:"creationTime"`
	CompletionTime int64 `json:"completionTime,omitempty"`
}

// CWDataProtectionPolicy holds a log group's data-protection policy document.
type CWDataProtectionPolicy struct {
	LogGroupName    string `json:"-"`
	PolicyDocument  string `json:"policyDocument"`
	LastUpdatedTime int64  `json:"lastUpdatedTime"`
}

var (
	cwMetricFilters       sim.Store[CWMetricFilter]
	cwSubscriptionFilters sim.Store[CWSubscriptionFilter]
	cwExportTasks         sim.Store[CWExportTask]
	cwDataProtection      sim.Store[CWDataProtectionPolicy]
)

func registerCloudWatchLogsOps(r *sim.AWSRouter, srv *sim.Server) {
	cwMetricFilters = sim.MakeStore[CWMetricFilter](srv.DB(), "cw_metric_filters")
	cwSubscriptionFilters = sim.MakeStore[CWSubscriptionFilter](srv.DB(), "cw_subscription_filters")
	cwExportTasks = sim.MakeStore[CWExportTask](srv.DB(), "cw_export_tasks")
	cwDataProtection = sim.MakeStore[CWDataProtectionPolicy](srv.DB(), "cw_data_protection")

	r.Register("Logs_20140328.DeleteLogStream", handleCWDeleteLogStream)
	r.Register("Logs_20140328.PutMetricFilter", handleCWPutMetricFilter)
	r.Register("Logs_20140328.DescribeMetricFilters", handleCWDescribeMetricFilters)
	r.Register("Logs_20140328.DeleteMetricFilter", handleCWDeleteMetricFilter)
	r.Register("Logs_20140328.TestMetricFilter", handleCWTestMetricFilter)
	r.Register("Logs_20140328.PutSubscriptionFilter", handleCWPutSubscriptionFilter)
	r.Register("Logs_20140328.DescribeSubscriptionFilters", handleCWDescribeSubscriptionFilters)
	r.Register("Logs_20140328.DeleteSubscriptionFilter", handleCWDeleteSubscriptionFilter)
	r.Register("Logs_20140328.DeleteRetentionPolicy", handleCWDeleteRetentionPolicy)
	r.Register("Logs_20140328.TagLogGroup", handleCWTagLogGroup)
	r.Register("Logs_20140328.UntagLogGroup", handleCWUntagLogGroup)
	r.Register("Logs_20140328.ListTagsLogGroup", handleCWListTagsLogGroup)
	r.Register("Logs_20140328.UntagResource", handleCWUntagResource)
	r.Register("Logs_20140328.CreateExportTask", handleCWCreateExportTask)
	r.Register("Logs_20140328.DescribeExportTasks", handleCWDescribeExportTasks)
	r.Register("Logs_20140328.CancelExportTask", handleCWCancelExportTask)
	r.Register("Logs_20140328.PutDataProtectionPolicy", handleCWPutDataProtectionPolicy)
	r.Register("Logs_20140328.GetDataProtectionPolicy", handleCWGetDataProtectionPolicy)
	r.Register("Logs_20140328.DeleteDataProtectionPolicy", handleCWDeleteDataProtectionPolicy)
}

// cwFilterKey scopes a per-group named filter (metric/subscription) so two
// groups may share a filter name, matching real CloudWatch Logs.
func cwFilterKey(group, name string) string {
	return group + ":" + name
}

func handleCWDeleteLogStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.LogStreamName == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName and logStreamName are required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	key := cwEventsKey(req.LogGroupName, req.LogStreamName)
	if !cwLogStreams.Delete(key) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log stream does not exist: %s", req.LogStreamName)
		return
	}
	cwLogEvents.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutMetricFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName          string                   `json:"logGroupName"`
		FilterName            string                   `json:"filterName"`
		FilterPattern         string                   `json:"filterPattern"`
		MetricTransformations []CWMetricTransformation `json:"metricTransformations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.FilterName == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName and filterName are required", http.StatusBadRequest)
		return
	}
	if len(req.MetricTransformations) == 0 {
		sim.AWSError(w, "InvalidParameterException", "metricTransformations is required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	if _, err := cwCompileLogPattern(req.FilterPattern); err != nil {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Invalid filter pattern: %s", err.Error())
		return
	}
	key := cwFilterKey(req.LogGroupName, req.FilterName)
	creation := time.Now().UnixMilli()
	if existing, ok := cwMetricFilters.Get(key); ok {
		creation = existing.CreationTime
	}
	cwMetricFilters.Put(key, CWMetricFilter{
		FilterName:            req.FilterName,
		FilterPattern:         req.FilterPattern,
		LogGroupName:          req.LogGroupName,
		CreationTime:          creation,
		MetricTransformations: req.MetricTransformations,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeMetricFilters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
		MetricName       string `json:"metricName"`
		MetricNamespace  string `json:"metricNamespace"`
		Limit            int    `json:"limit"`
		NextToken        string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	filters := cwMetricFilters.Filter(func(f CWMetricFilter) bool {
		if req.LogGroupName != "" && f.LogGroupName != req.LogGroupName {
			return false
		}
		if req.FilterNamePrefix != "" && !strings.HasPrefix(f.FilterName, req.FilterNamePrefix) {
			return false
		}
		if req.MetricName != "" || req.MetricNamespace != "" {
			matched := false
			for _, t := range f.MetricTransformations {
				if req.MetricName != "" && t.MetricName != req.MetricName {
					continue
				}
				if req.MetricNamespace != "" && t.MetricNamespace != req.MetricNamespace {
					continue
				}
				matched = true
				break
			}
			if !matched {
				return false
			}
		}
		return true
	})
	if filters == nil {
		filters = []CWMetricFilter{}
	}
	sortBy(filters, func(f CWMetricFilter) string { return f.FilterName })
	page, next := awsPage(filters, req.NextToken, req.Limit, 50)
	out := map[string]any{"metricFilters": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWDeleteMetricFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
		FilterName   string `json:"filterName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.FilterName == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName and filterName are required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	if !cwMetricFilters.Delete(cwFilterKey(req.LogGroupName, req.FilterName)) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified metric filter does not exist: %s", req.FilterName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWTestMetricFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilterPattern    string   `json:"filterPattern"`
		LogEventMessages []string `json:"logEventMessages"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.FilterPattern == "" {
		sim.AWSError(w, "InvalidParameterException", "filterPattern is required", http.StatusBadRequest)
		return
	}
	pattern, err := cwCompileLogPattern(req.FilterPattern)
	if err != nil {
		sim.AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}
	matches := []map[string]any{}
	for i, msg := range req.LogEventMessages {
		if !pattern.match(msg) {
			continue
		}
		matches = append(matches, map[string]any{
			"eventNumber":     int64(i + 1),
			"eventMessage":    msg,
			"extractedValues": map[string]string{},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"matches": matches})
}

func handleCWPutSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName   string `json:"logGroupName"`
		FilterName     string `json:"filterName"`
		FilterPattern  string `json:"filterPattern"`
		DestinationArn string `json:"destinationArn"`
		RoleArn        string `json:"roleArn"`
		Distribution   string `json:"distribution"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.FilterName == "" || req.DestinationArn == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName, filterName and destinationArn are required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	if _, err := cwCompileLogPattern(req.FilterPattern); err != nil {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Invalid filter pattern: %s", err.Error())
		return
	}
	key := cwFilterKey(req.LogGroupName, req.FilterName)
	creation := time.Now().UnixMilli()
	if existing, ok := cwSubscriptionFilters.Get(key); ok {
		creation = existing.CreationTime
	}
	cwSubscriptionFilters.Put(key, CWSubscriptionFilter{
		FilterName:     req.FilterName,
		FilterPattern:  req.FilterPattern,
		LogGroupName:   req.LogGroupName,
		DestinationArn: req.DestinationArn,
		RoleArn:        req.RoleArn,
		Distribution:   req.Distribution,
		CreationTime:   creation,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeSubscriptionFilters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
		Limit            int    `json:"limit"`
		NextToken        string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	filters := cwSubscriptionFilters.Filter(func(f CWSubscriptionFilter) bool {
		if f.LogGroupName != req.LogGroupName {
			return false
		}
		if req.FilterNamePrefix != "" {
			return strings.HasPrefix(f.FilterName, req.FilterNamePrefix)
		}
		return true
	})
	if filters == nil {
		filters = []CWSubscriptionFilter{}
	}
	sortBy(filters, func(f CWSubscriptionFilter) string { return f.FilterName })
	page, next := awsPage(filters, req.NextToken, req.Limit, 50)
	out := map[string]any{"subscriptionFilters": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWDeleteSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
		FilterName   string `json:"filterName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.FilterName == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName and filterName are required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	if !cwSubscriptionFilters.Delete(cwFilterKey(req.LogGroupName, req.FilterName)) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified subscription filter does not exist: %s", req.FilterName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDeleteRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}
	if !cwLogGroups.Update(req.LogGroupName, func(lg *CWLogGroup) { lg.RetentionInDays = 0 }) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWTagLogGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string            `json:"logGroupName"`
		Tags         map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwLogGroups.Update(req.LogGroupName, func(lg *CWLogGroup) {
		if lg.Tags == nil {
			lg.Tags = map[string]string{}
		}
		for k, v := range req.Tags {
			lg.Tags[k] = v
		}
	}) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWUntagLogGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string   `json:"logGroupName"`
		Tags         []string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwLogGroups.Update(req.LogGroupName, func(lg *CWLogGroup) {
		for _, k := range req.Tags {
			delete(lg.Tags, k)
		}
	}) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWListTagsLogGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	tags := map[string]string{}
	if lg, ok := cwLogGroups.Get(req.LogGroupName); ok && lg.Tags != nil {
		tags = lg.Tags
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func handleCWUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := cwLogGroupByArn(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "log group not found: %s", req.ResourceArn)
		return
	}
	cwLogGroups.Update(name, func(lg *CWLogGroup) {
		for _, k := range req.TagKeys {
			delete(lg.Tags, k)
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWCreateExportTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName        string `json:"logGroupName"`
		TaskName            string `json:"taskName"`
		From                int64  `json:"from"`
		To                  int64  `json:"to"`
		Destination         string `json:"destination"`
		DestinationPrefix   string `json:"destinationPrefix"`
		LogStreamNamePrefix string `json:"logStreamNamePrefix"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.Destination == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupName and destination are required", http.StatusBadRequest)
		return
	}
	if req.To <= req.From {
		sim.AWSError(w, "InvalidParameterException", "to must be greater than from", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	now := time.Now().UnixMilli()
	taskId := generateUUID()
	task := CWExportTask{
		TaskId:            taskId,
		TaskName:          req.TaskName,
		LogGroupName:      req.LogGroupName,
		From:              req.From,
		To:                req.To,
		Destination:       req.Destination,
		DestinationPrefix: req.DestinationPrefix,
		Status:            CWExportTaskStatus{Code: "COMPLETED", Message: "Completed successfully"},
		ExecutionInfo:     CWExportTaskExecutionInfo{CreationTime: now, CompletionTime: now},
	}
	cwExportTasks.Put(taskId, task)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"taskId": taskId})
}

func handleCWDescribeExportTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskId     string `json:"taskId"`
		StatusCode string `json:"statusCode"`
		Limit      int    `json:"limit"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	tasks := cwExportTasks.Filter(func(t CWExportTask) bool {
		if req.TaskId != "" && t.TaskId != req.TaskId {
			return false
		}
		if req.StatusCode != "" && t.Status.Code != req.StatusCode {
			return false
		}
		return true
	})
	if tasks == nil {
		tasks = []CWExportTask{}
	}
	sortBy(tasks, func(t CWExportTask) string { return t.TaskId })
	page, next := awsPage(tasks, req.NextToken, req.Limit, 50)
	out := map[string]any{"exportTasks": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWCancelExportTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskId string `json:"taskId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	task, ok := cwExportTasks.Get(req.TaskId)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified export task does not exist: %s", req.TaskId)
		return
	}
	// Real CloudWatch Logs rejects cancelling a task that already reached a
	// terminal state (COMPLETED/FAILED/CANCELLED) with InvalidOperationException.
	switch task.Status.Code {
	case "COMPLETED", "FAILED", "CANCELLED":
		sim.AWSError(w, "InvalidOperationException",
			"The specified export task has already completed.", http.StatusBadRequest)
		return
	}
	cwExportTasks.Update(req.TaskId, func(t *CWExportTask) {
		t.Status = CWExportTaskStatus{Code: "CANCELLED", Message: "Cancelled by user"}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutDataProtectionPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		PolicyDocument     string `json:"policyDocument"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupIdentifier == "" || req.PolicyDocument == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupIdentifier and policyDocument are required", http.StatusBadRequest)
		return
	}
	name, ok := cwResolveLogGroupIdentifier(req.LogGroupIdentifier)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupIdentifier)
		return
	}
	now := time.Now().UnixMilli()
	cwDataProtection.Put(name, CWDataProtectionPolicy{
		LogGroupName:    name,
		PolicyDocument:  req.PolicyDocument,
		LastUpdatedTime: now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"logGroupIdentifier": name,
		"policyDocument":     req.PolicyDocument,
		"lastUpdatedTime":    now,
	})
}

func handleCWGetDataProtectionPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := cwResolveLogGroupIdentifier(req.LogGroupIdentifier)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupIdentifier)
		return
	}
	policy, ok := cwDataProtection.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"No data protection policy found for log group: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"logGroupIdentifier": name,
		"policyDocument":     policy.PolicyDocument,
		"lastUpdatedTime":    policy.LastUpdatedTime,
	})
}

func handleCWDeleteDataProtectionPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := cwResolveLogGroupIdentifier(req.LogGroupIdentifier)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupIdentifier)
		return
	}
	cwDataProtection.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cwResolveLogGroupIdentifier accepts either a bare log-group name or a
// log-group ARN (the data-protection APIs take logGroupIdentifier, which may be
// either) and returns the canonical group name if it exists.
func cwResolveLogGroupIdentifier(id string) (string, bool) {
	if strings.HasPrefix(id, "arn:") {
		return cwLogGroupByArn(id)
	}
	if _, ok := cwLogGroups.Get(id); ok {
		return id, true
	}
	return "", false
}

// cwEvaluateMetricFilters applies every metric filter whose LogGroupName
// matches to the freshly ingested events, publishing each transformation's
// metric datum into the shared metric store — the path by which a
// filter-pattern match turns into a queryable CloudWatch metric and a fired
// alarm. Real CloudWatch evaluates filters asynchronously after PutLogEvents;
// the simulator evaluates inline so a follow-up GetMetricStatistics observes
// the new datapoints deterministically. It takes stored events so every
// producer — PutLogEvents and the container log drivers alike — reaches the
// filters through one signature.
func cwEvaluateMetricFilters(logGroupName string, events []CWLogEvent) {
	filters := cwMetricFilters.Filter(func(f CWMetricFilter) bool {
		return f.LogGroupName == logGroupName
	})
	if len(filters) == 0 {
		return
	}
	for _, e := range events {
		for _, f := range filters {
			pattern, err := cwCompileLogPattern(f.FilterPattern)
			if err != nil || !pattern.match(e.Message) {
				continue
			}
			for _, t := range f.MetricTransformations {
				cwPublishFilterMetric(t, e.Message, e.Timestamp)
			}
		}
	}
}

// cwPublishFilterMetric emits a single metric datum derived from a matched
// event under the transformation's namespace/metric. MetricValue resolves to a
// literal number, a "$." JSON-path token (summed across the event), or falls
// back to DefaultValue when neither yields a number. Dimensions declared on the
// transformation resolve literal values verbatim and "$." tokens against the
// event JSON.
func cwPublishFilterMetric(t CWMetricTransformation, message string, tsMillis int64) {
	value, ok := cwResolveMetricValue(t.MetricValue, message)
	if !ok && t.DefaultValue != nil {
		value = *t.DefaultValue
		ok = true
	}
	if !ok {
		return
	}
	dims := make([]CWDimension, 0, len(t.Dimensions))
	for name, raw := range t.Dimensions {
		dims = append(dims, CWDimension{Name: name, Value: cwResolveDimValue(raw, message)})
	}
	cwStoreDatum(CWMetricDatum{
		Namespace:  t.MetricNamespace,
		MetricName: t.MetricName,
		Dimensions: dims,
		Value:      value,
		Timestamp:  float64(tsMillis / 1000),
		Unit:       t.Unit,
	})
}

// cwResolveMetricValue parses a MetricTransformation value token: a literal
// float ("1", "2.5"), or a "$.path" JSON selector that extracts a numeric field
// from the matched event. Returns false when the token is neither a number nor
// a resolvable numeric JSON field.
func cwResolveMetricValue(token, message string) (float64, bool) {
	token = strings.TrimSpace(token)
	if v, err := strconv.ParseFloat(token, 64); err == nil {
		return v, true
	}
	if strings.HasPrefix(token, "$") {
		var doc any
		if err := json.Unmarshal([]byte(message), &doc); err != nil {
			return 0, false
		}
		got, present := cwSelectJSON(doc, token)
		if !present {
			return 0, false
		}
		if f, err := strconv.ParseFloat(cwJSONScalar(got), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// cwResolveDimValue resolves a dimension value, treating "$." tokens as JSON
// selectors into the event and everything else as a literal.
func cwResolveDimValue(token, message string) string {
	if !strings.HasPrefix(token, "$") {
		return token
	}
	var doc any
	if err := json.Unmarshal([]byte(message), &doc); err != nil {
		return token
	}
	got, present := cwSelectJSON(doc, token)
	if !present {
		return token
	}
	return cwJSONScalar(got)
}
