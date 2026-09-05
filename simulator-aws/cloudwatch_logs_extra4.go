package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// CloudWatch Logs control-plane operations for S3 Table Integration data-source
// associations, data-source field discovery, query log-group enumeration,
// account-level bearer-token authentication, and vended-log delivery
// configuration updates. Each operates on a real sim.Store (or an existing
// CloudWatch Logs store) at CloudWatch Logs (awsJson1.1) API fidelity. There
// are no fakes: associations are stored and read back, fields are derived from
// real stored log events, the query log-group list is read from the stored
// Insights query, and delivery configuration updates mutate the existing
// delivery record. Reads with no underlying data return honestly-empty results.

// CWS3TableSource mirrors the SDK S3TableIntegrationSource shape: a stored
// association between a data source and an S3 Table Integration. Keyed by
// identifier.
type CWS3TableSource struct {
	Identifier             string `json:"identifier"`
	IntegrationArn         string `json:"-"`
	DataSourceName         string `json:"-"`
	DataSourceType         string `json:"-"`
	Status                 string `json:"status"`
	StatusReason           string `json:"statusReason,omitempty"`
	CreatedTimeStamp       int64  `json:"createdTimeStamp"`
	ParentSourceIdentifier string `json:"parentSourceIdentifier,omitempty"`
}

var (
	cwS3TableSources sim.Store[CWS3TableSource]
	// cwBearerTokenAuth records, per log-group identifier, whether bearer-token
	// authentication is enabled for that log-delivery source.
	cwBearerTokenAuth sim.Store[bool]
)

func registerCloudWatchLogsExtra4(r *AWSRouter, srv *sim.Server) {
	cwS3TableSources = sim.MakeStore[CWS3TableSource](srv.DB(), "cw_s3_table_sources")
	cwBearerTokenAuth = sim.MakeStore[bool](srv.DB(), "cw_bearer_token_auth")

	for op, h := range map[string]http.HandlerFunc{
		"AssociateSourceToS3TableIntegration":      handleCWAssociateSourceToS3TableIntegration,
		"DisassociateSourceFromS3TableIntegration": handleCWDisassociateSourceFromS3TableIntegration,
		"ListSourcesForS3TableIntegration":         handleCWListSourcesForS3TableIntegration,
		"GetLogFields":                             handleCWGetLogFields,
		"ListLogGroupsForQuery":                    handleCWListLogGroupsForQuery,
		"PutBearerTokenAuthentication":             handleCWPutBearerTokenAuthentication,
		"UpdateDeliveryConfiguration":              handleCWUpdateDeliveryConfiguration,
	} {
		r.Register("Logs_20140328."+op, h)
	}
}

func handleCWAssociateSourceToS3TableIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationArn string `json:"integrationArn"`
		DataSource     struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"dataSource"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.IntegrationArn == "" || req.DataSource.Name == "" {
		AWSError(w, "InvalidParameterException", "integrationArn and dataSource.name are required", http.StatusBadRequest)
		return
	}
	id := uuid.New().String()
	cwS3TableSources.Put(id, CWS3TableSource{
		Identifier:       id,
		IntegrationArn:   req.IntegrationArn,
		DataSourceName:   req.DataSource.Name,
		DataSourceType:   req.DataSource.Type,
		Status:           "ACTIVE",
		CreatedTimeStamp: time.Now().UnixMilli(),
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"identifier": id})
}

func handleCWDisassociateSourceFromS3TableIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwS3TableSources.Delete(req.Identifier) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified data source association does not exist: %s", req.Identifier)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"identifier": req.Identifier})
}

