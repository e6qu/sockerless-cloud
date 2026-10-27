package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Application Insights data plane reads the log store the workload writes
// into, the store Log Analytics queries, addressed by app id rather than
// workspace id. An event type is a table, an event read is a query over it,
// and a metric is a count of its rows in the window.

// The `$all` type spans every table, so it maps to none of its own.
var insightsEventTables = map[string]string{
	"traces":              "AppTraces",
	"customEvents":        "AppEvents",
	"pageViews":           "AppPageViews",
	"browserTimings":      "AppBrowserTimings",
	"requests":            "AppRequests",
	"dependencies":        "AppDependencies",
	"exceptions":          "AppExceptions",
	"availabilityResults": "AppAvailabilityResults",
	"performanceCounters": "AppPerformanceCounters",
	"customMetrics":       "AppMetrics",
}

func registerInsightsDataPlane(srv *sim.Server) {
	// Query_Execute and Query_Get.
	srv.HandleFunc("POST /v1/apps/{appId}/query", func(w http.ResponseWriter, r *http.Request) {
		scope, ok := insightsAppScope(w, r)
		if !ok {
			return
		}
		var req QueryRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "BadArgumentError",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Query == "" {
			AzureError(w, "BadArgumentError", "The 'query' property is required.", http.StatusBadRequest)
			return
		}
		writeKQLResult(w, scope, req.Query, req.Timespan)
	})
	srv.HandleFunc("GET /v1/apps/{appId}/query", func(w http.ResponseWriter, r *http.Request) {
		scope, ok := insightsAppScope(w, r)
		if !ok {
			return
		}
		query := r.URL.Query().Get("query")
		if query == "" {
			AzureError(w, "BadArgumentError", "The 'query' parameter is required.", http.StatusBadRequest)
			return
		}
		writeKQLResult(w, scope, query, r.URL.Query().Get("timespan"))
	})

	// Metadata_Get and Metadata_Post.
	metadata := func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, insightsMetadataDoc(sim.PathParam(r, "appId")))
	}
	srv.HandleFunc("GET /v1/apps/{appId}/metadata", metadata)
	srv.HandleFunc("POST /v1/apps/{appId}/metadata", metadata)

	// Events_GetOdataMetadata describes the event types the API serves, not any
	// application's data.
	srv.HandleFunc("GET /v1/apps/{appId}/events/$metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(insightsEventsODataMetadata()))
	})

	srv.HandleFunc("GET /v1/apps/{appId}/events/{eventType}", insightsGetEventsByType)
	srv.HandleFunc("GET /v1/apps/{appId}/events/{eventType}/{eventId}", insightsGetEvent)

	srv.HandleFunc("GET /v1/apps/{appId}/metrics/metadata", insightsGetMetricsMetadata)
	// A metric id carries a slash ("requests/count"), so the tail is greedy; the
	// literal metrics/metadata sibling is more specific and still wins.
	srv.HandleFunc("GET /v1/apps/{appId}/metrics/{metricId...}", insightsGetMetric)
	srv.HandleFunc("POST /v1/apps/{appId}/metrics", insightsGetMetrics)
}

func insightsAppScope(w http.ResponseWriter, r *http.Request) (logScope, bool) {
	scope, ok := appLogScope(sim.PathParam(r, "appId"))
	if !ok {
		sim.WriteJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{
			"code":    "PathNotFoundError",
			"message": "The requested path does not exist",
		}})
	}
	return scope, ok
}

func insightsEventRows(appID, eventType string) ([]map[string]any, bool) {
	if eventType == "$all" {
		var all []map[string]any
		types := make([]string, 0, len(insightsEventTables))
		for name := range insightsEventTables {
			types = append(types, name)
		}
		sort.Strings(types)
		for _, name := range types {
			rows, _ := insightsEventRows(appID, name)
			all = append(all, rows...)
		}
		return all, true
	}
	table, ok := insightsEventTables[eventType]
	if !ok {
		return nil, false
	}
	scope, found := appLogScope(appID)
	if !found {
		return nil, true
	}
	result, err := runKQLQuery(scope, table, "")
	if err != nil {
		return nil, false
	}
	var rows []map[string]any
	for _, t := range result.Tables {
		for _, row := range t.Rows {
			rows = append(rows, insightsEventDoc(eventType, t.Columns, row))
		}
	}
	return rows, true
}

