package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudTrail Lake control plane: event data stores, Lake queries (run
// synchronously over the same recorded events LookupEvents serves), dashboards,
// imports, federation, resource policies, organization delegated admin, event
// configuration, and public keys. Each is a real, faithfully-stored resource —
// no fakes.

// CloudTrailEventDataStore is a CloudTrail Lake event data store: a named
// resource with an ARN (arn:aws:cloudtrail:region:acct:eventdatastore/UUID), a
// lifecycle Status (CREATED → ENABLED, or STOPPED_INGESTION / PENDING_DELETION),
// a retention period, and advanced event selectors.
type CloudTrailEventDataStore struct {
	ARN                          string
	Name                         string
	Status                       string
	RetentionPeriod              int
	MultiRegionEnabled           bool
	OrganizationEnabled          bool
	TerminationProtectionEnabled bool
	BillingMode                  string
	KmsKeyId                     string
	AdvancedEventSelectors       []map[string]any
	FederationStatus             string
	FederationRoleArn            string
	Tags                         []EC2Tag
	CreatedTimestamp             string
	UpdatedTimestamp             string
}

// CloudTrailQuery is a Lake query run over the recorded events. The sim runs it
// synchronously and settles it FINISHED, materialising the matched event rows.
type CloudTrailQuery struct {
	QueryId        string
	EventDataStore string
	QueryStatement string
	QueryStatus    string
	CreationTime   string
	DeliveryS3Uri  string
	Rows           []map[string]string
	BytesScanned   int64
	EventsScanned  int64
	EventsMatched  int64
}

// CloudTrailDashboard is a CloudTrail Lake dashboard: a named resource with an
// ARN and a set of query widgets.
type CloudTrailDashboard struct {
	ARN                          string
	Name                         string
	Type                         string
	Widgets                      []map[string]any
	RefreshSchedule              map[string]any
	TerminationProtectionEnabled bool
	Tags                         []EC2Tag
	CreatedTimestamp             string
	UpdatedTimestamp             string
}

// CloudTrailImport is a CloudTrail Lake import job copying historical events
// from S3 into one or more event-data-store destinations.
type CloudTrailImport struct {
	ImportId         string
	Destinations     []string
	ImportSource     map[string]any
	ImportStatus     string
	StartEventTime   *float64
	EndEventTime     *float64
	CreatedTimestamp string
	UpdatedTimestamp string
}

var (
	cloudTrailEventDataStores sim.Store[CloudTrailEventDataStore]
	cloudTrailQueries         sim.Store[CloudTrailQuery]
	cloudTrailDashboards      sim.Store[CloudTrailDashboard]
	cloudTrailImports         sim.Store[CloudTrailImport]
	cloudTrailOrgAdmins       sim.Store[CloudTrailOrgAdmin]
)

// CloudTrailOrgAdmin records an account registered as the organization's
// CloudTrail delegated administrator.
type CloudTrailOrgAdmin struct {
	AccountId string
}

func registerCloudTrailLake(r *sim.AWSRouter, srv *sim.Server) {
	cloudTrailEventDataStores = sim.MakeStore[CloudTrailEventDataStore](srv.DB(), "cloudtrail_event_data_stores")
	cloudTrailQueries = sim.MakeStore[CloudTrailQuery](srv.DB(), "cloudtrail_queries")
	cloudTrailDashboards = sim.MakeStore[CloudTrailDashboard](srv.DB(), "cloudtrail_dashboards")
	cloudTrailImports = sim.MakeStore[CloudTrailImport](srv.DB(), "cloudtrail_imports")
	cloudTrailOrgAdmins = sim.MakeStore[CloudTrailOrgAdmin](srv.DB(), "cloudtrail_org_admins")

	ops := map[string]http.HandlerFunc{
		"CreateEventDataStore":                 handleCloudTrailCreateEventDataStore,
		"GetEventDataStore":                    handleCloudTrailGetEventDataStore,
		"ListEventDataStores":                  handleCloudTrailListEventDataStores,
		"UpdateEventDataStore":                 handleCloudTrailUpdateEventDataStore,
		"DeleteEventDataStore":                 handleCloudTrailDeleteEventDataStore,
		"RestoreEventDataStore":                handleCloudTrailRestoreEventDataStore,
		"StartEventDataStoreIngestion":         handleCloudTrailStartIngestion,
		"StopEventDataStoreIngestion":          handleCloudTrailStopIngestion,
		"StartQuery":                           handleCloudTrailStartQuery,
		"DescribeQuery":                        handleCloudTrailDescribeQuery,
		"GetQueryResults":                      handleCloudTrailGetQueryResults,
		"CancelQuery":                          handleCloudTrailCancelQuery,
		"ListQueries":                          handleCloudTrailListQueries,
		"GenerateQuery":                        handleCloudTrailGenerateQuery,
		"SearchSampleQueries":                  handleCloudTrailSearchSampleQueries,
		"ListInsightsData":                     handleCloudTrailListInsightsData,
		"ListInsightsMetricData":               handleCloudTrailListInsightsMetricData,
		"CreateDashboard":                      handleCloudTrailCreateDashboard,
		"GetDashboard":                         handleCloudTrailGetDashboard,
		"ListDashboards":                       handleCloudTrailListDashboards,
		"UpdateDashboard":                      handleCloudTrailUpdateDashboard,
		"DeleteDashboard":                      handleCloudTrailDeleteDashboard,
		"StartDashboardRefresh":                handleCloudTrailStartDashboardRefresh,
		"StartImport":                          handleCloudTrailStartImport,
		"GetImport":                            handleCloudTrailGetImport,
		"StopImport":                           handleCloudTrailStopImport,
		"ListImports":                          handleCloudTrailListImports,
		"ListImportFailures":                   handleCloudTrailListImportFailures,
		"EnableFederation":                     handleCloudTrailEnableFederation,
		"DisableFederation":                    handleCloudTrailDisableFederation,
		"PutResourcePolicy":                    handleCloudTrailPutResourcePolicy,
		"GetResourcePolicy":                    handleCloudTrailGetResourcePolicy,
		"DeleteResourcePolicy":                 handleCloudTrailDeleteResourcePolicy,
		"RegisterOrganizationDelegatedAdmin":   handleCloudTrailRegisterOrgAdmin,
		"DeregisterOrganizationDelegatedAdmin": handleCloudTrailDeregisterOrgAdmin,
		"GetEventConfiguration":                handleCloudTrailGetEventConfiguration,
		"PutEventConfiguration":                handleCloudTrailPutEventConfiguration,
		"ListPublicKeys":                       handleCloudTrailListPublicKeys,
		"UpdateChannel":                        handleCloudTrailUpdateChannel,
	}
	for op, handler := range ops {
		r.Register("CloudTrail_20131101."+op, handler)
		r.Register("com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101."+op, handler)
	}
}

func cloudTrailEDSARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudtrail:%s:%s:eventdatastore/%s", awsRegion(), awsAccountID(), id)
}

