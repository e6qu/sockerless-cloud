package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Application Insights data plane: what an application's telemetry says.
//
// A component's telemetry is the log store the workload writes into, which is
// the same store Log Analytics queries — Application Insights is a view onto
// it, addressed by the component's app id rather than a workspace id. So every
// read here goes through the one query engine, and the answers move when the
// application writes.
//
// The event types are the tables Application Insights groups telemetry into,
// and an event read is a query over the table its type names. A metric is that
// same table aggregated: a count of the rows in the window asked about, which
// is what the simulator can measure without inventing a series it never
// sampled.

// insightsEventTables maps each event type the document declares to the table
// its telemetry lands in. The `$all` type spans them, which is why it maps to
// nothing of its own.
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

// registerInsightsDataPlane mounts the app-scoped reads.
func registerInsightsDataPlane(srv *sim.Server) {
	// Query_Execute and Query_Get — the same engine Log Analytics queries with,
	// addressed by app id instead of workspace id.
	srv.HandleFunc("POST /v1/apps/{appId}/query", func(w http.ResponseWriter, r *http.Request) {
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
		sim.WriteJSON(w, http.StatusOK, runKQLQuery(sim.PathParam(r, "appId"), req.Query))
	})
	srv.HandleFunc("GET /v1/apps/{appId}/query", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "" {
			AzureError(w, "BadArgumentError", "The 'query' parameter is required.", http.StatusBadRequest)
			return
		}
		sim.WriteJSON(w, http.StatusOK, runKQLQuery(sim.PathParam(r, "appId"), query))
	})

	// Metadata_Get and Metadata_Post — the schema the application's telemetry
	// can be queried against.
	metadata := func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, insightsMetadataDoc(sim.PathParam(r, "appId")))
	}
	srv.HandleFunc("GET /v1/apps/{appId}/metadata", metadata)
	srv.HandleFunc("POST /v1/apps/{appId}/metadata", metadata)

	// Events_GetOdataMetadata — the OData description of the events surface.
	// The document is the event types this API serves, which is a fact about
	// the API rather than about any application's data.
	srv.HandleFunc("GET /v1/apps/{appId}/events/$metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(insightsEventsODataMetadata()))
	})

	// Events_GetByType and Events_Get.
	srv.HandleFunc("GET /v1/apps/{appId}/events/{eventType}", insightsGetEventsByType)
	srv.HandleFunc("GET /v1/apps/{appId}/events/{eventType}/{eventId}", insightsGetEvent)

	// Metrics_GetMetadata, Metrics_Get and Metrics_GetMultiple.
	srv.HandleFunc("GET /v1/apps/{appId}/metrics/metadata", insightsGetMetricsMetadata)
	// A metric id carries a slash — "requests/count", "client/processingDuration"
	// — so the tail is greedy. The literal metrics/metadata sibling is the more
	// specific pattern and still wins.
	srv.HandleFunc("GET /v1/apps/{appId}/metrics/{metricId...}", insightsGetMetric)
	srv.HandleFunc("POST /v1/apps/{appId}/metrics", insightsGetMetrics)
}

// insightsEventRows reads the telemetry rows behind one event type. `$all`
// spans every type, which is what makes it the one type with no table of its
// own.
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
	result := runKQLQuery(appID, table)
	var rows []map[string]any
	for _, t := range result.Tables {
		for _, row := range t.Rows {
			rows = append(rows, insightsEventDoc(eventType, t.Columns, row))
		}
	}
	return rows, true
}

// insightsEventDoc renders one telemetry row as an event. The members the
// document declares that the row does not carry are left out rather than
// zero-filled: an event with no session did not have one.
func insightsEventDoc(eventType string, columns []Column, row []any) map[string]any {
	doc := map[string]any{
		"type":  eventType,
		"count": 1,
	}
	// Only the members the row actually identifies. A table's columns are not
	// the application's custom dimensions — customDimensions is the property
	// bag a caller attached to its own telemetry — so putting the columns there
	// would both misname them and, since the document types each dimension as
	// an object, put strings where objects belong.
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
		// Application Insights identifies an event by an id the ingestion
		// stamped. A row that carries none is identified by the timestamp and
		// type it does carry, which is what addresses it uniquely here.
		timestamp, _ := doc["timestamp"].(string)
		doc["id"] = eventType + ":" + timestamp
	}
	return doc
}

// insightsGetEventsByType — Events_GetByType.
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

// insightsGetEvent — Events_Get. One event of a type, addressed by its id.
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
	// Application Insights answers an unknown event id with an empty value
	// rather than a 404: the read is a filtered collection, and an id nothing
	// matches filters everything out.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"@odata.context": insightsODataContext(r, eventType),
		"value":          value,
	})
}

// insightsODataContext is the metadata address the events surface describes
// itself with, which is the one this API serves.
func insightsODataContext(r *http.Request, eventType string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/v1/apps/" + sim.PathParam(r, "appId") +
		"/events/$metadata#" + eventType
}

// insightsEventsODataMetadata is the OData description of the events surface:
// one entity set per event type this API serves.
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

// insightsGetMetricsMetadata — Metrics_GetMetadata. The metrics an application
// has are the tables its telemetry is in, because a metric here is that
// telemetry counted.
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

// insightsMetricSegment measures one metric: the number of telemetry rows the
// application wrote into the table the metric names. A metric the simulator
// samples no series for is counted, not invented.
func insightsMetricSegment(appID, metricID string) (map[string]any, bool) {
	// A metric id names the telemetry and what is measured about it —
	// "traces/count" — and the aggregation is how the measurements over the
	// window are combined, which for a count is a sum.
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

// insightsGetMetric — Metrics_Get.
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

// insightsGetMetrics — Metrics_GetMultiple. The batch answers per request id,
// so a caller can tell which result is which.
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

// insightsMetadataDoc describes what an application's telemetry can be queried
// against. Application Insights declares its own metadata shape — applications,
// tables, table groups and functions — which is not the Log Analytics one: that
// carries a workspaces member this document has no room for.
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
	// The application this metadata is about. Its resource id and region are
	// both required members of the entry and both are the component's own: the
	// data plane addresses it by the application id the component records.
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