// Members the row does not carry are left out rather than zero-filled: an
// event with no session did not have one.
func insightsEventDoc(eventType string, columns []Column, row []any) map[string]any {
	doc := map[string]any{
		"type":  eventType,
		"count": 1,
	}
	// A table's columns are not customDimensions, the property bag a caller
	// attaches to its telemetry, which the document types as objects.
	for i, column := range columns {
		if i >= len(row) || row[i] == nil {
			continue
		}
		switch column.Name {
		case "TimeGenerated":
			doc["timestamp"] = row[i]
		case "_ItemId", "Id":
			doc["id"] = row[i]
		}
	}
	if _, ok := doc["id"]; !ok {
		// A row without an ingestion-stamped id is identified by its timestamp and
		// type.
		timestamp, _ := doc["timestamp"].(string)
		doc["id"] = eventType + ":" + timestamp
	}
	return doc
}

func insightsGetEventsByType(w http.ResponseWriter, r *http.Request) {
	eventType := sim.PathParam(r, "eventType")
	rows, ok := insightsEventRows(sim.PathParam(r, "appId"), eventType)
	if !ok {
		AzureError(w, "BadArgumentError",
			"The event type '"+eventType+"' is not one this API serves.", http.StatusBadRequest)
		return
	}
	value := make([]any, 0, len(rows))
	for _, row := range rows {
		value = append(value, row)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"@odata.context": insightsODataContext(r, eventType),
		"value":          value,
	})
}

func insightsGetEvent(w http.ResponseWriter, r *http.Request) {
	eventType := sim.PathParam(r, "eventType")
	eventID := sim.PathParam(r, "eventId")
	rows, ok := insightsEventRows(sim.PathParam(r, "appId"), eventType)
	if !ok {
		AzureError(w, "BadArgumentError",
			"The event type '"+eventType+"' is not one this API serves.", http.StatusBadRequest)
		return
	}
	value := []any{}
	for _, row := range rows {
		if id, _ := row["id"].(string); id == eventID {
			value = append(value, row)
			break
		}
	}
	// Application Insights answers an unknown event id with an empty value, not a
	// 404: the read filters a collection.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"@odata.context": insightsODataContext(r, eventType),
		"value":          value,
	})
}

func insightsODataContext(r *http.Request, eventType string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/v1/apps/" + sim.PathParam(r, "appId") +
		"/events/$metadata#" + eventType
}

// One entity set per event type.
func insightsEventsODataMetadata() string {
	types := make([]string, 0, len(insightsEventTables))
	for name := range insightsEventTables {
		types = append(types, name)
	}
	sort.Strings(types)

	var sets strings.Builder
	for _, name := range types {
		sets.WriteString(`      <EntitySet Name="` + name + `" EntityType="microsoft.insights.event"/>` + "\n")
	}
	return `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx">
  <edmx:DataServices>
    <Schema Namespace="microsoft.insights" xmlns="http://docs.oasis-open.org/odata/ns/edm">
      <EntityType Name="event">
        <Key><PropertyRef Name="id"/></Key>
        <Property Name="id" Type="Edm.String" Nullable="false"/>
        <Property Name="type" Type="Edm.String"/>
        <Property Name="count" Type="Edm.Int64"/>
        <Property Name="timestamp" Type="Edm.DateTimeOffset"/>
      </EntityType>
      <EntityContainer Name="Events">
` + sets.String() + `      </EntityContainer>
    </Schema>
  </edmx:DataServices>
</edmx:Edmx>
`
}

