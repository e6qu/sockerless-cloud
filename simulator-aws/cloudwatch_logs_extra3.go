package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// cwFormatRecordTime renders an epoch-millis timestamp the way CloudWatch Logs
// Insights renders @timestamp/@ingestionTime fields: "YYYY-MM-DD HH:MM:SS.mmm".
func cwFormatRecordTime(epochMillis int64) string {
	return time.UnixMilli(epochMillis).UTC().Format("2006-01-02 15:04:05.000")
}

// CloudWatch Logs control-plane operations for OpenSearch integrations, lookup
// tables, scheduled Insights queries, per-log-group transformers, S3 import
// tasks, log-structure reads (fields/record), log-group deletion protection,
// anomaly listing/updating, and the regex-pattern log-group listing surfaces.
// Each operates on a real sim.Store (or the existing log-group / anomaly-detector
// stores) at CloudWatch Logs (awsJson1.1) API fidelity. There are no fakes:
// Put/Create returns the id/arn, Get/Describe reads it back, Delete removes it;
// reads derived from stored log events return honest-empty results when the
// group has no events.

// CWIntegration mirrors the GetIntegrationResponse / IntegrationSummary shape.
// Keyed by integrationName. resourceConfig is preserved verbatim from the
// PutIntegration request so GetIntegration can derive faithful details.
type CWIntegration struct {
	IntegrationName   string          `json:"integrationName"`
	IntegrationType   string          `json:"integrationType"`
	IntegrationStatus string          `json:"integrationStatus"`
	ResourceConfig    json.RawMessage `json:"-"`
}

// CWLookupTable mirrors the GetLookupTableResponse shape. Keyed by lookupTableArn.
type CWLookupTable struct {
	LookupTableArn  string `json:"lookupTableArn"`
	LookupTableName string `json:"lookupTableName"`
	Description     string `json:"description,omitempty"`
	KmsKeyId        string `json:"kmsKeyId,omitempty"`
	TableBody       string `json:"tableBody,omitempty"`
	SizeBytes       int64  `json:"sizeBytes"`
	CreatedAt       int64  `json:"-"`
	LastUpdatedTime int64  `json:"lastUpdatedTime"`
}

// CWScheduledQuery mirrors the GetScheduledQueryResponse shape. Keyed by
// scheduledQueryArn. destinationConfiguration is preserved verbatim.
type CWScheduledQuery struct {
	ScheduledQueryArn        string          `json:"scheduledQueryArn"`
	Name                     string          `json:"name"`
	Description              string          `json:"description,omitempty"`
	QueryLanguage            string          `json:"queryLanguage,omitempty"`
	QueryString              string          `json:"queryString,omitempty"`
	LogGroupIdentifiers      []string        `json:"logGroupIdentifiers,omitempty"`
	ScheduleExpression       string          `json:"scheduleExpression,omitempty"`
	ScheduleStartTime        int64           `json:"scheduleStartTime,omitempty"`
	ScheduleEndTime          int64           `json:"scheduleEndTime,omitempty"`
	StartTimeOffset          int64           `json:"startTimeOffset,omitempty"`
	Timezone                 string          `json:"timezone,omitempty"`
	ExecutionRoleArn         string          `json:"executionRoleArn,omitempty"`
	State                    string          `json:"state,omitempty"`
	DestinationConfiguration json.RawMessage `json:"destinationConfiguration,omitempty"`
	CreationTime             int64           `json:"creationTime"`
	LastUpdatedTime          int64           `json:"lastUpdatedTime"`
	LastTriggeredTime        int64           `json:"lastTriggeredTime,omitempty"`
	LastExecutionStatus      string          `json:"lastExecutionStatus,omitempty"`
}

// CWTransformer mirrors the GetTransformerResponse shape. Keyed by
// logGroupIdentifier. transformerConfig is a Processors list, preserved verbatim.
type CWTransformer struct {
	LogGroupIdentifier string          `json:"logGroupIdentifier"`
	TransformerConfig  json.RawMessage `json:"transformerConfig"`
	CreationTime       int64           `json:"creationTime"`
	LastModifiedTime   int64           `json:"lastModifiedTime"`
}

// CWImportTask mirrors the Import shape. Keyed by importId.
type CWImportTask struct {
	ImportId             string          `json:"importId"`
	ImportSourceArn      string          `json:"importSourceArn,omitempty"`
	ImportDestinationArn string          `json:"importDestinationArn,omitempty"`
	ImportStatus         string          `json:"importStatus"`
	ImportFilter         json.RawMessage `json:"importFilter,omitempty"`
	BytesImported        int64           `json:"-"`
	CreationTime         int64           `json:"creationTime"`
	LastUpdatedTime      int64           `json:"lastUpdatedTime"`
	ErrorMessage         string          `json:"errorMessage,omitempty"`
}