func handleCWListSourcesForS3TableIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationArn string `json:"integrationArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.IntegrationArn == "" {
		AWSError(w, "InvalidParameterException", "integrationArn is required", http.StatusBadRequest)
		return
	}
	sources := []map[string]any{}
	for _, s := range cwS3TableSources.List() {
		if s.IntegrationArn != req.IntegrationArn {
			continue
		}
		src := map[string]any{
			"identifier": s.Identifier,
			"dataSource": map[string]any{
				"name": s.DataSourceName,
				"type": s.DataSourceType,
			},
			"status":           s.Status,
			"createdTimeStamp": s.CreatedTimeStamp,
		}
		if s.StatusReason != "" {
			src["statusReason"] = s.StatusReason
		}
		if s.ParentSourceIdentifier != "" {
			src["parentSourceIdentifier"] = s.ParentSourceIdentifier
		}
		sources = append(sources, src)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

func handleCWGetLogFields(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataSourceName string `json:"dataSourceName"`
		DataSourceType string `json:"dataSourceType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DataSourceName == "" || req.DataSourceType == "" {
		AWSError(w, "InvalidParameterException", "dataSourceName and dataSourceType are required", http.StatusBadRequest)
		return
	}
	// A data source categorizes a log group's events. Discover the field set
	// from that log group's stored events: the structural @-fields plus any
	// top-level keys parsed from JSON event messages. With no matching log group
	// or no events, the field list is honestly empty.
	fieldTypes := map[string]string{}
	if _, ok := cwLogGroups.Get(req.DataSourceName); ok {
		fieldTypes["@timestamp"] = "timestamp"
		fieldTypes["@message"] = "string"
		fieldTypes["@ingestionTime"] = "timestamp"
		fieldTypes["@logStream"] = "string"
		for _, stream := range cwLogStreams.Filter(func(s CWLogStream) bool { return s.LogGroupName == req.DataSourceName }) {
			events, ok := cwLogEvents.Get(cwEventsKey(req.DataSourceName, stream.LogStreamName))
			if !ok {
				continue
			}
			for _, e := range events {
				var obj map[string]any
				if json.Unmarshal([]byte(e.Message), &obj) == nil {
					for k, v := range obj {
						fieldTypes[k] = cwLogFieldDataType(v)
					}
				}
			}
		}
	}
	logFields := []map[string]any{}
	for name, dt := range fieldTypes {
		logFields = append(logFields, map[string]any{
			"logFieldName": name,
			"logFieldType": map[string]any{"type": dt},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logFields": logFields})
}

// cwLogFieldDataType reports the CloudWatch Logs data-type name for a value
// parsed from a JSON log event message.
func cwLogFieldDataType(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case float64, json.Number:
		return "double"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "map"
	default:
		return "string"
	}
}

func handleCWListLogGroupsForQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryId string `json:"queryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.QueryId == "" {
		AWSError(w, "InvalidParameterException", "queryId is required", http.StatusBadRequest)
		return
	}
	q, ok := cwQueries.Get(req.QueryId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified query does not exist: %s", req.QueryId)
		return
	}
	// The log groups the query processed are the ones it was started against.
	// A query started against none yields an honestly-empty list.
	identifiers := append([]string{}, q.LogGroups...)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logGroupIdentifiers": identifiers})
}

func handleCWPutBearerTokenAuthentication(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier               string `json:"logGroupIdentifier"`
		BearerTokenAuthenticationEnabled bool   `json:"bearerTokenAuthenticationEnabled"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupIdentifier == "" {
		AWSError(w, "InvalidParameterException", "logGroupIdentifier is required", http.StatusBadRequest)
		return
	}
	name := cwLogGroupIdentifierToName(req.LogGroupIdentifier)
	if _, ok := cwLogGroups.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", name)
		return
	}
	cwBearerTokenAuth.Put(name, req.BearerTokenAuthenticationEnabled)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWUpdateDeliveryConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id             string   `json:"id"`
		RecordFields   []string `json:"recordFields"`
		FieldDelimiter string   `json:"fieldDelimiter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidParameterException", "id is required", http.StatusBadRequest)
		return
	}
	updated := cwDeliveries.Update(req.Id, func(d *CWDelivery) {
		if req.RecordFields != nil {
			d.RecordFields = req.RecordFields
		}
		if req.FieldDelimiter != "" {
			d.FieldDelimiter = req.FieldDelimiter
		}
	})
	if !updated {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery does not exist: %s", req.Id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