func cloudTrailDashboardARN(name string) string {
	return fmt.Sprintf("arn:aws:cloudtrail:%s:%s:dashboard/%s", awsRegion(), awsAccountID(), name)
}

// findCloudTrailEDS resolves an event data store by ARN or by the bare UUID that
// terminates the ARN (the EventDataStore request field accepts either).
func findCloudTrailEDS(arnOrID string) (CloudTrailEventDataStore, bool) {
	if eds, ok := cloudTrailEventDataStores.Get(arnOrID); ok {
		return eds, true
	}
	for _, eds := range cloudTrailEventDataStores.List() {
		if eds.ARN == arnOrID || strings.HasSuffix(eds.ARN, "/"+arnOrID) {
			return eds, true
		}
	}
	return CloudTrailEventDataStore{}, false
}

// cloudTrailEDSResponse renders the full event-data-store response shape shared
// by Create/Get/Update/Restore (each emits the subset of these its response
// shape declares; the spec-shape validator tolerates the superset of members
// only when each is a real member of that response, so callers pick the fields).
func cloudTrailEDSResponse(eds CloudTrailEventDataStore) map[string]any {
	resp := map[string]any{
		"EventDataStoreArn":            eds.ARN,
		"Name":                         eds.Name,
		"Status":                       eds.Status,
		"RetentionPeriod":              eds.RetentionPeriod,
		"MultiRegionEnabled":           eds.MultiRegionEnabled,
		"OrganizationEnabled":          eds.OrganizationEnabled,
		"TerminationProtectionEnabled": eds.TerminationProtectionEnabled,
		"BillingMode":                  eds.BillingMode,
		"AdvancedEventSelectors":       eds.AdvancedEventSelectors,
		"CreatedTimestamp":             cloudTrailEpochSeconds(eds.CreatedTimestamp),
		"UpdatedTimestamp":             cloudTrailEpochSeconds(eds.UpdatedTimestamp),
	}
	if eds.KmsKeyId != "" {
		resp["KmsKeyId"] = eds.KmsKeyId
	}
	return resp
}

func handleCloudTrailCreateEventDataStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                         string
		RetentionPeriod              *int
		MultiRegionEnabled           *bool
		OrganizationEnabled          *bool
		TerminationProtectionEnabled *bool
		BillingMode                  string
		KmsKeyId                     string
		StartIngestion               *bool
		AdvancedEventSelectors       []map[string]any
		TagsList                     []EC2Tag
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		cloudTrailError(w, "InvalidParameterException", "Name is required", http.StatusBadRequest)
		return
	}
	for _, eds := range cloudTrailEventDataStores.List() {
		if eds.Name == req.Name {
			cloudTrailError(w, "EventDataStoreAlreadyExistsException",
				"An event data store with the specified name already exists", http.StatusBadRequest)
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	retention := 366
	if req.RetentionPeriod != nil {
		retention = *req.RetentionPeriod
	}
	billing := req.BillingMode
	if billing == "" {
		billing = "EXTENDABLE_RETENTION_PRICING"
	}
	status := "ENABLED"
	if req.StartIngestion != nil && !*req.StartIngestion {
		status = "CREATED"
	}
	eds := CloudTrailEventDataStore{
		ARN:                          cloudTrailEDSARN(generateUUID()),
		Name:                         req.Name,
		Status:                       status,
		RetentionPeriod:              retention,
		MultiRegionEnabled:           req.MultiRegionEnabled != nil && *req.MultiRegionEnabled,
		OrganizationEnabled:          req.OrganizationEnabled != nil && *req.OrganizationEnabled,
		TerminationProtectionEnabled: req.TerminationProtectionEnabled == nil || *req.TerminationProtectionEnabled,
		BillingMode:                  billing,
		KmsKeyId:                     req.KmsKeyId,
		AdvancedEventSelectors:       req.AdvancedEventSelectors,
		Tags:                         req.TagsList,
		CreatedTimestamp:             now,
		UpdatedTimestamp:             now,
	}
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	resp := cloudTrailEDSResponse(eds)
	resp["TagsList"] = eds.Tags
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailGetEventDataStore(w http.ResponseWriter, r *http.Request) {
	eds, ok := cloudTrailEDSFromRequest(w, r)
	if !ok {
		return
	}
	resp := cloudTrailEDSResponse(eds)
	resp["PartitionKeys"] = []map[string]any{
		{"Name": "eventData", "Type": "varchar"},
	}
	if eds.FederationStatus != "" {
		resp["FederationStatus"] = eds.FederationStatus
	}
	if eds.FederationRoleArn != "" {
		resp["FederationRoleArn"] = eds.FederationRoleArn
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailListEventDataStores(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int
		NextToken  string
	}
	_ = readAWSJSONAllowEmpty(r, &req)
	list := make([]map[string]any, 0)
	for _, eds := range cloudTrailEventDataStores.List() {
		list = append(list, map[string]any{
			"EventDataStoreArn":            eds.ARN,
			"Name":                         eds.Name,
			"Status":                       eds.Status,
			"RetentionPeriod":              eds.RetentionPeriod,
			"MultiRegionEnabled":           eds.MultiRegionEnabled,
			"OrganizationEnabled":          eds.OrganizationEnabled,
			"TerminationProtectionEnabled": eds.TerminationProtectionEnabled,
			"AdvancedEventSelectors":       eds.AdvancedEventSelectors,
			"CreatedTimestamp":             cloudTrailEpochSeconds(eds.CreatedTimestamp),
			"UpdatedTimestamp":             cloudTrailEpochSeconds(eds.UpdatedTimestamp),
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"EventDataStores": list})
}

func handleCloudTrailUpdateEventDataStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventDataStore               string
		Name                         string
		RetentionPeriod              *int
		MultiRegionEnabled           *bool
		OrganizationEnabled          *bool
		TerminationProtectionEnabled *bool
		BillingMode                  string
		KmsKeyId                     string
		AdvancedEventSelectors       []map[string]any
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	eds, ok := findCloudTrailEDS(req.EventDataStore)
	if !ok {
		cloudTrailError(w, "EventDataStoreNotFoundException", "Event data store not found", http.StatusNotFound)
		return
	}
	if req.Name != "" {
		eds.Name = req.Name
	}
	if req.RetentionPeriod != nil {
		eds.RetentionPeriod = *req.RetentionPeriod
	}
	if req.MultiRegionEnabled != nil {
		eds.MultiRegionEnabled = *req.MultiRegionEnabled
	}
	if req.OrganizationEnabled != nil {
		eds.OrganizationEnabled = *req.OrganizationEnabled
	}
	if req.TerminationProtectionEnabled != nil {
		eds.TerminationProtectionEnabled = *req.TerminationProtectionEnabled
	}
	if req.BillingMode != "" {
		eds.BillingMode = req.BillingMode
	}
	if req.KmsKeyId != "" {
		eds.KmsKeyId = req.KmsKeyId
	}
	if req.AdvancedEventSelectors != nil {
		eds.AdvancedEventSelectors = req.AdvancedEventSelectors
	}
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	resp := cloudTrailEDSResponse(eds)
	if eds.FederationStatus != "" {
		resp["FederationStatus"] = eds.FederationStatus
	}
	if eds.FederationRoleArn != "" {
		resp["FederationRoleArn"] = eds.FederationRoleArn
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailDeleteEventDataStore(w http.ResponseWriter, r *http.Request) {
	eds, ok := cloudTrailEDSFromRequest(w, r)
	if !ok {
		return
	}
	// Real CloudTrail moves a deleted store to PENDING_DELETION (a 7-day waiting
	// period during which RestoreEventDataStore can recover it) rather than
	// removing it outright.
	eds.Status = "PENDING_DELETION"
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailRestoreEventDataStore(w http.ResponseWriter, r *http.Request) {
	eds, ok := cloudTrailEDSFromRequest(w, r)
	if !ok {
		return
	}
	eds.Status = "ENABLED"
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	writeAWSJSON(w, http.StatusOK, cloudTrailEDSResponse(eds))
}

func handleCloudTrailStartIngestion(w http.ResponseWriter, r *http.Request) {
	eds, ok := cloudTrailEDSFromRequest(w, r)
	if !ok {
		return
	}
	eds.Status = "ENABLED"
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailStopIngestion(w http.ResponseWriter, r *http.Request) {
	eds, ok := cloudTrailEDSFromRequest(w, r)
	if !ok {
		return
	}
	eds.Status = "STOPPED_INGESTION"
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func cloudTrailEDSFromRequest(w http.ResponseWriter, r *http.Request) (CloudTrailEventDataStore, bool) {
	var req struct {
		EventDataStore string
	}
	if !readAWSJSON(w, r, &req) {
		return CloudTrailEventDataStore{}, false
	}
	eds, ok := findCloudTrailEDS(req.EventDataStore)
	if !ok {
		cloudTrailError(w, "EventDataStoreNotFoundException", "Event data store not found", http.StatusNotFound)
		return CloudTrailEventDataStore{}, false
	}
	return eds, true
}

func handleCloudTrailStartQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryStatement  string
		DeliveryS3Uri   string
		QueryAlias      string
		QueryParameters []string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.QueryStatement == "" && req.QueryAlias == "" {
		cloudTrailError(w, "InvalidQueryStatementException",
			"QueryStatement or QueryAlias is required", http.StatusBadRequest)
		return
	}
	// Run the query synchronously over the recorded events and settle FINISHED.
	rows, scanned, matched := cloudTrailRunQuery(req.QueryStatement)
	query := CloudTrailQuery{
		QueryId:        generateUUID(),
		QueryStatement: req.QueryStatement,
		QueryStatus:    "FINISHED",
		CreationTime:   time.Now().UTC().Format(time.RFC3339),
		DeliveryS3Uri:  req.DeliveryS3Uri,
		Rows:           rows,
		EventsScanned:  scanned,
		EventsMatched:  matched,
		BytesScanned:   scanned * 256,
	}
	cloudTrailQueries.Put(query.QueryId, query)
	writeAWSJSON(w, http.StatusOK, map[string]any{"QueryId": query.QueryId})
}

// cloudTrailRunQuery executes a Lake query over the sim's recorded CloudTrail
// events. It returns rows (one per matched event) plus scanned/matched counts.
// The query statement's WHERE eventName = '<X>' / eventSource = '<X>' clauses,
// when present, scope the rows; everything else returns all recorded events —
// the faithful "run a query over the events the sim already stores" behavior.
func cloudTrailRunQuery(statement string) (rows []map[string]string, scanned, matched int64) {
	events := cloudTrailEvents.List()
	scanned = int64(len(events))
	lower := strings.ToLower(statement)
	wantName := cloudTrailQueryEquals(lower, "eventname")
	wantSource := cloudTrailQueryEquals(lower, "eventsource")
	rows = make([]map[string]string, 0)
	for _, ev := range events {
		if wantName != "" && !strings.EqualFold(ev.EventName, wantName) {
			continue
		}
		if wantSource != "" && !strings.EqualFold(ev.EventSource, wantSource) {
			continue
		}
		rows = append(rows, map[string]string{
			"eventName":   ev.EventName,
			"eventSource": ev.EventSource,
			"eventTime":   ev.EventTime,
			"eventId":     ev.EventId,
		})
	}
	matched = int64(len(rows))
	return rows, scanned, matched
}

// cloudTrailQueryEquals extracts the value of a `<column> = '<value>'` predicate
// from a (lowercased) query statement, if present. Used to scope a Lake query to
// a specific eventName/eventSource over the recorded events.
func cloudTrailQueryEquals(lowerStatement, column string) string {
	idx := strings.Index(lowerStatement, column)
	for idx >= 0 {
		rest := strings.TrimSpace(lowerStatement[idx+len(column):])
		if strings.HasPrefix(rest, "=") {
			rest = strings.TrimSpace(rest[1:])
			if strings.HasPrefix(rest, "'") {
				if end := strings.Index(rest[1:], "'"); end >= 0 {
					return rest[1 : 1+end]
				}
			}
		}
		next := strings.Index(lowerStatement[idx+len(column):], column)
		if next < 0 {
			break
		}
		idx = idx + len(column) + next
	}
	return ""
}

func findCloudTrailQuery(w http.ResponseWriter, r *http.Request) (CloudTrailQuery, bool) {
	var req struct {
		QueryId string
	}
	if !readAWSJSON(w, r, &req) {
		return CloudTrailQuery{}, false
	}
	q, ok := cloudTrailQueries.Get(req.QueryId)
	if !ok {
		cloudTrailError(w, "QueryIdNotFoundException", "Query not found", http.StatusNotFound)
		return CloudTrailQuery{}, false
	}
	return q, true
}

func handleCloudTrailDescribeQuery(w http.ResponseWriter, r *http.Request) {
	q, ok := findCloudTrailQuery(w, r)
	if !ok {
		return
	}
	resp := map[string]any{
		"QueryId":     q.QueryId,
		"QueryString": q.QueryStatement,
		"QueryStatus": q.QueryStatus,
		"QueryStatistics": map[string]any{
			"EventsMatched":         q.EventsMatched,
			"EventsScanned":         q.EventsScanned,
			"BytesScanned":          q.BytesScanned,
			"ExecutionTimeInMillis": 1,
			"CreationTime":          cloudTrailEpochSeconds(q.CreationTime),
		},
	}
	if q.DeliveryS3Uri != "" {
		resp["DeliveryS3Uri"] = q.DeliveryS3Uri
		resp["DeliveryStatus"] = "SUCCESS"
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailGetQueryResults(w http.ResponseWriter, r *http.Request) {
	q, ok := findCloudTrailQuery(w, r)
	if !ok {
		return
	}
	rows := make([]map[string]string, 0, len(q.Rows))
	rows = append(rows, q.Rows...)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"QueryStatus":     q.QueryStatus,
		"QueryResultRows": cloudTrailQueryResultRows(rows),
		"QueryStatistics": map[string]any{
			"ResultsCount":      len(rows),
			"TotalResultsCount": len(rows),
			"BytesScanned":      q.BytesScanned,
		},
	})
}

// cloudTrailQueryResultRows shapes the rows as the wire form GetQueryResults
// returns: a list of rows, each row a list of single-entry {column: value} maps.
func cloudTrailQueryResultRows(rows []map[string]string) [][]map[string]string {
	out := make([][]map[string]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]map[string]string, 0, len(row))
		for _, col := range []string{"eventId", "eventName", "eventSource", "eventTime"} {
			if v, ok := row[col]; ok {
				cells = append(cells, map[string]string{col: v})
			}
		}
		out = append(out, cells)
	}
	return out
}

func handleCloudTrailCancelQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	q, ok := cloudTrailQueries.Get(req.QueryId)
	if !ok {
		cloudTrailError(w, "QueryIdNotFoundException", "Query not found", http.StatusNotFound)
		return
	}
	q.QueryStatus = "CANCELLED"
	cloudTrailQueries.Put(q.QueryId, q)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"QueryId":     q.QueryId,
		"QueryStatus": q.QueryStatus,
	})
}

func handleCloudTrailListQueries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventDataStore string
		QueryStatus    string
	}
	_ = readAWSJSONAllowEmpty(r, &req)
	queries := cloudTrailQueries.List()
	sort.SliceStable(queries, func(i, j int) bool {
		return queries[i].CreationTime > queries[j].CreationTime
	})
	out := make([]map[string]any, 0)
	for _, q := range queries {
		if req.QueryStatus != "" && q.QueryStatus != req.QueryStatus {
			continue
		}
		out = append(out, map[string]any{
			"QueryId":      q.QueryId,
			"QueryStatus":  q.QueryStatus,
			"CreationTime": cloudTrailEpochSeconds(q.CreationTime),
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Queries": out})
}

func handleCloudTrailGenerateQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventDataStores []string
		Prompt          string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if len(req.EventDataStores) == 0 || req.Prompt == "" {
		cloudTrailError(w, "InvalidParameterException",
			"EventDataStores and Prompt are required", http.StatusBadRequest)
		return
	}
	// Deterministically generate a SQL statement scanning the named event data
	// store, mirroring the natural-language → SQL behavior of the real op.
	statement := fmt.Sprintf("SELECT eventName, eventSource, eventTime FROM %s LIMIT 10", req.EventDataStores[0])
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"QueryStatement": statement,
		"QueryAlias":     "generated-query",
	})
}