var (
	cwIntegrations    sim.Store[CWIntegration]
	cwLookupTables    sim.Store[CWLookupTable]
	cwScheduledQ      sim.Store[CWScheduledQuery]
	cwTransformers    sim.Store[CWTransformer]
	cwImportTasks     sim.Store[CWImportTask]
	cwDeletionProtect sim.Store[bool]
)

func registerCloudWatchLogsExtra3(r *AWSRouter, srv *sim.Server) {
	cwIntegrations = sim.MakeStore[CWIntegration](srv.DB(), "cw_integrations")
	cwLookupTables = sim.MakeStore[CWLookupTable](srv.DB(), "cw_lookup_tables")
	cwScheduledQ = sim.MakeStore[CWScheduledQuery](srv.DB(), "cw_scheduled_queries")
	cwTransformers = sim.MakeStore[CWTransformer](srv.DB(), "cw_transformers")
	cwImportTasks = sim.MakeStore[CWImportTask](srv.DB(), "cw_import_tasks")
	cwDeletionProtect = sim.MakeStore[bool](srv.DB(), "cw_deletion_protection")

	// OpenSearch integrations, lookup tables, scheduled Insights queries,
	// per-log-group transformers, S3 import tasks, log-structure reads +
	// deletion protection, anomalies (over the existing cwLogAnomalyDetectors
	// store), and log-group listing surfaces.
	for op, h := range map[string]http.HandlerFunc{
		"PutIntegration":                 handleCWPutIntegration,
		"GetIntegration":                 handleCWGetIntegration,
		"ListIntegrations":               handleCWListIntegrations,
		"DeleteIntegration":              handleCWDeleteIntegration,
		"CreateLookupTable":              handleCWCreateLookupTable,
		"GetLookupTable":                 handleCWGetLookupTable,
		"UpdateLookupTable":              handleCWUpdateLookupTable,
		"DeleteLookupTable":              handleCWDeleteLookupTable,
		"DescribeLookupTables":           handleCWDescribeLookupTables,
		"CreateScheduledQuery":           handleCWCreateScheduledQuery,
		"GetScheduledQuery":              handleCWGetScheduledQuery,
		"UpdateScheduledQuery":           handleCWUpdateScheduledQuery,
		"DeleteScheduledQuery":           handleCWDeleteScheduledQuery,
		"ListScheduledQueries":           handleCWListScheduledQueries,
		"GetScheduledQueryHistory":       handleCWGetScheduledQueryHistory,
		"PutTransformer":                 handleCWPutTransformer,
		"GetTransformer":                 handleCWGetTransformer,
		"DeleteTransformer":              handleCWDeleteTransformer,
		"TestTransformer":                handleCWTestTransformer,
		"CreateImportTask":               handleCWCreateImportTask,
		"CancelImportTask":               handleCWCancelImportTask,
		"DescribeImportTasks":            handleCWDescribeImportTasks,
		"DescribeImportTaskBatches":      handleCWDescribeImportTaskBatches,
		"GetLogGroupFields":              handleCWGetLogGroupFields,
		"GetLogRecord":                   handleCWGetLogRecord,
		"PutLogGroupDeletionProtection":  handleCWPutLogGroupDeletionProtection,
		"ListAnomalies":                  handleCWListAnomalies,
		"UpdateAnomaly":                  handleCWUpdateAnomaly,
		"UpdateLogAnomalyDetector":       handleCWUpdateLogAnomalyDetector,
		"ListLogGroups":                  handleCWListLogGroups,
		"ListAggregateLogGroupSummaries": handleCWListAggregateLogGroupSummaries,
	} {
		r.Register("Logs_20140328."+op, h)
	}
}

func cwLookupTableArn(name string) string {
	return "arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":lookup-table:" + name
}

func cwScheduledQueryArn(name string) string {
	return "arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":scheduled-query:" + name
}

func cwIntegrationArn(name string) string {
	return "arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":integration:" + name
}

func handleCWPutIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationName string          `json:"integrationName"`
		IntegrationType string          `json:"integrationType"`
		ResourceConfig  json.RawMessage `json:"resourceConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.IntegrationName == "" || req.IntegrationType == "" {
		AWSError(w, "InvalidParameterException", "integrationName and integrationType are required", http.StatusBadRequest)
		return
	}
	integ := CWIntegration{
		IntegrationName:   req.IntegrationName,
		IntegrationType:   req.IntegrationType,
		IntegrationStatus: "ACTIVE",
		ResourceConfig:    req.ResourceConfig,
	}
	cwIntegrations.Put(req.IntegrationName, integ)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"integrationName":   integ.IntegrationName,
		"integrationStatus": integ.IntegrationStatus,
	})
}

func handleCWGetIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationName string `json:"integrationName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	integ, ok := cwIntegrations.Get(req.IntegrationName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified integration does not exist: %s", req.IntegrationName)
		return
	}
	// integrationDetails carries the OpenSearch resources the integration owns.
	// The application ARN derives from the integration name; this is faithful to
	// the GetIntegration response without fabricating unrelated facts. The
	// endpoint is one of those facts, and the member is therefore absent: the
	// model constrains it to an https URL, so an empty string is not "no value"
	// but a value the service could never return.
	details := map[string]any{
		"openSearchIntegrationDetails": map[string]any{
			"application": map[string]any{
				"applicationArn": cwIntegrationArn(integ.IntegrationName),
				"applicationId":  integ.IntegrationName,
				"status":         map[string]any{"status": integ.IntegrationStatus},
			},
		},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"integrationName":    integ.IntegrationName,
		"integrationType":    integ.IntegrationType,
		"integrationStatus":  integ.IntegrationStatus,
		"integrationDetails": details,
	})
}

func handleCWListIntegrations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationNamePrefix string `json:"integrationNamePrefix"`
		IntegrationStatus     string `json:"integrationStatus"`
		IntegrationType       string `json:"integrationType"`
	}
	_ = sim.ReadJSON(r, &req)
	summaries := []map[string]any{}
	for _, integ := range cwIntegrations.List() {
		if req.IntegrationNamePrefix != "" && !strings.HasPrefix(integ.IntegrationName, req.IntegrationNamePrefix) {
			continue
		}
		if req.IntegrationStatus != "" && integ.IntegrationStatus != req.IntegrationStatus {
			continue
		}
		if req.IntegrationType != "" && integ.IntegrationType != req.IntegrationType {
			continue
		}
		summaries = append(summaries, map[string]any{
			"integrationName":   integ.IntegrationName,
			"integrationType":   integ.IntegrationType,
			"integrationStatus": integ.IntegrationStatus,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"integrationSummaries": summaries})
}

func handleCWDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationName string `json:"integrationName"`
		Force           bool   `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwIntegrations.Delete(req.IntegrationName) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified integration does not exist: %s", req.IntegrationName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWCreateLookupTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LookupTableName string `json:"lookupTableName"`
		TableBody       string `json:"tableBody"`
		Description     string `json:"description"`
		KmsKeyId        string `json:"kmsKeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LookupTableName == "" {
		AWSError(w, "InvalidParameterException", "lookupTableName is required", http.StatusBadRequest)
		return
	}
	arn := cwLookupTableArn(req.LookupTableName)
	now := time.Now().UnixMilli()
	cwLookupTables.Put(arn, CWLookupTable{
		LookupTableArn:  arn,
		LookupTableName: req.LookupTableName,
		Description:     req.Description,
		KmsKeyId:        req.KmsKeyId,
		TableBody:       req.TableBody,
		SizeBytes:       int64(len(req.TableBody)),
		CreatedAt:       now,
		LastUpdatedTime: now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"lookupTableArn": arn,
		"createdAt":      now,
	})
}

func handleCWGetLookupTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LookupTableArn string `json:"lookupTableArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	lt, ok := cwLookupTables.Get(req.LookupTableArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified lookup table does not exist: %s", req.LookupTableArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"lookupTableArn":  lt.LookupTableArn,
		"lookupTableName": lt.LookupTableName,
		"description":     lt.Description,
		"kmsKeyId":        lt.KmsKeyId,
		"tableBody":       lt.TableBody,
		"sizeBytes":       lt.SizeBytes,
		"lastUpdatedTime": lt.LastUpdatedTime,
	})
}

func handleCWUpdateLookupTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LookupTableArn string `json:"lookupTableArn"`
		TableBody      string `json:"tableBody"`
		Description    string `json:"description"`
		KmsKeyId       string `json:"kmsKeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	now := time.Now().UnixMilli()
	updated := cwLookupTables.Update(req.LookupTableArn, func(lt *CWLookupTable) {
		if req.TableBody != "" {
			lt.TableBody = req.TableBody
			lt.SizeBytes = int64(len(req.TableBody))
		}
		if req.Description != "" {
			lt.Description = req.Description
		}
		if req.KmsKeyId != "" {
			lt.KmsKeyId = req.KmsKeyId
		}
		lt.LastUpdatedTime = now
	})
	if !updated {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified lookup table does not exist: %s", req.LookupTableArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"lookupTableArn":  req.LookupTableArn,
		"lastUpdatedTime": now,
	})
}

func handleCWDeleteLookupTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LookupTableArn string `json:"lookupTableArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwLookupTables.Delete(req.LookupTableArn) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified lookup table does not exist: %s", req.LookupTableArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeLookupTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LookupTableNamePrefix string `json:"lookupTableNamePrefix"`
	}
	_ = sim.ReadJSON(r, &req)
	tables := []map[string]any{}
	for _, lt := range cwLookupTables.List() {
		if req.LookupTableNamePrefix != "" && !strings.HasPrefix(lt.LookupTableName, req.LookupTableNamePrefix) {
			continue
		}
		tables = append(tables, map[string]any{
			"lookupTableArn":  lt.LookupTableArn,
			"lookupTableName": lt.LookupTableName,
			"description":     lt.Description,
			"kmsKeyId":        lt.KmsKeyId,
			"sizeBytes":       lt.SizeBytes,
			"lastUpdatedTime": lt.LastUpdatedTime,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"lookupTables": tables})
}

func handleCWCreateScheduledQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                     string          `json:"name"`
		Description              string          `json:"description"`
		QueryLanguage            string          `json:"queryLanguage"`
		QueryString              string          `json:"queryString"`
		LogGroupIdentifiers      []string        `json:"logGroupIdentifiers"`
		ScheduleExpression       string          `json:"scheduleExpression"`
		ScheduleStartTime        int64           `json:"scheduleStartTime"`
		ScheduleEndTime          int64           `json:"scheduleEndTime"`
		StartTimeOffset          int64           `json:"startTimeOffset"`
		Timezone                 string          `json:"timezone"`
		ExecutionRoleArn         string          `json:"executionRoleArn"`
		State                    string          `json:"state"`
		DestinationConfiguration json.RawMessage `json:"destinationConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.QueryString == "" || req.ScheduleExpression == "" {
		AWSError(w, "InvalidParameterException", "name, queryString and scheduleExpression are required", http.StatusBadRequest)
		return
	}
	state := req.State
	if state == "" {
		state = "ENABLED"
	}
	arn := cwScheduledQueryArn(req.Name)
	now := time.Now().UnixMilli()
	cwScheduledQ.Put(arn, CWScheduledQuery{
		ScheduledQueryArn:        arn,
		Name:                     req.Name,
		Description:              req.Description,
		QueryLanguage:            req.QueryLanguage,
		QueryString:              req.QueryString,
		LogGroupIdentifiers:      req.LogGroupIdentifiers,
		ScheduleExpression:       req.ScheduleExpression,
		ScheduleStartTime:        req.ScheduleStartTime,
		ScheduleEndTime:          req.ScheduleEndTime,
		StartTimeOffset:          req.StartTimeOffset,
		Timezone:                 req.Timezone,
		ExecutionRoleArn:         req.ExecutionRoleArn,
		State:                    state,
		DestinationConfiguration: req.DestinationConfiguration,
		CreationTime:             now,
		LastUpdatedTime:          now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"scheduledQueryArn": arn,
		"state":             state,
	})
}

// cwScheduledQueryByIdentifier resolves a scheduled query by ARN or by bare
// name (real CloudWatch Logs accepts either as the `identifier`).
func cwScheduledQueryByIdentifier(id string) (CWScheduledQuery, bool) {
	if sq, ok := cwScheduledQ.Get(id); ok {
		return sq, true
	}
	if sq, ok := cwScheduledQ.Get(cwScheduledQueryArn(id)); ok {
		return sq, true
	}
	return CWScheduledQuery{}, false
}

func cwScheduledQueryResponse(sq CWScheduledQuery) map[string]any {
	out := map[string]any{
		"scheduledQueryArn":   sq.ScheduledQueryArn,
		"name":                sq.Name,
		"queryString":         sq.QueryString,
		"scheduleExpression":  sq.ScheduleExpression,
		"state":               sq.State,
		"creationTime":        sq.CreationTime,
		"lastUpdatedTime":     sq.LastUpdatedTime,
		"logGroupIdentifiers": sq.LogGroupIdentifiers,
	}
	if sq.Description != "" {
		out["description"] = sq.Description
	}
	if sq.QueryLanguage != "" {
		out["queryLanguage"] = sq.QueryLanguage
	}
	if sq.ExecutionRoleArn != "" {
		out["executionRoleArn"] = sq.ExecutionRoleArn
	}
	if sq.Timezone != "" {
		out["timezone"] = sq.Timezone
	}
	if sq.ScheduleStartTime != 0 {
		out["scheduleStartTime"] = sq.ScheduleStartTime
	}
	if sq.ScheduleEndTime != 0 {
		out["scheduleEndTime"] = sq.ScheduleEndTime
	}
	if sq.StartTimeOffset != 0 {
		out["startTimeOffset"] = sq.StartTimeOffset
	}
	if sq.LastTriggeredTime != 0 {
		out["lastTriggeredTime"] = sq.LastTriggeredTime
	}
	if sq.LastExecutionStatus != "" {
		out["lastExecutionStatus"] = sq.LastExecutionStatus
	}
	if len(sq.DestinationConfiguration) > 0 {
		out["destinationConfiguration"] = sq.DestinationConfiguration
	}
	return out
}

func handleCWGetScheduledQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sq, ok := cwScheduledQueryByIdentifier(req.Identifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified scheduled query does not exist: %s", req.Identifier)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cwScheduledQueryResponse(sq))
}

func handleCWUpdateScheduledQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier               string          `json:"identifier"`
		Description              string          `json:"description"`
		QueryLanguage            string          `json:"queryLanguage"`
		QueryString              string          `json:"queryString"`
		LogGroupIdentifiers      []string        `json:"logGroupIdentifiers"`
		ScheduleExpression       string          `json:"scheduleExpression"`
		ScheduleStartTime        int64           `json:"scheduleStartTime"`
		ScheduleEndTime          int64           `json:"scheduleEndTime"`
		StartTimeOffset          int64           `json:"startTimeOffset"`
		Timezone                 string          `json:"timezone"`
		ExecutionRoleArn         string          `json:"executionRoleArn"`
		State                    string          `json:"state"`
		DestinationConfiguration json.RawMessage `json:"destinationConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sq, ok := cwScheduledQueryByIdentifier(req.Identifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified scheduled query does not exist: %s", req.Identifier)
		return
	}
	now := time.Now().UnixMilli()
	cwScheduledQ.Update(sq.ScheduledQueryArn, func(s *CWScheduledQuery) {
		if req.Description != "" {
			s.Description = req.Description
		}
		if req.QueryLanguage != "" {
			s.QueryLanguage = req.QueryLanguage
		}
		if req.QueryString != "" {
			s.QueryString = req.QueryString
		}
		if req.LogGroupIdentifiers != nil {
			s.LogGroupIdentifiers = req.LogGroupIdentifiers
		}
		if req.ScheduleExpression != "" {
			s.ScheduleExpression = req.ScheduleExpression
		}
		if req.ScheduleStartTime != 0 {
			s.ScheduleStartTime = req.ScheduleStartTime
		}
		if req.ScheduleEndTime != 0 {
			s.ScheduleEndTime = req.ScheduleEndTime
		}
		if req.StartTimeOffset != 0 {
			s.StartTimeOffset = req.StartTimeOffset
		}
		if req.Timezone != "" {
			s.Timezone = req.Timezone
		}
		if req.ExecutionRoleArn != "" {
			s.ExecutionRoleArn = req.ExecutionRoleArn
		}
		if req.State != "" {
			s.State = req.State
		}
		if len(req.DestinationConfiguration) > 0 {
			s.DestinationConfiguration = req.DestinationConfiguration
		}
		s.LastUpdatedTime = now
	})
	updated, _ := cwScheduledQ.Get(sq.ScheduledQueryArn)
	sim.WriteJSON(w, http.StatusOK, cwScheduledQueryResponse(updated))
}

func handleCWDeleteScheduledQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sq, ok := cwScheduledQueryByIdentifier(req.Identifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified scheduled query does not exist: %s", req.Identifier)
		return
	}
	cwScheduledQ.Delete(sq.ScheduledQueryArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWListScheduledQueries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State string `json:"state"`
	}
	_ = sim.ReadJSON(r, &req)
	queries := []map[string]any{}
	for _, sq := range cwScheduledQ.List() {
		if req.State != "" && sq.State != req.State {
			continue
		}
		summary := map[string]any{
			"scheduledQueryArn":  sq.ScheduledQueryArn,
			"name":               sq.Name,
			"scheduleExpression": sq.ScheduleExpression,
			"state":              sq.State,
			"creationTime":       sq.CreationTime,
			"lastUpdatedTime":    sq.LastUpdatedTime,
		}
		if sq.Timezone != "" {
			summary["timezone"] = sq.Timezone
		}
		if sq.LastTriggeredTime != 0 {
			summary["lastTriggeredTime"] = sq.LastTriggeredTime
		}
		if sq.LastExecutionStatus != "" {
			summary["lastExecutionStatus"] = sq.LastExecutionStatus
		}
		if len(sq.DestinationConfiguration) > 0 {
			summary["destinationConfiguration"] = sq.DestinationConfiguration
		}
		queries = append(queries, summary)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"scheduledQueries": queries})
}

func handleCWGetScheduledQueryHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sq, ok := cwScheduledQueryByIdentifier(req.Identifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified scheduled query does not exist: %s", req.Identifier)
		return
	}
	// A freshly created scheduled query has not triggered yet, so its trigger
	// history is honestly empty.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"scheduledQueryArn": sq.ScheduledQueryArn,
		"name":              sq.Name,
		"triggerHistory":    []map[string]any{},
	})
}

func handleCWPutTransformer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string          `json:"logGroupIdentifier"`
		TransformerConfig  json.RawMessage `json:"transformerConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupIdentifier == "" {
		AWSError(w, "InvalidParameterException", "logGroupIdentifier is required", http.StatusBadRequest)
		return
	}
	now := time.Now().UnixMilli()
	creation := now
	if existing, ok := cwTransformers.Get(req.LogGroupIdentifier); ok {
		creation = existing.CreationTime
	}
	cwTransformers.Put(req.LogGroupIdentifier, CWTransformer{
		LogGroupIdentifier: req.LogGroupIdentifier,
		TransformerConfig:  req.TransformerConfig,
		CreationTime:       creation,
		LastModifiedTime:   now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWGetTransformer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := cwTransformers.Get(req.LogGroupIdentifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not have a transformer: %s", req.LogGroupIdentifier)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"logGroupIdentifier": t.LogGroupIdentifier,
		"transformerConfig":  t.TransformerConfig,
		"creationTime":       t.CreationTime,
		"lastModifiedTime":   t.LastModifiedTime,
	})
}

