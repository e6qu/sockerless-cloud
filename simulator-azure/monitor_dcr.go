package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// monitor_dcr.go serves data collection rules (Microsoft.Insights/
// dataCollectionRules) and the Logs Ingestion API that uploads through them.
// An upload names a rule by its immutable id and one of the rule's declared
// streams; every data flow carrying that stream runs its transformKql over
// the rows and writes the result to the output table of each Log Analytics
// destination it names, and to nothing else.

type DataCollectionRule struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	Type       string                       `json:"type"`
	Location   string                       `json:"location"`
	Kind       string                       `json:"kind,omitempty"`
	Tags       map[string]string            `json:"tags,omitempty"`
	Identity   json.RawMessage              `json:"identity,omitempty"`
	Etag       string                       `json:"etag,omitempty"`
	Properties DataCollectionRuleProperties `json:"properties"`
}

type DataCollectionRuleProperties struct {
	Description              string                          `json:"description,omitempty"`
	ImmutableID              string                          `json:"immutableId"`
	DataCollectionEndpointID string                          `json:"dataCollectionEndpointId,omitempty"`
	Endpoints                *DataCollectionRuleEndpoints    `json:"endpoints,omitempty"`
	References               json.RawMessage                 `json:"references,omitempty"`
	AgentSettings            json.RawMessage                 `json:"agentSettings,omitempty"`
	StreamDeclarations       map[string]DCRStreamDeclaration `json:"streamDeclarations,omitempty"`
	DataSources              json.RawMessage                 `json:"dataSources,omitempty"`
	DirectDataSources        json.RawMessage                 `json:"directDataSources,omitempty"`
	Destinations             *DCRDestinations                `json:"destinations,omitempty"`
	DataFlows                []DCRDataFlow                   `json:"dataFlows,omitempty"`
	ProvisioningState        string                          `json:"provisioningState"`
}

type DataCollectionRuleEndpoints struct {
	LogsIngestion string `json:"logsIngestion,omitempty"`
}

type DCRStreamDeclaration struct {
	Columns []DCRColumn `json:"columns"`
}

type DCRColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type DCRDestinations struct {
	LogAnalytics        []DCRLogAnalyticsDestination `json:"logAnalytics,omitempty"`
	MonitoringAccounts  json.RawMessage              `json:"monitoringAccounts,omitempty"`
	AzureMonitorMetrics json.RawMessage              `json:"azureMonitorMetrics,omitempty"`
	EventHubs           json.RawMessage              `json:"eventHubs,omitempty"`
	EventHubsDirect     json.RawMessage              `json:"eventHubsDirect,omitempty"`
	StorageBlobsDirect  json.RawMessage              `json:"storageBlobsDirect,omitempty"`
	StorageTablesDirect json.RawMessage              `json:"storageTablesDirect,omitempty"`
	StorageAccounts     json.RawMessage              `json:"storageAccounts,omitempty"`
	MicrosoftFabric     json.RawMessage              `json:"microsoftFabric,omitempty"`
	AzureDataExplorer   json.RawMessage              `json:"azureDataExplorer,omitempty"`
}

type DCRLogAnalyticsDestination struct {
	WorkspaceResourceID string `json:"workspaceResourceId"`
	WorkspaceID         string `json:"workspaceId,omitempty"`
	Name                string `json:"name"`
}

type DCRDataFlow struct {
	Streams          []string `json:"streams"`
	Destinations     []string `json:"destinations"`
	TransformKql     string   `json:"transformKql,omitempty"`
	OutputStream     string   `json:"outputStream,omitempty"`
	BuiltInTransform string   `json:"builtInTransform,omitempty"`
	CaptureOverflow  *bool    `json:"captureOverflow,omitempty"`
}

var dataCollectionRules sim.Store[DataCollectionRule]

// dataCollectionRulesByImmutableID finds the rule an upload names.
var dataCollectionRulesByImmutableID sim.GenerationIndex[DataCollectionRule]

func dataCollectionRuleImmutableIDKeys(r DataCollectionRule) []string {
	return []string{strings.ToLower(r.Properties.ImmutableID)}
}