func handleCloudTrailSearchSampleQueries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SearchPhrase string
		MaxResults   int
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.SearchPhrase == "" {
		cloudTrailError(w, "InvalidParameterException", "SearchPhrase is required", http.StatusBadRequest)
		return
	}
	results := []map[string]any{
		{
			"Name":        "Sample: events by name",
			"Description": "Lists recorded events matching " + req.SearchPhrase,
			"SQL":         "SELECT eventName, eventSource FROM $EDS_ID WHERE eventName = '" + req.SearchPhrase + "'",
			"Relevance":   1.0,
		},
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"SearchResults": results})
}

func handleCloudTrailListInsightsData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InsightSource string
		DataType      string
	}
	_ = readAWSJSONAllowEmpty(r, &req)
	writeAWSJSON(w, http.StatusOK, map[string]any{"Events": []map[string]any{}})
}

func handleCloudTrailListInsightsMetricData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventName   string
		EventSource string
		InsightType string
		ErrorCode   string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	resp := map[string]any{
		"EventName":   req.EventName,
		"EventSource": req.EventSource,
		"InsightType": req.InsightType,
		"Timestamps":  []float64{},
		"Values":      []float64{},
	}
	if req.ErrorCode != "" {
		resp["ErrorCode"] = req.ErrorCode
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailCreateDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                         string
		Widgets                      []map[string]any
		RefreshSchedule              map[string]any
		TerminationProtectionEnabled *bool
		TagsList                     []EC2Tag
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		cloudTrailError(w, "InvalidParameterException", "Name is required", http.StatusBadRequest)
		return
	}
	if _, ok := cloudTrailDashboards.Get(req.Name); ok {
		cloudTrailError(w, "ResourceAlreadyExistsException",
			"A dashboard with the specified name already exists", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	dash := CloudTrailDashboard{
		ARN:                          cloudTrailDashboardARN(req.Name),
		Name:                         req.Name,
		Type:                         "CUSTOM",
		Widgets:                      req.Widgets,
		RefreshSchedule:              req.RefreshSchedule,
		TerminationProtectionEnabled: req.TerminationProtectionEnabled != nil && *req.TerminationProtectionEnabled,
		Tags:                         req.TagsList,
		CreatedTimestamp:             now,
		UpdatedTimestamp:             now,
	}
	cloudTrailDashboards.Put(dash.Name, dash)
	resp := map[string]any{
		"DashboardArn":                 dash.ARN,
		"Name":                         dash.Name,
		"Type":                         dash.Type,
		"Widgets":                      dash.Widgets,
		"TerminationProtectionEnabled": dash.TerminationProtectionEnabled,
		"TagsList":                     dash.Tags,
	}
	if dash.RefreshSchedule != nil {
		resp["RefreshSchedule"] = dash.RefreshSchedule
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailGetDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	dash, ok := findCloudTrailDashboard(req.DashboardId)
	if !ok {
		cloudTrailError(w, "ResourceNotFoundException", "Dashboard not found", http.StatusNotFound)
		return
	}
	resp := map[string]any{
		"DashboardArn":                 dash.ARN,
		"Type":                         dash.Type,
		"Status":                       "CREATED",
		"Widgets":                      dash.Widgets,
		"TerminationProtectionEnabled": dash.TerminationProtectionEnabled,
		"CreatedTimestamp":             cloudTrailEpochSeconds(dash.CreatedTimestamp),
		"UpdatedTimestamp":             cloudTrailEpochSeconds(dash.UpdatedTimestamp),
	}
	if dash.RefreshSchedule != nil {
		resp["RefreshSchedule"] = dash.RefreshSchedule
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailListDashboards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix string
		Type       string
	}
	_ = readAWSJSONAllowEmpty(r, &req)
	out := make([]map[string]any, 0)
	for _, dash := range cloudTrailDashboards.List() {
		if req.NamePrefix != "" && !strings.HasPrefix(dash.Name, req.NamePrefix) {
			continue
		}
		if req.Type != "" && dash.Type != req.Type {
			continue
		}
		out = append(out, map[string]any{
			"DashboardArn": dash.ARN,
			"Type":         dash.Type,
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Dashboards": out})
}

func findCloudTrailDashboard(idOrARN string) (CloudTrailDashboard, bool) {
	if d, ok := cloudTrailDashboards.Get(idOrARN); ok {
		return d, true
	}
	for _, d := range cloudTrailDashboards.List() {
		if d.ARN == idOrARN || strings.HasSuffix(d.ARN, "/"+idOrARN) {
			return d, true
		}
	}
	return CloudTrailDashboard{}, false
}

func handleCloudTrailUpdateDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardId                  string
		Widgets                      []map[string]any
		RefreshSchedule              map[string]any
		TerminationProtectionEnabled *bool
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	dash, ok := findCloudTrailDashboard(req.DashboardId)
	if !ok {
		cloudTrailError(w, "ResourceNotFoundException", "Dashboard not found", http.StatusNotFound)
		return
	}
	if req.Widgets != nil {
		dash.Widgets = req.Widgets
	}
	if req.RefreshSchedule != nil {
		dash.RefreshSchedule = req.RefreshSchedule
	}
	if req.TerminationProtectionEnabled != nil {
		dash.TerminationProtectionEnabled = *req.TerminationProtectionEnabled
	}
	dash.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailDashboards.Put(dash.Name, dash)
	resp := map[string]any{
		"DashboardArn":                 dash.ARN,
		"Name":                         dash.Name,
		"Type":                         dash.Type,
		"Widgets":                      dash.Widgets,
		"TerminationProtectionEnabled": dash.TerminationProtectionEnabled,
		"CreatedTimestamp":             cloudTrailEpochSeconds(dash.CreatedTimestamp),
		"UpdatedTimestamp":             cloudTrailEpochSeconds(dash.UpdatedTimestamp),
	}
	if dash.RefreshSchedule != nil {
		resp["RefreshSchedule"] = dash.RefreshSchedule
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailDeleteDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	dash, ok := findCloudTrailDashboard(req.DashboardId)
	if !ok {
		cloudTrailError(w, "ResourceNotFoundException", "Dashboard not found", http.StatusNotFound)
		return
	}
	cloudTrailDashboards.Delete(dash.Name)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

// cloudTrailRefreshID returns the decimal refresh id AWS CloudTrail assigns to
// a dashboard refresh.
func cloudTrailRefreshID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return strconv.FormatUint(binary.BigEndian.Uint64(buf)%1_000_000_000_000, 10)
}

func handleCloudTrailStartDashboardRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if _, ok := findCloudTrailDashboard(req.DashboardId); !ok {
		cloudTrailError(w, "ResourceNotFoundException", "Dashboard not found", http.StatusNotFound)
		return
	}
	// A refresh id is a decimal number, not a UUID: the model admits only
	// digits, so a client that round-trips it into GetDashboard would be
	// sending a value the service rejects.
	writeAWSJSON(w, http.StatusOK, map[string]any{"RefreshId": cloudTrailRefreshID()})
}

func handleCloudTrailStartImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Destinations   []string
		ImportSource   map[string]any
		StartEventTime *float64
		EndEventTime   *float64
		ImportId       string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	imp, existing := cloudTrailImports.Get(req.ImportId)
	if req.ImportId == "" || !existing {
		imp = CloudTrailImport{
			ImportId:         generateUUID(),
			Destinations:     req.Destinations,
			ImportSource:     req.ImportSource,
			ImportStatus:     "IN_PROGRESS",
			StartEventTime:   req.StartEventTime,
			EndEventTime:     req.EndEventTime,
			CreatedTimestamp: now,
			UpdatedTimestamp: now,
		}
	} else {
		imp.ImportStatus = "IN_PROGRESS"
		imp.UpdatedTimestamp = now
	}
	cloudTrailImports.Put(imp.ImportId, imp)
	writeAWSJSON(w, http.StatusOK, cloudTrailImportResponse(imp, false))
}

// cloudTrailImportResponse renders the import response shape. withStats includes
// the ImportStatistics (GetImport / StopImport carry it; StartImport does not).
func cloudTrailImportResponse(imp CloudTrailImport, withStats bool) map[string]any {
	resp := map[string]any{
		"ImportId":         imp.ImportId,
		"Destinations":     imp.Destinations,
		"ImportSource":     imp.ImportSource,
		"ImportStatus":     imp.ImportStatus,
		"CreatedTimestamp": cloudTrailEpochSeconds(imp.CreatedTimestamp),
		"UpdatedTimestamp": cloudTrailEpochSeconds(imp.UpdatedTimestamp),
	}
	if imp.StartEventTime != nil {
		resp["StartEventTime"] = *imp.StartEventTime
	}
	if imp.EndEventTime != nil {
		resp["EndEventTime"] = *imp.EndEventTime
	}
	if withStats {
		resp["ImportStatistics"] = map[string]any{
			"PrefixesFound":     0,
			"PrefixesCompleted": 0,
			"FilesCompleted":    0,
			"EventsCompleted":   0,
			"FailedEntries":     0,
		}
	}
	return resp
}

func handleCloudTrailGetImport(w http.ResponseWriter, r *http.Request) {
	imp, ok := cloudTrailImportFromRequest(w, r)
	if !ok {
		return
	}
	writeAWSJSON(w, http.StatusOK, cloudTrailImportResponse(imp, true))
}

func handleCloudTrailStopImport(w http.ResponseWriter, r *http.Request) {
	imp, ok := cloudTrailImportFromRequest(w, r)
	if !ok {
		return
	}
	imp.ImportStatus = "STOPPED"
	imp.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailImports.Put(imp.ImportId, imp)
	writeAWSJSON(w, http.StatusOK, cloudTrailImportResponse(imp, true))
}

func cloudTrailImportFromRequest(w http.ResponseWriter, r *http.Request) (CloudTrailImport, bool) {
	var req struct {
		ImportId string
	}
	if !readAWSJSON(w, r, &req) {
		return CloudTrailImport{}, false
	}
	imp, ok := cloudTrailImports.Get(req.ImportId)
	if !ok {
		cloudTrailError(w, "ImportNotFoundException", "Import not found", http.StatusNotFound)
		return CloudTrailImport{}, false
	}
	return imp, true
}

func handleCloudTrailListImports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportStatus string
		Destination  string
	}
	_ = readAWSJSONAllowEmpty(r, &req)
	out := make([]map[string]any, 0)
	for _, imp := range cloudTrailImports.List() {
		if req.ImportStatus != "" && imp.ImportStatus != req.ImportStatus {
			continue
		}
		out = append(out, map[string]any{
			"ImportId":         imp.ImportId,
			"Destinations":     imp.Destinations,
			"ImportStatus":     imp.ImportStatus,
			"CreatedTimestamp": cloudTrailEpochSeconds(imp.CreatedTimestamp),
			"UpdatedTimestamp": cloudTrailEpochSeconds(imp.UpdatedTimestamp),
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Imports": out})
}

func handleCloudTrailListImportFailures(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if _, ok := cloudTrailImports.Get(req.ImportId); !ok {
		cloudTrailError(w, "ImportNotFoundException", "Import not found", http.StatusNotFound)
		return
	}
	// A clean import has no failures.
	writeAWSJSON(w, http.StatusOK, map[string]any{"Failures": []map[string]any{}})
}

func handleCloudTrailEnableFederation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventDataStore    string
		FederationRoleArn string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	eds, ok := findCloudTrailEDS(req.EventDataStore)
	if !ok {
		cloudTrailError(w, "EventDataStoreNotFoundException", "Event data store not found", http.StatusNotFound)
		return
	}
	if req.FederationRoleArn == "" {
		cloudTrailError(w, "InvalidParameterException", "FederationRoleArn is required", http.StatusBadRequest)
		return
	}
	eds.FederationStatus = "ENABLED"
	eds.FederationRoleArn = req.FederationRoleArn
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"EventDataStoreArn": eds.ARN,
		"FederationStatus":  eds.FederationStatus,
		"FederationRoleArn": eds.FederationRoleArn,
	})
}

func handleCloudTrailDisableFederation(w http.ResponseWriter, r *http.Request) {
	eds, ok := cloudTrailEDSFromRequest(w, r)
	if !ok {
		return
	}
	eds.FederationStatus = "DISABLED"
	eds.FederationRoleArn = ""
	eds.UpdatedTimestamp = time.Now().UTC().Format(time.RFC3339)
	cloudTrailEventDataStores.Put(eds.ARN, eds)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"EventDataStoreArn": eds.ARN,
		"FederationStatus":  eds.FederationStatus,
	})
}

func handleCloudTrailPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn    string
		ResourcePolicy string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.ResourceArn == "" || req.ResourcePolicy == "" {
		cloudTrailError(w, "InvalidParameterException",
			"ResourceArn and ResourcePolicy are required", http.StatusBadRequest)
		return
	}
	iamPutResourcePolicy(req.ResourceArn, req.ResourcePolicy)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"ResourceArn":    req.ResourceArn,
		"ResourcePolicy": req.ResourcePolicy,
	})
}

func handleCloudTrailGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	policy, ok := iamGetResourcePolicy(req.ResourceArn)
	if !ok {
		cloudTrailError(w, "ResourcePolicyNotFoundException", "Resource policy not found", http.StatusNotFound)
		return
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"ResourceArn":    req.ResourceArn,
		"ResourcePolicy": policy,
	})
}

func handleCloudTrailDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if _, ok := iamGetResourcePolicy(req.ResourceArn); !ok {
		cloudTrailError(w, "ResourcePolicyNotFoundException", "Resource policy not found", http.StatusNotFound)
		return
	}
	iamDeleteResourcePolicy(req.ResourceArn)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailRegisterOrgAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MemberAccountId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.MemberAccountId == "" {
		cloudTrailError(w, "InvalidParameterException", "MemberAccountId is required", http.StatusBadRequest)
		return
	}
	cloudTrailOrgAdmins.Put(req.MemberAccountId, CloudTrailOrgAdmin{AccountId: req.MemberAccountId})
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailDeregisterOrgAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DelegatedAdminAccountId string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if _, ok := cloudTrailOrgAdmins.Get(req.DelegatedAdminAccountId); !ok {
		cloudTrailError(w, "AccountNotRegisteredException",
			"Account is not registered as a delegated administrator", http.StatusBadRequest)
		return
	}
	cloudTrailOrgAdmins.Delete(req.DelegatedAdminAccountId)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailGetEventConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventDataStore string
		TrailName      string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	resp := map[string]any{
		"MaxEventSize":        "Standard",
		"ContextKeySelectors": []map[string]any{},
	}
	if req.EventDataStore != "" {
		eds, ok := findCloudTrailEDS(req.EventDataStore)
		if !ok {
			cloudTrailError(w, "EventDataStoreNotFoundException", "Event data store not found", http.StatusNotFound)
			return
		}
		resp["EventDataStoreArn"] = eds.ARN
	} else if req.TrailName != "" {
		trail, ok := findCloudTrail(req.TrailName)
		if !ok {
			cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
			return
		}
		resp["TrailARN"] = trail.ARN
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailPutEventConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventDataStore      string
		TrailName           string
		MaxEventSize        string
		ContextKeySelectors []map[string]any
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	maxSize := req.MaxEventSize
	if maxSize == "" {
		maxSize = "Standard"
	}
	selectors := req.ContextKeySelectors
	if selectors == nil {
		selectors = []map[string]any{}
	}
	resp := map[string]any{
		"MaxEventSize":        maxSize,
		"ContextKeySelectors": selectors,
	}
	if req.EventDataStore != "" {
		eds, ok := findCloudTrailEDS(req.EventDataStore)
		if !ok {
			cloudTrailError(w, "EventDataStoreNotFoundException", "Event data store not found", http.StatusNotFound)
			return
		}
		resp["EventDataStoreArn"] = eds.ARN
	} else if req.TrailName != "" {
		trail, ok := findCloudTrail(req.TrailName)
		if !ok {
			cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
			return
		}
		resp["TrailARN"] = trail.ARN
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailListPublicKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StartTime *float64
		EndTime   *float64
	}
	_ = readAWSJSONAllowEmpty(r, &req)
	// CloudTrail digest-signing public keys are an account-level static set. The
	// sim does not sign digest files, so it advertises no keys (a real, empty
	// result) rather than a fabricated key.
	writeAWSJSON(w, http.StatusOK, map[string]any{"PublicKeyList": []map[string]any{}})
}

func handleCloudTrailUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel      string
		Name         string
		Destinations []map[string]any
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	channel, ok := findCloudTrailChannel(req.Channel)
	if !ok {
		cloudTrailError(w, "ChannelNotFoundException", "Channel not found", http.StatusNotFound)
		return
	}
	if req.Name != "" {
		channel.Name = req.Name
	}
	if req.Destinations != nil {
		channel.Destinations = req.Destinations
	}
	cloudTrailChannels.Put(channel.ARN, channel)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"ChannelArn":   channel.ARN,
		"Name":         channel.Name,
		"Source":       channel.Source,
		"Destinations": channel.Destinations,
	})
}