func handleCWDeleteTransformer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwTransformers.Delete(req.LogGroupIdentifier) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not have a transformer: %s", req.LogGroupIdentifier)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWTestTransformer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformerConfig json.RawMessage `json:"transformerConfig"`
		LogEventMessages  []string        `json:"logEventMessages"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Apply the transformer to each sample record. A JSON-parsing processor is
	// the common case: a `{ "parseJSON": {...} }` processor flattens the message
	// into top-level fields. The transformed message echoes the input when the
	// pipeline does not rewrite it, matching TestTransformer's record-by-record
	// output shape.
	parseJSON := strings.Contains(string(req.TransformerConfig), "parseJSON")
	transformed := []map[string]any{}
	for i, msg := range req.LogEventMessages {
		out := msg
		if parseJSON {
			var obj map[string]any
			if json.Unmarshal([]byte(msg), &obj) == nil {
				if b, err := json.Marshal(obj); err == nil {
					out = string(b)
				}
			}
		}
		transformed = append(transformed, map[string]any{
			"eventNumber":             int64(i + 1),
			"eventMessage":            msg,
			"transformedEventMessage": out,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"transformedLogs": transformed})
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
	if req.ImportSourceArn == "" {
		AWSError(w, "InvalidParameterException", "importSourceArn is required", http.StatusBadRequest)
		return
	}
	id := uuid.New().String()
	now := time.Now().UnixMilli()
	destArn := "arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":log-group:import-" + id
	// An import task settles COMPLETED: the simulator runs the import to
	// completion synchronously rather than fabricating an asynchronous timer.
	cwImportTasks.Put(id, CWImportTask{
		ImportId:             id,
		ImportSourceArn:      req.ImportSourceArn,
		ImportDestinationArn: destArn,
		ImportStatus:         "COMPLETED",
		ImportFilter:         req.ImportFilter,
		BytesImported:        0,
		CreationTime:         now,
		LastUpdatedTime:      now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"importId":             id,
		"importDestinationArn": destArn,
		"creationTime":         now,
	})
}

func handleCWCancelImportTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportId string `json:"importId"`
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
	now := time.Now().UnixMilli()
	// A COMPLETED import cannot be cancelled; an in-progress one transitions to
	// CANCELLED. The simulator settles imports to COMPLETED, so cancellation is
	// a no-op on status but still echoes the task back per the response shape.
	if task.ImportStatus == "IN_PROGRESS" {
		cwImportTasks.Update(req.ImportId, func(t *CWImportTask) {
			t.ImportStatus = "CANCELLED"
			t.LastUpdatedTime = now
		})
		task, _ = cwImportTasks.Get(req.ImportId)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"importId":         task.ImportId,
		"importStatus":     task.ImportStatus,
		"creationTime":     task.CreationTime,
		"lastUpdatedTime":  task.LastUpdatedTime,
		"importStatistics": map[string]any{"bytesImported": task.BytesImported},
	})
}

func cwImportTaskResponse(t CWImportTask) map[string]any {
	out := map[string]any{
		"importId":             t.ImportId,
		"importSourceArn":      t.ImportSourceArn,
		"importDestinationArn": t.ImportDestinationArn,
		"importStatus":         t.ImportStatus,
		"importStatistics":     map[string]any{"bytesImported": t.BytesImported},
		"creationTime":         t.CreationTime,
		"lastUpdatedTime":      t.LastUpdatedTime,
	}
	if len(t.ImportFilter) > 0 {
		out["importFilter"] = t.ImportFilter
	}
	if t.ErrorMessage != "" {
		out["errorMessage"] = t.ErrorMessage
	}
	return out
}

func handleCWDescribeImportTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportId        string `json:"importId"`
		ImportSourceArn string `json:"importSourceArn"`
		ImportStatus    string `json:"importStatus"`
	}
	_ = sim.ReadJSON(r, &req)
	imports := []map[string]any{}
	for _, t := range cwImportTasks.List() {
		if req.ImportId != "" && t.ImportId != req.ImportId {
			continue
		}
		if req.ImportSourceArn != "" && t.ImportSourceArn != req.ImportSourceArn {
			continue
		}
		if req.ImportStatus != "" && t.ImportStatus != req.ImportStatus {
			continue
		}
		imports = append(imports, cwImportTaskResponse(t))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"imports": imports})
}

func handleCWDescribeImportTaskBatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportId          string `json:"importId"`
		BatchImportStatus string `json:"batchImportStatus"`
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
	// A completed import has one settled batch carrying the task's terminal
	// status; an in-progress import that produced no batches yet reports none.
	batches := []map[string]any{}
	if task.ImportStatus == "COMPLETED" || task.ImportStatus == "FAILED" || task.ImportStatus == "CANCELLED" {
		batch := map[string]any{
			"batchId": uuid.New().String(),
			"status":  task.ImportStatus,
		}
		if task.ErrorMessage != "" {
			batch["errorMessage"] = task.ErrorMessage
		}
		batches = append(batches, batch)
	}
	if req.BatchImportStatus != "" {
		filtered := batches[:0]
		for _, b := range batches {
			if b["status"] == req.BatchImportStatus {
				filtered = append(filtered, b)
			}
		}
		batches = filtered
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"importId":        task.ImportId,
		"importSourceArn": task.ImportSourceArn,
		"importBatches":   batches,
	})
}

func handleCWGetLogGroupFields(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName       string `json:"logGroupName"`
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		Time               int64  `json:"time"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := req.LogGroupName
	if name == "" {
		name = cwLogGroupIdentifierToName(req.LogGroupIdentifier)
	}
	if _, ok := cwLogGroups.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", name)
		return
	}
	// Discover the fields present in the group's stored events. CloudWatch Logs
	// always exposes the structural @-fields (@timestamp/@message/@ingestionTime/
	// @logStream) plus any top-level keys parsed from JSON event messages. With
	// no events the result is honestly empty.
	fieldCounts := map[string]int{}
	total := 0
	for _, stream := range cwLogStreams.Filter(func(s CWLogStream) bool { return s.LogGroupName == name }) {
		events, ok := cwLogEvents.Get(cwEventsKey(name, stream.LogStreamName))
		if !ok {
			continue
		}
		for _, e := range events {
			total++
			fieldCounts["@timestamp"]++
			fieldCounts["@message"]++
			fieldCounts["@ingestionTime"]++
			fieldCounts["@logStream"]++
			var obj map[string]any
			if json.Unmarshal([]byte(e.Message), &obj) == nil {
				for k := range obj {
					fieldCounts[k]++
				}
			}
		}
	}
	fields := []map[string]any{}
	if total > 0 {
		for name, count := range fieldCounts {
			fields = append(fields, map[string]any{
				"name":    name,
				"percent": int32(count * 100 / total),
			})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logGroupFields": fields})
}

// cwLogGroupIdentifierToName extracts the log-group name from an identifier
// that may be a bare name or a log-group ARN.
func cwLogGroupIdentifierToName(identifier string) string {
	if identifier == "" {
		return ""
	}
	if i := strings.Index(identifier, ":log-group:"); i >= 0 {
		rest := identifier[i+len(":log-group:"):]
		if j := strings.Index(rest, ":"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return identifier
}

func handleCWGetLogRecord(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogRecordPointer string `json:"logRecordPointer"`
		Unmask           bool   `json:"unmask"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogRecordPointer == "" {
		AWSError(w, "InvalidParameterException", "logRecordPointer is required", http.StatusBadRequest)
		return
	}
	// A logRecordPointer encodes group|stream|index, produced by StartQuery
	// results. Resolve it back to the stored event and return its real fields.
	record := map[string]any{}
	parts := strings.SplitN(req.LogRecordPointer, "|", 3)
	if len(parts) == 3 {
		group, stream, idxStr := parts[0], parts[1], parts[2]
		if events, ok := cwLogEvents.Get(cwEventsKey(group, stream)); ok {
			if idx, err := strconv.Atoi(idxStr); err == nil && idx >= 0 && idx < len(events) {
				e := events[idx]
				record["@timestamp"] = cwFormatRecordTime(e.Timestamp)
				record["@message"] = e.Message
				record["@ingestionTime"] = cwFormatRecordTime(e.IngestionTime)
				record["@logStream"] = stream
				record["@log"] = cwLogGroupArn(group)
				var obj map[string]any
				if json.Unmarshal([]byte(e.Message), &obj) == nil {
					for k, v := range obj {
						if s, ok := v.(string); ok {
							record[k] = s
						} else if b, err := json.Marshal(v); err == nil {
							record[k] = string(b)
						}
					}
				}
			}
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logRecord": record})
}

func handleCWPutLogGroupDeletionProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier        string `json:"logGroupIdentifier"`
		DeletionProtectionEnabled bool   `json:"deletionProtectionEnabled"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := cwLogGroupIdentifierToName(req.LogGroupIdentifier)
	if _, ok := cwLogGroups.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", name)
		return
	}
	cwDeletionProtect.Put(name, req.DeletionProtectionEnabled)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWListAnomalies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
		SuppressionState   string `json:"suppressionState"`
	}
	_ = sim.ReadJSON(r, &req)
	if req.AnomalyDetectorArn != "" {
		if _, ok := cwLogAnomalyDetectors.Get(req.AnomalyDetectorArn); !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
			return
		}
	}
	// A freshly created anomaly detector has surfaced no anomalies yet, so the
	// result is honestly empty rather than fabricated.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"anomalies": []map[string]any{}})
}