// dcrColumnKQLTypes maps a stream column type to the query engine's.
var dcrColumnKQLTypes = map[string]string{
	"string": "string", "int": "int", "long": "long", "real": "real",
	"boolean": "bool", "datetime": "datetime", "dynamic": "dynamic",
}

// dcrInvalid is a rule the service refuses, with what is wrong with it.
type dcrInvalid string

func (e dcrInvalid) Error() string { return string(e) }

func dcrInvalidf(format string, args ...any) error { return dcrInvalid(fmt.Sprintf(format, args...)) }

// dcrStreamColumns reads a declared stream's columns as the engine's schema.
func dcrStreamColumns(decl DCRStreamDeclaration) []Column {
	cols := make([]Column, 0, len(decl.Columns))
	for _, c := range decl.Columns {
		cols = append(cols, Column{Name: c.Name, Type: dcrColumnKQLTypes[strings.ToLower(c.Type)]})
	}
	return cols
}

// dcrOutputTable names the table a data flow writes a stream to: the
// outputStream's, or the input stream's own when the flow names none.
func dcrOutputTable(flow DCRDataFlow, stream string) (string, error) {
	out := flow.OutputStream
	if out == "" {
		out = stream
	}
	if name, ok := strings.CutPrefix(out, "Custom-"); ok {
		return name, nil
	}
	if name, ok := strings.CutPrefix(out, "Microsoft-"); ok {
		return name, nil
	}
	return "", dcrInvalidf("The output stream '%s' is not valid: it starts with Custom- or Microsoft-.", out)
}

// dcrTransform parses and binds a flow's transformKql against its input
// stream, returning the query and the schema it produces.
func dcrTransform(kql string, input []Column) (kqlQuery, []Column, *kqlError) {
	if strings.TrimSpace(kql) == "" {
		kql = "source"
	}
	parsed, err := parseKQL(kql)
	if err != nil {
		return kqlQuery{}, nil, err
	}
	if parsed.table != "source" {
		return kqlQuery{}, nil, kqlSemanticError("SEM0100",
			fmt.Sprintf("Failed to resolve table or column expression named '%s'", parsed.table))
	}
	set := kqlResultSet{columns: input}
	binder := &kqlBinder{src: kql, now: time.Now().UTC()}
	for _, op := range parsed.ops {
		if set, err = binder.apply(op, set); err != nil {
			return kqlQuery{}, nil, err
		}
	}
	return parsed, set.columns, nil
}