// An application's metrics are the tables its telemetry is in.
func insightsGetMetricsMetadata(w http.ResponseWriter, r *http.Request) {
	metrics := map[string]any{}
	names := make([]string, 0, len(insightsEventTables))
	for name := range insightsEventTables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		metrics[name+"/count"] = map[string]any{
			"displayName":           name + " count",
			"defaultAggregation":    "sum",
			"supportedAggregations": []any{"sum"},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"metrics":    metrics,
		"dimensions": map[string]any{},
	})
}

// The number of telemetry rows in the table the metric names.
func insightsMetricSegment(appID, metricID string) (map[string]any, bool) {
	// "traces/count" names the telemetry and the measure; a count aggregates as a
	// sum.
	eventType, measure, _ := strings.Cut(metricID, "/")
	if measure != "count" {
		return nil, false
	}
	const aggregation = "sum"
	rows, ok := insightsEventRows(appID, eventType)
	if !ok {
		return nil, false
	}
	now := time.Now().UTC()
	return map[string]any{
		"start":    now.Add(-time.Hour).Format(time.RFC3339),
		"end":      now.Format(time.RFC3339),
		metricID:   map[string]any{aggregation: len(rows)},
		"segments": []any{},
	}, true
}

func insightsGetMetric(w http.ResponseWriter, r *http.Request) {
	metricID := sim.PathParam(r, "metricId")
	segment, ok := insightsMetricSegment(sim.PathParam(r, "appId"), metricID)
	if !ok {
		AzureError(w, "BadArgumentError",
			"The metric '"+metricID+"' is not one this application reports.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": segment})
}

// Metrics_GetMultiple answers per request id.
func insightsGetMetrics(w http.ResponseWriter, r *http.Request) {
	var body []struct {
		ID         string `json:"id"`
		Parameters struct {
			MetricID string `json:"metricId"`
		} `json:"parameters"`
	}
	if err := sim.ReadJSON(r, &body); err != nil {
		AzureError(w, "BadArgumentError",
			"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	appID := sim.PathParam(r, "appId")
	results := make([]any, 0, len(body))
	for _, request := range body {
		segment, ok := insightsMetricSegment(appID, request.Parameters.MetricID)
		if !ok {
			results = append(results, map[string]any{
				"id":     request.ID,
				"status": http.StatusBadRequest,
				"body": map[string]any{"error": map[string]any{
					"code":    "BadArgumentError",
					"message": "The metric '" + request.Parameters.MetricID + "' is not one this application reports.",
				}},
			})
			continue
		}
		results = append(results, map[string]any{
			"id":     request.ID,
			"status": http.StatusOK,
			"body":   map[string]any{"value": segment},
		})
	}
	sim.WriteJSON(w, http.StatusOK, results)
}

// Application Insights' metadata shape (applications, tables, table groups,
// functions) is not Log Analytics': it has no workspaces member.
func insightsMetadataDoc(appID string) map[string]any {
	names := make([]string, 0, len(insightsEventTables))
	for _, table := range insightsEventTables {
		names = append(names, table)
	}
	sort.Strings(names)

	tables := make([]any, 0, len(names))
	for _, name := range names {
		columns := []any{}
		for _, column := range kqlTableSchemas[name] {
			columns = append(columns, map[string]any{
				"name": column.Name, "type": strings.ToLower(column.Type),
			})
		}
		tables = append(tables, map[string]any{
			"id": name, "name": name, "columns": columns,
		})
	}
	// The entry requires resourceId and region; the data plane addresses the
	// application by its app id.
	app := map[string]any{"id": appID, "name": appID, "region": "", "resourceId": ""}
	if azureAppInsightsComponents != nil {
		if c, ok := azureInsightsByApplicationID.Lookup(
			azureAppInsightsComponents, strings.ToLower(appID), azureInsightsApplicationIDKeys); ok {
			app["name"] = c.Name
			app["region"] = c.Location
			app["resourceId"] = c.ID
		}
	}
	return map[string]any{
		"applications": []any{app},
		"tables":       tables,
		"tableGroups":  []any{},
		"functions":    []any{},
	}
}