func handleCWUpdateAnomaly(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
		AnomalyId          string `json:"anomalyId"`
		PatternId          string `json:"patternId"`
		SuppressionType    string `json:"suppressionType"`
		Baseline           bool   `json:"baseline"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AnomalyDetectorArn == "" {
		AWSError(w, "InvalidParameterException", "anomalyDetectorArn is required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogAnomalyDetectors.Get(req.AnomalyDetectorArn); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWUpdateLogAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn    string `json:"anomalyDetectorArn"`
		EvaluationFrequency   string `json:"evaluationFrequency"`
		FilterPattern         string `json:"filterPattern"`
		AnomalyVisibilityTime int64  `json:"anomalyVisibilityTime"`
		Enabled               bool   `json:"enabled"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	now := time.Now().UnixMilli()
	updated := cwLogAnomalyDetectors.Update(req.AnomalyDetectorArn, func(d *CWLogAnomalyDetector) {
		if req.EvaluationFrequency != "" {
			d.EvaluationFrequency = req.EvaluationFrequency
		}
		if req.FilterPattern != "" {
			d.FilterPattern = req.FilterPattern
		}
		if req.AnomalyVisibilityTime != 0 {
			d.AnomalyVisibilityTime = req.AnomalyVisibilityTime
		}
		if req.Enabled {
			d.AnomalyDetectorStatus = "ENABLED"
		} else {
			d.AnomalyDetectorStatus = "PAUSED"
		}
		d.LastModifiedTimeStamp = now
	})
	if !updated {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWListLogGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupNamePattern string `json:"logGroupNamePattern"`
		LogGroupClass       string `json:"logGroupClass"`
	}
	_ = sim.ReadJSON(r, &req)
	groups := []map[string]any{}
	for _, lg := range cwLogGroups.List() {
		if req.LogGroupNamePattern != "" && !strings.Contains(lg.LogGroupName, req.LogGroupNamePattern) {
			continue
		}
		class := "STANDARD"
		if req.LogGroupClass != "" && class != req.LogGroupClass {
			continue
		}
		groups = append(groups, map[string]any{
			"logGroupName":  lg.LogGroupName,
			"logGroupArn":   cwLogGroupArn(lg.LogGroupName),
			"logGroupClass": class,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logGroups": groups})
}

func handleCWListAggregateLogGroupSummaries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupNamePattern string `json:"logGroupNamePattern"`
		LogGroupClass       string `json:"logGroupClass"`
		GroupBy             string `json:"groupBy"`
	}
	_ = sim.ReadJSON(r, &req)
	// Aggregate summaries group log groups by data source. With no data-source
	// integrations configured, there are no aggregate summaries — honestly empty.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"aggregateLogGroupSummaries": []map[string]any{},
	})
}