// validateDataCollectionRule checks a rule the way the service does before
// accepting it, and fills in each Log Analytics destination's workspace id.
func validateDataCollectionRule(rule *DataCollectionRule) error {
	p := &rule.Properties
	if p.DataCollectionEndpointID != "" {
		if _, ok := dataCollectionEndpointByID(p.DataCollectionEndpointID); !ok {
			return dcrInvalidf("The data collection endpoint '%s' was not found.", p.DataCollectionEndpointID)
		}
	}
	for name, decl := range p.StreamDeclarations {
		if !strings.HasPrefix(name, "Custom-") {
			return dcrInvalidf("The stream declaration '%s' is not valid: a declared stream's name starts with Custom-.", name)
		}
		if len(decl.Columns) == 0 {
			return dcrInvalidf("The stream declaration '%s' declares no columns.", name)
		}
		seen := map[string]bool{}
		for _, c := range decl.Columns {
			if _, ok := dcrColumnKQLTypes[strings.ToLower(c.Type)]; !ok {
				return dcrInvalidf("The column '%s' of stream '%s' has the type '%s', which is not one of string, int, long, real, boolean, datetime, dynamic.", c.Name, name, c.Type)
			}
			if c.Name == "" || seen[c.Name] {
				return dcrInvalidf("The stream declaration '%s' has an unnamed or repeated column.", name)
			}
			seen[c.Name] = true
		}
	}
	workspaces := map[string]Workspace{}
	known := map[string]bool{}
	if p.Destinations != nil {
		for i := range p.Destinations.LogAnalytics {
			d := &p.Destinations.LogAnalytics[i]
			if d.Name == "" {
				return dcrInvalidf("A Log Analytics destination needs a name.")
			}
			if known[d.Name] {
				return dcrInvalidf("The destination name '%s' is used more than once.", d.Name)
			}
			ws, ok := logAnalyticsWorkspaceByID(d.WorkspaceResourceID)
			if !ok {
				return dcrInvalidf("The Log Analytics workspace '%s' of destination '%s' was not found.", d.WorkspaceResourceID, d.Name)
			}
			d.WorkspaceID = ws.Properties.CustomerID
			workspaces[d.Name] = ws
			known[d.Name] = true
		}
		for _, raw := range []json.RawMessage{p.Destinations.MonitoringAccounts, p.Destinations.EventHubs,
			p.Destinations.EventHubsDirect, p.Destinations.StorageBlobsDirect, p.Destinations.StorageTablesDirect,
			p.Destinations.StorageAccounts, p.Destinations.MicrosoftFabric, p.Destinations.AzureDataExplorer} {
			var named []struct {
				Name string `json:"name"`
			}
			if len(raw) > 0 && json.Unmarshal(raw, &named) == nil {
				for _, n := range named {
					known[n.Name] = true
				}
			}
		}
		if len(p.Destinations.AzureMonitorMetrics) > 0 {
			var named struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(p.Destinations.AzureMonitorMetrics, &named) == nil {
				known[named.Name] = true
			}
		}
	}
	for i, flow := range p.DataFlows {
		if len(flow.Streams) == 0 || len(flow.Destinations) == 0 {
			return dcrInvalidf("Data flow %d names no stream or no destination.", i)
		}
		for _, dest := range flow.Destinations {
			if !known[dest] {
				return dcrInvalidf("Data flow %d sends to the destination '%s', which the rule does not declare.", i, dest)
			}
		}
		for _, stream := range flow.Streams {
			decl, declared := p.StreamDeclarations[stream]
			if !declared {
				if strings.HasPrefix(stream, "Custom-") {
					return dcrInvalidf("Data flow %d names the stream '%s', which the rule does not declare.", i, stream)
				}
				continue
			}
			_, outCols, kerr := dcrTransform(flow.TransformKql, dcrStreamColumns(decl))
			if kerr != nil {
				return dcrInvalidf("The transformKql of data flow %d is not valid: %s", i, kerr.message)
			}
			table, err := dcrOutputTable(flow, stream)
			if err != nil {
				return err
			}
			for _, dest := range flow.Destinations {
				ws, ok := workspaces[dest]
				if !ok {
					continue
				}
				t, ok := logAnalyticsWorkspaceTable(ws, table)
				if !ok {
					return dcrInvalidf("The table '%s' of output stream '%s' does not exist in the workspace of destination '%s'.", table, flow.OutputStream, dest)
				}
				if t.Properties.Schema.TableSubType == "Classic" {
					return dcrInvalidf("The table '%s' is a classic custom log table; migrate it to data collection rules before a rule writes to it.", table)
				}
				schema := logAnalyticsTableSchema(t)
				for _, c := range outCols {
					if _, ok := kqlSchemaColumn(schema, c.Name); !ok {
						return dcrInvalidf("The transform output column '%s' of data flow %d is not a column of the table '%s'.", c.Name, i, table)
					}
					if c.Name == "TimeGenerated" && c.Type != "datetime" {
						return dcrInvalidf("The transform output column 'TimeGenerated' of data flow %d must be a datetime.", i)
					}
				}
				if _, ok := kqlSchemaColumn(outCols, "TimeGenerated"); !ok {
					return dcrInvalidf("The transform output of data flow %d has no TimeGenerated column.", i)
				}
			}
		}
	}
	return nil
}

