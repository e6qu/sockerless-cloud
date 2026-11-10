package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
	"github.com/google/uuid"
)

// CreateImportTask imports the events an AWS CloudTrail Lake event data store
// holds into a managed log group, as the CreateImportTask reference describes
// it: the source must be ENABLED, and the import role must trust
// cloudtrail.amazonaws.com and allow cloudtrail:GetEventDataStoreData,
// logs:CreateLogGroup and logs:CreateLogStream. The importer reads the
// events, within the importFilter's eventTime range, in batches by event day,
// writing each as the CloudTrail record JSON at its eventTime.

type CWImportBatch struct {
	BatchId      string `json:"batchId"`
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Day          string `json:"day"`
}

type cwImportFilter struct {
	StartEventTime *int64 `json:"startEventTime"`
	EndEventTime   *int64 `json:"endEventTime"`
}

func registerCloudWatchLogsImports(srv *sim.Server) {
	kvstore.StartSweeper(srv, "CloudWatch Logs import tasks", time.Second, func(time.Time) { cwRunImportTasks() })
}

// cwImportDestination is the managed log group an event data store imports
// into.
func cwImportDestination(edsArn string) string {
	return "aws/cloudtrail/" + edsArn[strings.LastIndex(edsArn, "/")+1:]
}

func handleCWCreateImportTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportSourceArn string          `json:"importSourceArn"`
		ImportRoleArn   string          `json:"importRoleArn"`
		ImportFilter    json.RawMessage `json:"importFilter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ImportSourceArn == "" || req.ImportRoleArn == "" {
		AWSError(w, "InvalidParameterException", "importSourceArn and importRoleArn are required", http.StatusBadRequest)
		return
	}
	var filter cwImportFilter
	if len(req.ImportFilter) > 0 {
		if err := json.Unmarshal(req.ImportFilter, &filter); err != nil {
			AWSError(w, "InvalidParameterException", "importFilter is not valid", http.StatusBadRequest)
			return
		}
		if filter.StartEventTime != nil && filter.EndEventTime != nil && *filter.StartEventTime > *filter.EndEventTime {
			AWSError(w, "InvalidParameterException", "startEventTime must not be after endEventTime", http.StatusBadRequest)
			return
		}
	}
	eds, ok := findCloudTrailEDS(req.ImportSourceArn)
	if !ok || eds.ARN != req.ImportSourceArn {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified import source does not exist: %s", req.ImportSourceArn)
		return
	}
	if eds.Status != "ENABLED" {
		AWSErrorf(w, "InvalidOperationException", http.StatusBadRequest,
			"The import source %s is not active.", req.ImportSourceArn)
		return
	}
	group := cwImportDestination(eds.ARN)
	destArn := cwLogGroupArn(group)
	if err := iamValidateServiceRole(req.ImportRoleArn, "cloudtrail.amazonaws.com", map[string]string{
		"cloudtrail:GetEventDataStoreData": eds.ARN,
		"logs:CreateLogGroup":              destArn + ":*",
		"logs:CreateLogStream":             destArn + ":*",
	}); err != nil {
		AWSError(w, "AccessDeniedException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, t := range cwImportTasks.List() {
		if t.ImportSourceArn == eds.ARN && t.ImportStatus == "IN_PROGRESS" {
			AWSErrorf(w, "ConflictException", http.StatusBadRequest,
				"An import from %s is already in progress: %s", eds.ARN, t.ImportId)
			return
		}
	}
	id := uuid.New().String()
	now := time.Now().UnixMilli()
	cwImportTasks.Put(id, CWImportTask{
		ImportId:             id,
		ImportSourceArn:      eds.ARN,
		ImportDestinationArn: destArn,
		ImportRoleArn:        req.ImportRoleArn,
		ImportStatus:         "IN_PROGRESS",
		ImportFilter:         req.ImportFilter,
		CreationTime:         now,
		LastUpdatedTime:      now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"importId":             id,
		"importDestinationArn": destArn,
		"creationTime":         now,
	})
}

func cwRunImportTasks() {
	for _, t := range cwImportTasks.List() {
		if t.ImportStatus == "IN_PROGRESS" {
			cwRunImportTask(t)
		}
	}
}

func cwImportFail(id, message string) {
	cwImportTasks.Update(id, func(t *CWImportTask) {
		if t.ImportStatus == "IN_PROGRESS" {
			t.ImportStatus, t.ErrorMessage, t.LastUpdatedTime = "FAILED", message, time.Now().UnixMilli()
		}
	})
}

func cwRunImportTask(task CWImportTask) {
	eds, ok := findCloudTrailEDS(task.ImportSourceArn)
	if !ok {
		cwImportFail(task.ImportId, "The import source no longer exists: "+task.ImportSourceArn)
		return
	}
	group := cwImportDestination(eds.ARN)
	if err := iamValidateServiceRole(task.ImportRoleArn, "cloudtrail.amazonaws.com", map[string]string{
		"cloudtrail:GetEventDataStoreData": eds.ARN,
		"logs:CreateLogGroup":              task.ImportDestinationArn + ":*",
		"logs:CreateLogStream":             task.ImportDestinationArn + ":*",
	}); err != nil {
		cwImportFail(task.ImportId, err.Error())
		return
	}
	var filter cwImportFilter
	if len(task.ImportFilter) > 0 {
		_ = json.Unmarshal(task.ImportFilter, &filter)
	}
	byDay := map[string][]CloudTrailEvent{}
	for _, stored := range cloudTrailLakeEvents.Filter(func(e CloudTrailLakeEvent) bool { return e.EventDataStore == eds.ARN }) {
		at, err := time.Parse(time.RFC3339, stored.Event.EventTime)
		if err != nil {
			continue
		}
		ms := at.UnixMilli()
		if filter.StartEventTime != nil && ms < *filter.StartEventTime || filter.EndEventTime != nil && ms > *filter.EndEventTime {
			continue
		}
		day := at.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], stored.Event)
	}
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Strings(days)
	stream := task.ImportId
	cwEnsureLogStream(group, stream)
	for _, day := range days {
		current, ok := cwImportTasks.Get(task.ImportId)
		if !ok || current.ImportStatus != "IN_PROGRESS" {
			return
		}
		batch := CWImportBatch{BatchId: uuid.New().String(), Status: "COMPLETED", Day: day}
		events := byDay[day]
		sort.Slice(events, func(i, j int) bool { return events[i].EventTime < events[j].EventTime })
		now := time.Now().UnixMilli()
		var logEvents []CWLogEvent
		var bytes int64
		for _, e := range events {
			at, _ := time.Parse(time.RFC3339, e.EventTime)
			message := cloudTrailEventRecordJSON(e)
			bytes += int64(len(message))
			logEvents = append(logEvents, CWLogEvent{Timestamp: at.UnixMilli(), Message: message, IngestionTime: now})
		}
		cwAppendLogEvents(cwEventsKey(group, stream), logEvents, nil)
		cwImportTasks.Update(task.ImportId, func(t *CWImportTask) {
			t.Batches = append(t.Batches, batch)
			t.BytesImported += bytes
			t.LastUpdatedTime = now
		})
	}
	cwImportTasks.Update(task.ImportId, func(t *CWImportTask) {
		if t.ImportStatus == "IN_PROGRESS" {
			t.ImportStatus, t.LastUpdatedTime = "COMPLETED", time.Now().UnixMilli()
		}
	})
}

func handleCWDescribeImportTaskBatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportId          string   `json:"importId"`
		BatchImportStatus []string `json:"batchImportStatus"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	task, ok := cwImportTasks.Get(req.ImportId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified import task does not exist: %s", req.ImportId)
		return
	}
	batches := []map[string]any{}
	for _, b := range task.Batches {
		if len(req.BatchImportStatus) > 0 && !cwStrIn(b.Status, req.BatchImportStatus) {
			continue
		}
		batch := map[string]any{"batchId": b.BatchId, "status": b.Status}
		if b.ErrorMessage != "" {
			batch["errorMessage"] = b.ErrorMessage
		}
		batches = append(batches, batch)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"importId":        task.ImportId,
		"importSourceArn": task.ImportSourceArn,
		"importBatches":   batches,
	})
}