// kqlSchemaColumn finds a column of a schema by name.
func kqlSchemaColumn(cols []Column, name string) (Column, bool) {
	for _, c := range cols {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func newDCRImmutableID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "dcr-" + hex.EncodeToString(b)
}

func registerDataCollectionRules(srv *sim.Server) {
	dataCollectionRules = sim.MakeStore[DataCollectionRule](srv.DB(), "monitor_data_collection_rules")
	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionRules"
	ruleID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/dataCollectionRules/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "dataCollectionRuleName"))
	}
	notFound := func(w http.ResponseWriter, r *http.Request) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Insights/dataCollectionRules/%s' under resource group '%s' was not found.",
			sim.PathParam(r, "dataCollectionRuleName"), sim.PathParam(r, "resourceGroupName"))
	}
	wire := func(r *http.Request, rule DataCollectionRule) DataCollectionRule {
		if strings.EqualFold(rule.Kind, "Direct") {
			rule.Properties.Endpoints = &DataCollectionRuleEndpoints{LogsIngestion: azureRequestScheme(r) + "://" + r.Host}
		}
		return rule
	}

	srv.HandleFunc("PUT "+base+"/{dataCollectionRuleName}", func(w http.ResponseWriter, r *http.Request) {
		var req DataCollectionRule
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidPayload", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Location == "" {
			AzureError(w, "InvalidPayload", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		id := ruleID(r)
		existing, exists := dataCollectionRules.Get(id)
		rule := DataCollectionRule{
			ID:         id,
			Name:       sim.PathParam(r, "dataCollectionRuleName"),
			Type:       "Microsoft.Insights/dataCollectionRules",
			Location:   req.Location,
			Kind:       req.Kind,
			Tags:       req.Tags,
			Identity:   req.Identity,
			Etag:       `"` + sim.NewUUID() + `"`,
			Properties: req.Properties,
		}
		rule.Properties.ImmutableID = newDCRImmutableID()
		if exists {
			rule.Properties.ImmutableID = existing.Properties.ImmutableID
		}
		rule.Properties.Endpoints = nil
		rule.Properties.ProvisioningState = "Succeeded"
		if err := validateDataCollectionRule(&rule); err != nil {
			AzureError(w, "InvalidPayload", err.Error(), http.StatusBadRequest)
			return
		}
		dataCollectionRules.Put(id, rule)
		status := http.StatusCreated
		if exists {
			status = http.StatusOK
		}
		sim.WriteJSON(w, status, wire(r, rule))
	})

	srv.HandleFunc("GET "+base+"/{dataCollectionRuleName}", func(w http.ResponseWriter, r *http.Request) {
		rule, ok := dataCollectionRules.Get(ruleID(r))
		if !ok {
			notFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, wire(r, rule))
	})

	srv.HandleFunc("PATCH "+base+"/{dataCollectionRuleName}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tags     map[string]string `json:"tags"`
			Identity json.RawMessage   `json:"identity"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidPayload", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var out DataCollectionRule
		ok := dataCollectionRules.Update(ruleID(r), func(rule *DataCollectionRule) {
			if req.Tags != nil {
				rule.Tags = req.Tags
			}
			if len(req.Identity) > 0 {
				rule.Identity = req.Identity
			}
			rule.Etag = `"` + sim.NewUUID() + `"`
			out = *rule
		})
		if !ok {
			notFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, wire(r, out))
	})

	srv.HandleFunc("DELETE "+base+"/{dataCollectionRuleName}", func(w http.ResponseWriter, r *http.Request) {
		if dataCollectionRules.Delete(ruleID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	list := func(w http.ResponseWriter, r *http.Request, prefix string) {
		prefix = strings.ToLower(prefix)
		rules := dataCollectionRules.Filter(func(rule DataCollectionRule) bool {
			return strings.HasPrefix(strings.ToLower(rule.ID), prefix)
		})
		sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
		out := make([]DataCollectionRule, 0, len(rules))
		for _, rule := range rules {
			out = append(out, wire(r, rule))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		list(w, r, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/dataCollectionRules/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")))
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/dataCollectionRules", func(w http.ResponseWriter, r *http.Request) {
		list(w, r, fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId")))
	})

	srv.HandleFunc("POST /dataCollectionRules/{ruleId}/streams/{stream}", handleLogsIngestionUpload)

	// Data collection rule associations attach a rule to a resource whose
	// Azure Monitor Agent then collects by it. The simulator runs no agent, so
	// it declares every association operation unserved.
	associationsUnserved := func(w http.ResponseWriter, _ *http.Request) {
		AzureError(w, "NotImplemented",
			"The simulator runs no Azure Monitor Agent, so data collection rule associations are not implemented.",
			http.StatusNotImplemented)
	}
	srv.HandleFunc("GET "+base+"/{dataCollectionRuleName}/associations", associationsUnserved)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints/{dataCollectionEndpointName}/associations", associationsUnserved)
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(strings.ToLower(r.URL.Path), "/providers/microsoft.insights/datacollectionruleassociations") {
				associationsUnserved(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

// dataCollectionEndpointByID reads an endpoint by its ARM id, whatever the
// casing of the id's segments.
func dataCollectionEndpointByID(id string) (DataCollectionEndpoint, bool) {
	parts := strings.Split(strings.Trim(id, "/"), "/")
	if len(parts) != 8 || !strings.EqualFold(parts[6], "dataCollectionEndpoints") {
		return DataCollectionEndpoint{}, false
	}
	return dataCollectionEndpoints.Get(fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/dataCollectionEndpoints/%s", parts[1], parts[3], parts[7]))
}

// logsIngestionMaxBody is the API's limit on one call's body.
const logsIngestionMaxBody = 1 << 20

func logsIngestionError(w http.ResponseWriter, status int, code, format string, args ...any) {
	w.Header().Set("x-ms-error-code", code)
	sim.WriteJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": fmt.Sprintf(format, args...)}})
}

// handleLogsIngestionUpload is the Logs Ingestion API's Upload.
func handleLogsIngestionUpload(w http.ResponseWriter, r *http.Request) {
	immutableID, stream := sim.PathParam(r, "ruleId"), sim.PathParam(r, "stream")
	rule, ok := dataCollectionRulesByImmutableID.Lookup(dataCollectionRules, strings.ToLower(immutableID), dataCollectionRuleImmutableIDKeys)
	if !ok {
		logsIngestionError(w, http.StatusNotFound, "NotFound", "Data collection rule with immutable Id '%s' not found.", immutableID)
		return
	}
	decl, declared := rule.Properties.StreamDeclarations[stream]
	if !declared {
		logsIngestionError(w, http.StatusBadRequest, "InvalidStream",
			"The stream '%s' is not declared in the data collection rule '%s'.", stream, immutableID)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, logsIngestionMaxBody+1))
	if err != nil {
		logsIngestionError(w, http.StatusBadRequest, "InvalidPayload", "%v", err)
		return
	}
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			logsIngestionError(w, http.StatusBadRequest, "InvalidContentEncoding", "The body is not valid gzip: %v", err)
			return
		}
		body, err = io.ReadAll(io.LimitReader(zr, 64*logsIngestionMaxBody))
		if err != nil {
			logsIngestionError(w, http.StatusBadRequest, "InvalidContentEncoding", "The body is not valid gzip: %v", err)
			return
		}
	} else if len(body) > logsIngestionMaxBody {
		logsIngestionError(w, http.StatusRequestEntityTooLarge, "ContentLengthLimitExceeded",
			"The request body exceeds the limit of %d bytes.", logsIngestionMaxBody)
		return
	}
	var entries []map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&entries); err != nil {
		logsIngestionError(w, http.StatusBadRequest, "InvalidPayload", "The body is not a JSON array of objects: %v", err)
		return
	}
	input := dcrStreamColumns(decl)
	rows := make([][]any, 0, len(entries))
	for _, e := range entries {
		row := make([]any, len(input))
		for i, c := range input {
			row[i] = dcrCell(e[c.Name], c.Type)
		}
		rows = append(rows, row)
	}
	workspaces := map[string]Workspace{}
	if rule.Properties.Destinations != nil {
		for _, d := range rule.Properties.Destinations.LogAnalytics {
			if ws, ok := logAnalyticsWorkspaceByID(d.WorkspaceResourceID); ok {
				workspaces[d.Name] = ws
			}
		}
	}
	for _, flow := range rule.Properties.DataFlows {
		if !containsString(flow.Streams, stream) {
			continue
		}
		parsed, _, kerr := dcrTransform(flow.TransformKql, input)
		if kerr != nil {
			logsIngestionError(w, http.StatusBadRequest, "InvalidTransform", "%s", kerr.message)
			return
		}
		set := kqlResultSet{columns: input, rows: rows}
		binder := &kqlBinder{src: flow.TransformKql, now: time.Now().UTC()}
		for _, op := range parsed.ops {
			if set, kerr = binder.apply(op, set); kerr != nil {
				logsIngestionError(w, http.StatusBadRequest, "InvalidTransform", "%s", kerr.message)
				return
			}
		}
		table, err := dcrOutputTable(flow, stream)
		if err != nil {
			continue
		}
		for _, dest := range flow.Destinations {
			ws, ok := workspaces[dest]
			if !ok {
				continue
			}
			for _, row := range set.rows {
				stored := monitorLogRow{}
				for i, c := range set.columns {
					if v, ok := kqlStoredValue(row[i]); ok {
						stored[c.Name] = v
					}
				}
				appendWorkspaceLogRow(ws.Properties.CustomerID, table, stored)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// dcrCell reads one uploaded value as a cell of its declared column type; a
// value of the wrong shape is null.
func dcrCell(v any, typ string) any {
	if v == nil {
		if typ == "string" {
			return ""
		}
		return nil
	}
	switch typ {
	case "string":
		if s, ok := v.(string); ok {
			return s
		}
		if n, ok := v.(json.Number); ok {
			return n.String()
		}
		b, _ := json.Marshal(v)
		return string(b)
	case "int", "long":
		switch v := v.(type) {
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n
			}
			if f, err := v.Float64(); err == nil {
				return int64(f)
			}
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
	case "real":
		switch v := v.(type) {
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		}
	case "bool":
		switch v := v.(type) {
		case bool:
			return v
		case string:
			if b, err := strconv.ParseBool(v); err == nil {
				return b
			}
		}
	case "datetime":
		if s, ok := v.(string); ok {
			if t, ok := parseKQLDatetime(s); ok {
				return t
			}
		}
	case "dynamic":
		return kqlDynamicFromJSON(v)
	}
	return nil
}

// kqlStoredValue writes a cell as the stored row keeps it; a null cell is
// absent.
func kqlStoredValue(v any) (string, bool) {
	switch v := v.(type) {
	case nil:
		return "", false
	case map[string]any, []any:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return kqlToString(v), true
}

// DataCollectionEndpoint is a data collection endpoint: the Logs Ingestion
// endpoint a data collection rule that names it is uploaded through.
type DataCollectionEndpoint struct {
	ID         string                           `json:"id"`
	Name       string                           `json:"name"`
	Type       string                           `json:"type"`
	Location   string                           `json:"location"`
	Kind       string                           `json:"kind,omitempty"`
	Tags       map[string]string                `json:"tags,omitempty"`
	Identity   json.RawMessage                  `json:"identity,omitempty"`
	Etag       string                           `json:"etag,omitempty"`
	Properties DataCollectionEndpointProperties `json:"properties"`
}

type DataCollectionEndpointProperties struct {
	Description       string                      `json:"description,omitempty"`
	ImmutableID       string                      `json:"immutableId"`
	LogsIngestion     *DataCollectionEndpointURL  `json:"logsIngestion,omitempty"`
	NetworkAcls       *DataCollectionEndpointACLs `json:"networkAcls,omitempty"`
	ProvisioningState string                      `json:"provisioningState"`
}

type DataCollectionEndpointURL struct {
	Endpoint string `json:"endpoint"`
}

type DataCollectionEndpointACLs struct {
	PublicNetworkAccess string `json:"publicNetworkAccess,omitempty"`
}

var dataCollectionEndpoints sim.Store[DataCollectionEndpoint]

func registerDataCollectionEndpoints(srv *sim.Server) {
	dataCollectionEndpoints = sim.MakeStore[DataCollectionEndpoint](srv.DB(), "monitor_data_collection_endpoints")
	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints"
	endpointID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/dataCollectionEndpoints/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "dataCollectionEndpointName"))
	}
	notFound := func(w http.ResponseWriter, r *http.Request) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Insights/dataCollectionEndpoints/%s' under resource group '%s' was not found.",
			sim.PathParam(r, "dataCollectionEndpointName"), sim.PathParam(r, "resourceGroupName"))
	}
	// The Logs Ingestion API is served beside Azure Resource Manager, so an
	// endpoint's logs ingestion URL is the host the request reached.
	wire := func(r *http.Request, dce DataCollectionEndpoint) DataCollectionEndpoint {
		dce.Properties.LogsIngestion = &DataCollectionEndpointURL{Endpoint: azureRequestScheme(r) + "://" + r.Host}
		return dce
	}

	srv.HandleFunc("PUT "+base+"/{dataCollectionEndpointName}", func(w http.ResponseWriter, r *http.Request) {
		var req DataCollectionEndpoint
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidPayload", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Location == "" {
			AzureError(w, "InvalidPayload", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		id := endpointID(r)
		existing, exists := dataCollectionEndpoints.Get(id)
		dce := DataCollectionEndpoint{
			ID:       id,
			Name:     sim.PathParam(r, "dataCollectionEndpointName"),
			Type:     "Microsoft.Insights/dataCollectionEndpoints",
			Location: req.Location,
			Kind:     req.Kind,
			Tags:     req.Tags,
			Identity: req.Identity,
			Etag:     `"` + sim.NewUUID() + `"`,
			Properties: DataCollectionEndpointProperties{
				Description:       req.Properties.Description,
				ImmutableID:       "dce-" + newDCRImmutableID()[len("dcr-"):],
				NetworkAcls:       req.Properties.NetworkAcls,
				ProvisioningState: "Succeeded",
			},
		}
		if dce.Properties.NetworkAcls == nil {
			dce.Properties.NetworkAcls = &DataCollectionEndpointACLs{PublicNetworkAccess: "Enabled"}
		}
		if exists {
			dce.Properties.ImmutableID = existing.Properties.ImmutableID
		}
		dataCollectionEndpoints.Put(id, dce)
		status := http.StatusCreated
		if exists {
			status = http.StatusOK
		}
		sim.WriteJSON(w, status, wire(r, dce))
	})
	srv.HandleFunc("GET "+base+"/{dataCollectionEndpointName}", func(w http.ResponseWriter, r *http.Request) {
		dce, ok := dataCollectionEndpoints.Get(endpointID(r))
		if !ok {
			notFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, wire(r, dce))
	})
	srv.HandleFunc("PATCH "+base+"/{dataCollectionEndpointName}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tags     map[string]string `json:"tags"`
			Identity json.RawMessage   `json:"identity"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidPayload", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var out DataCollectionEndpoint
		ok := dataCollectionEndpoints.Update(endpointID(r), func(dce *DataCollectionEndpoint) {
			if req.Tags != nil {
				dce.Tags = req.Tags
			}
			if len(req.Identity) > 0 {
				dce.Identity = req.Identity
			}
			dce.Etag = `"` + sim.NewUUID() + `"`
			out = *dce
		})
		if !ok {
			notFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, wire(r, out))
	})
	srv.HandleFunc("DELETE "+base+"/{dataCollectionEndpointName}", func(w http.ResponseWriter, r *http.Request) {
		if dataCollectionEndpoints.Delete(endpointID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	list := func(w http.ResponseWriter, r *http.Request, prefix string) {
		prefix = strings.ToLower(prefix)
		dces := dataCollectionEndpoints.Filter(func(dce DataCollectionEndpoint) bool {
			return strings.HasPrefix(strings.ToLower(dce.ID), prefix)
		})
		sort.Slice(dces, func(i, j int) bool { return dces[i].ID < dces[j].ID })
		out := make([]DataCollectionEndpoint, 0, len(dces))
		for _, dce := range dces {
			out = append(out, wire(r, dce))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		list(w, r, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/dataCollectionEndpoints/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")))
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/dataCollectionEndpoints", func(w http.ResponseWriter, r *http.Request) {
		list(w, r, fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId")))
	})
}
