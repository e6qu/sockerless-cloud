package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// monitor_tables.go serves a Log Analytics workspace's tables
// (Microsoft.OperationalInsights/workspaces/tables). Every workspace holds the
// Azure tables the simulator writes and queries (kqlTableSchemas); a
// customer creates a custom log table, whose name ends in _CL, with the
// columns a data collection rule's output stream writes; and a platform
// service creates the custom log tables it writes to when a resource first
// links the workspace, as Container Apps does for its console and system
// logs. A query resolves its table against the queried workspace's tables.

// LogAnalyticsTable is a workspace table as Tables_Get returns it. Only a
// customised Azure table or a custom log table is stored; an Azure table no
// one changed reads from its schema.
type LogAnalyticsTable struct {
	ID         string                      `json:"id"`
	Name       string                      `json:"name"`
	Type       string                      `json:"type"`
	Properties LogAnalyticsTableProperties `json:"properties"`
}

type LogAnalyticsTableProperties struct {
	RetentionInDays               int                     `json:"retentionInDays"`
	TotalRetentionInDays          int                     `json:"totalRetentionInDays"`
	ArchiveRetentionInDays        int                     `json:"archiveRetentionInDays"`
	Plan                          string                  `json:"plan"`
	LastPlanModifiedDate          string                  `json:"lastPlanModifiedDate,omitempty"`
	Schema                        LogAnalyticsTableSchema `json:"schema"`
	ProvisioningState             string                  `json:"provisioningState"`
	RetentionInDaysAsDefault      bool                    `json:"retentionInDaysAsDefault"`
	TotalRetentionInDaysAsDefault bool                    `json:"totalRetentionInDaysAsDefault"`
}

type LogAnalyticsTableSchema struct {
	Name            string                    `json:"name"`
	DisplayName     string                    `json:"displayName,omitempty"`
	Description     string                    `json:"description,omitempty"`
	Columns         []LogAnalyticsTableColumn `json:"columns"`
	StandardColumns []LogAnalyticsTableColumn `json:"standardColumns"`
	Source          string                    `json:"source"`
	TableType       string                    `json:"tableType"`
	TableSubType    string                    `json:"tableSubType"`
}

type LogAnalyticsTableColumn struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	DataTypeHint string `json:"dataTypeHint,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	Description  string `json:"description,omitempty"`
}

var logAnalyticsTables sim.Store[LogAnalyticsTable]

// logAnalyticsTablesByWorkspace finds a workspace's stored tables by the
// workspace's ARM id.
var logAnalyticsTablesByWorkspace sim.GenerationIndex[LogAnalyticsTable]

func logAnalyticsTableWorkspaceKeys(t LogAnalyticsTable) []string {
	ws, _, _ := strings.Cut(t.ID, "/tables/")
	return []string{strings.ToLower(ws)}
}

// The Tables API spells a column type the way the data collection rule
// spells it, except for dateTime; the query engine knows them as Kusto types.
var logAnalyticsColumnKQLTypes = map[string]string{
	"string": "string", "int": "int", "long": "long", "real": "real",
	"boolean": "bool", "datetime": "datetime", "guid": "string", "dynamic": "dynamic",
}

var kqlColumnTableTypes = map[string]string{
	"string": "string", "int": "int", "long": "long", "real": "real",
	"bool": "boolean", "datetime": "dateTime", "dynamic": "dynamic", "timespan": "string",
}

// logAnalyticsColumnKQLType reads a Tables API column type as the engine's.
func logAnalyticsColumnKQLType(t string) (string, bool) {
	k, ok := logAnalyticsColumnKQLTypes[strings.ToLower(t)]
	return k, ok
}

// kqlPlatformCustomLogTables are the custom log tables Container Apps creates
// in the workspace an environment links.
var kqlPlatformCustomLogTables = map[string][]Column{
	"ContainerAppConsoleLogs_CL": {
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: "ContainerGroupName_s", Type: "string"},
		{Name: "ContainerAppName_s", Type: "string"},
		{Name: "EnvironmentName_s", Type: "string"},
		{Name: "RevisionName_s", Type: "string"},
		{Name: "ContainerName_s", Type: "string"},
		{Name: "Log_s", Type: "string"},
		{Name: "Stream_s", Type: "string"},
	},
	"ContainerAppSystemLogs_CL": {
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: "ContainerAppName_s", Type: "string"},
		{Name: "JobName_s", Type: "string"},
		{Name: "EnvironmentName_s", Type: "string"},
		{Name: "RevisionName_s", Type: "string"},
		{Name: "ReplicaName_s", Type: "string"},
		{Name: "ExecutionName_s", Type: "string"},
		{Name: "Log_s", Type: "string"},
		{Name: "Reason_s", Type: "string"},
		{Name: "Type_s", Type: "string"},
		{Name: "EventSource_s", Type: "string"},
	},
}

func logAnalyticsTableID(workspaceID, table string) string {
	return workspaceID + "/tables/" + table
}

// logAnalyticsTableColumns converts engine columns to the Tables API's.
func logAnalyticsTableColumns(cols []Column) []LogAnalyticsTableColumn {
	out := make([]LogAnalyticsTableColumn, 0, len(cols))
	for _, c := range cols {
		out = append(out, LogAnalyticsTableColumn{Name: c.Name, Type: kqlColumnTableTypes[c.Type]})
	}
	return out
}

// logAnalyticsDefaultTable is a table as a workspace holds it before anyone
// changes it: its retention follows the workspace's.
func logAnalyticsDefaultTable(ws Workspace, name, tableType, subType, source string, cols []LogAnalyticsTableColumn, standard []LogAnalyticsTableColumn) LogAnalyticsTable {
	if cols == nil {
		cols = []LogAnalyticsTableColumn{}
	}
	if standard == nil {
		standard = []LogAnalyticsTableColumn{}
	}
	retention := ws.Properties.RetentionInDays
	return LogAnalyticsTable{
		ID:   logAnalyticsTableID(ws.ID, name),
		Name: name,
		Type: "Microsoft.OperationalInsights/workspaces/tables",
		Properties: LogAnalyticsTableProperties{
			RetentionInDays:               retention,
			TotalRetentionInDays:          retention,
			Plan:                          "Analytics",
			ProvisioningState:             "Succeeded",
			RetentionInDaysAsDefault:      true,
			TotalRetentionInDaysAsDefault: true,
			Schema: LogAnalyticsTableSchema{
				Name:            name,
				Columns:         cols,
				StandardColumns: standard,
				Source:          source,
				TableType:       tableType,
				TableSubType:    subType,
			},
		},
	}
}

// logAnalyticsWorkspaceTables lists a workspace's tables: the Azure tables,
// each as stored when someone changed it, and the custom log tables.
func logAnalyticsWorkspaceTables(ws Workspace) []LogAnalyticsTable {
	byName := map[string]LogAnalyticsTable{}
	for name, cols := range kqlTableSchemas {
		byName[strings.ToLower(name)] = logAnalyticsDefaultTable(ws, name, "Microsoft", "Any", "microsoft",
			nil, logAnalyticsTableColumns(cols))
	}
	for _, t := range logAnalyticsTablesByWorkspace.LookupAll(logAnalyticsTables, strings.ToLower(ws.ID), logAnalyticsTableWorkspaceKeys) {
		byName[strings.ToLower(t.Name)] = t
	}
	out := make([]LogAnalyticsTable, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// logAnalyticsWorkspaceTable reads one of a workspace's tables by name, as
// Log Analytics resolves table names, case-sensitively.
func logAnalyticsWorkspaceTable(ws Workspace, name string) (LogAnalyticsTable, bool) {
	if t, ok := logAnalyticsTables.Get(logAnalyticsTableID(ws.ID, name)); ok {
		return t, true
	}
	if cols, ok := kqlTableSchemas[name]; ok {
		return logAnalyticsDefaultTable(ws, name, "Microsoft", "Any", "microsoft", nil, logAnalyticsTableColumns(cols)), true
	}
	return LogAnalyticsTable{}, false
}

// logAnalyticsTableSchema is the engine schema of a workspace table: a custom
// log table's columns, or an Azure table's.
func logAnalyticsTableSchema(t LogAnalyticsTable) []Column {
	if t.Properties.Schema.TableType == "Microsoft" {
		return kqlTableSchemas[t.Name]
	}
	cols := make([]Column, 0, len(t.Properties.Schema.Columns)+1)
	for _, c := range t.Properties.Schema.Columns {
		typ, _ := logAnalyticsColumnKQLType(c.Type)
		cols = append(cols, Column{Name: c.Name, Type: typ})
	}
	return cols
}

// ensurePlatformCustomLogTables creates the custom log tables a platform
// service writes to in a workspace the first time a resource links it.
func ensurePlatformCustomLogTables(ws Workspace, names ...string) {
	for _, name := range names {
		id := logAnalyticsTableID(ws.ID, name)
		if _, ok := logAnalyticsTables.Get(id); ok {
			continue
		}
		cols := logAnalyticsTableColumns(kqlPlatformCustomLogTables[name])
		logAnalyticsTables.Put(id, logAnalyticsDefaultTable(ws, name, "CustomLog", "Classic", "customer", cols, nil))
	}
}

// logAnalyticsWorkspaceByID reads a workspace by its ARM id, whatever the
// casing of the id's segments.
func logAnalyticsWorkspaceByID(id string) (Workspace, bool) {
	if azureMonitorWorkspaces == nil {
		return Workspace{}, false
	}
	sub, rg, name, ok := parseWorkspaceResourceID(id)
	if !ok {
		return Workspace{}, false
	}
	return azureMonitorWorkspaces.Get(fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name))
}

// parseWorkspaceResourceID splits a workspace's ARM id.
func parseWorkspaceResourceID(id string) (sub, rg, name string, ok bool) {
	parts := strings.Split(strings.Trim(id, "/"), "/")
	if len(parts) != 8 || !strings.EqualFold(parts[0], "subscriptions") || !strings.EqualFold(parts[2], "resourceGroups") ||
		!strings.EqualFold(parts[4], "providers") || !strings.EqualFold(parts[5], "Microsoft.OperationalInsights") ||
		!strings.EqualFold(parts[6], "workspaces") {
		return "", "", "", false
	}
	return parts[1], parts[3], parts[7], true
}

func registerLogAnalyticsTables(srv *sim.Server, armBase string) {
	logAnalyticsTables = sim.MakeStore[LogAnalyticsTable](srv.DB(), "monitor_workspace_tables")
	base := armBase + "/workspaces/{workspaceName}/tables"

	workspace := func(w http.ResponseWriter, r *http.Request) (Workspace, bool) {
		sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workspaceName")
		ws, ok := azureMonitorWorkspaces.Get(fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.OperationalInsights/workspaces/%s' under resource group '%s' was not found.", name, rg)
		}
		return ws, ok
	}

	registerLogAnalyticsTableActions(srv, base, workspace)

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		ws, ok := workspace(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": logAnalyticsWorkspaceTables(ws)})
	})

	srv.HandleFunc("GET "+base+"/{tableName}", func(w http.ResponseWriter, r *http.Request) {
		ws, ok := workspace(w, r)
		if !ok {
			return
		}
		t, ok := logAnalyticsWorkspaceTable(ws, sim.PathParam(r, "tableName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The table '%s' was not found in workspace '%s'.", sim.PathParam(r, "tableName"), ws.Name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, t)
	})

	put := func(w http.ResponseWriter, r *http.Request) {
		ws, ok := workspace(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "tableName")
		var req struct {
			Properties struct {
				RetentionInDays      *int                     `json:"retentionInDays"`
				TotalRetentionInDays *int                     `json:"totalRetentionInDays"`
				Plan                 string                   `json:"plan"`
				Schema               *LogAnalyticsTableSchema `json:"schema"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		existing, exists := logAnalyticsWorkspaceTable(ws, name)
		table := existing
		switch {
		case exists && existing.Properties.Schema.TableType == "Microsoft":
			if req.Properties.Schema != nil && len(req.Properties.Schema.Columns) > 0 {
				AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
					"The schema of the Azure table '%s' cannot be changed; only a custom log table (name ending in _CL) takes custom columns.", name)
				return
			}
		case !strings.HasSuffix(name, "_CL"):
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The table name '%s' is not valid: a custom log table's name ends in _CL.", name)
			return
		default:
			if req.Properties.Schema == nil || len(req.Properties.Schema.Columns) == 0 {
				if !exists {
					AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
						"A custom log table needs schema.columns.")
					return
				}
				break
			}
			cols, err := logAnalyticsValidateColumns(req.Properties.Schema.Columns)
			if err != nil {
				AzureError(w, "InvalidParameter", err.Error(), http.StatusBadRequest)
				return
			}
			if !exists {
				table = logAnalyticsDefaultTable(ws, name, "CustomLog", "DataCollectionRuleBased", "customer", nil, []LogAnalyticsTableColumn{
					{Name: "TenantId", Type: "guid"}, {Name: "Type", Type: "string"},
				})
			}
			table.Properties.Schema.Columns = cols
			table.Properties.Schema.DisplayName = req.Properties.Schema.DisplayName
			table.Properties.Schema.Description = req.Properties.Schema.Description
		}
		// A read-modify-write client sends back the retention it read; a value
		// that only echoes a default keeps the default following its source.
		if v := req.Properties.RetentionInDays; v != nil && table.Properties.RetentionInDaysAsDefault && *v == table.Properties.RetentionInDays {
			req.Properties.RetentionInDays = nil
		}
		if v := req.Properties.TotalRetentionInDays; v != nil && table.Properties.TotalRetentionInDaysAsDefault && *v == table.Properties.TotalRetentionInDays {
			req.Properties.TotalRetentionInDays = nil
		}
		if v := req.Properties.RetentionInDays; v != nil {
			if *v == -1 {
				table.Properties.RetentionInDays = ws.Properties.RetentionInDays
				table.Properties.RetentionInDaysAsDefault = true
			} else if *v < 4 || *v > 730 {
				AzureError(w, "InvalidParameter", "retentionInDays must be between 4 and 730, or -1 for the workspace's retention.", http.StatusBadRequest)
				return
			} else {
				table.Properties.RetentionInDays = *v
				table.Properties.RetentionInDaysAsDefault = false
			}
			if table.Properties.TotalRetentionInDaysAsDefault {
				table.Properties.TotalRetentionInDays = table.Properties.RetentionInDays
			}
		}
		if v := req.Properties.TotalRetentionInDays; v != nil {
			if *v == -1 {
				table.Properties.TotalRetentionInDays = table.Properties.RetentionInDays
				table.Properties.TotalRetentionInDaysAsDefault = true
			} else if *v < table.Properties.RetentionInDays || *v > 4383 {
				AzureError(w, "InvalidParameter", "totalRetentionInDays must be between the table's retention and 4383, or -1 for the table's retention.", http.StatusBadRequest)
				return
			} else {
				table.Properties.TotalRetentionInDays = *v
				table.Properties.TotalRetentionInDaysAsDefault = false
			}
		}
		table.Properties.ArchiveRetentionInDays = table.Properties.TotalRetentionInDays - table.Properties.RetentionInDays
		if req.Properties.Plan != "" && req.Properties.Plan != table.Properties.Plan {
			if req.Properties.Plan != "Basic" && req.Properties.Plan != "Analytics" {
				AzureErrorf(w, "InvalidParameter", http.StatusBadRequest, "The plan '%s' is not one of Basic, Analytics.", req.Properties.Plan)
				return
			}
			table.Properties.Plan = req.Properties.Plan
			table.Properties.LastPlanModifiedDate = time.Now().UTC().Format(time.RFC3339)
		}
		table.Properties.ProvisioningState = "Succeeded"
		logAnalyticsTables.Put(table.ID, table)
		opID := issueAzureAsyncOperation(nil)
		opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.OperationalInsights",
			ws.Location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
		writeAzureAsyncCreateHeaders(w, opID, opURL, azureCurrentRequestURL(r))
		w.WriteHeader(http.StatusAccepted)
	}
	srv.HandleFunc("PUT "+base+"/{tableName}", put)
	srv.HandleFunc("PATCH "+base+"/{tableName}", put)

	srv.HandleFunc("DELETE "+base+"/{tableName}", func(w http.ResponseWriter, r *http.Request) {
		ws, ok := workspace(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "tableName")
		t, exists := logAnalyticsWorkspaceTable(ws, name)
		if !exists {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if t.Properties.Schema.TableType == "Microsoft" {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The Azure table '%s' cannot be deleted; only a custom log table can.", name)
			return
		}
		logAnalyticsTables.Delete(t.ID)
		dropLogRows(logRowsKey(ws.Properties.CustomerID, name))
		opID := issueAzureAsyncOperation(nil)
		opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.OperationalInsights",
			ws.Location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
		writeAzureAsyncCreateHeaders(w, opID, opURL, azureCurrentRequestURL(r))
		w.WriteHeader(http.StatusAccepted)
	})
}

// registerLogAnalyticsTableActions serves a table's migrate and cancelSearch.
func registerLogAnalyticsTableActions(srv *sim.Server, base string, workspace func(http.ResponseWriter, *http.Request) (Workspace, bool)) {
	// Tables_Migrate moves a classic custom log table onto data collection
	// rules, after which the Logs Ingestion API can write to it.
	srv.HandleFunc("POST "+base+"/{tableName}/migrate", func(w http.ResponseWriter, r *http.Request) {
		ws, ok := workspace(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "tableName")
		t, ok := logAnalyticsWorkspaceTable(ws, name)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The table '%s' was not found in workspace '%s'.", name, ws.Name)
			return
		}
		if t.Properties.Schema.TableType != "CustomLog" {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The table '%s' is not a custom log table, so it has nothing to migrate.", name)
			return
		}
		t.Properties.Schema.TableSubType = "DataCollectionRuleBased"
		logAnalyticsTables.Put(t.ID, t)
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("POST "+base+"/{tableName}/cancelSearch", func(w http.ResponseWriter, r *http.Request) {
		AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"The simulator runs no search jobs, so it has no search to cancel on table '%s'.", sim.PathParam(r, "tableName"))
	})
}

// logAnalyticsValidateColumns checks a custom log table's columns: unique
// names, a known type, and TimeGenerated as its datetime column.
func logAnalyticsValidateColumns(cols []LogAnalyticsTableColumn) ([]LogAnalyticsTableColumn, error) {
	seen := map[string]bool{}
	hasTime := false
	out := make([]LogAnalyticsTableColumn, 0, len(cols))
	for _, c := range cols {
		if c.Name == "" {
			return nil, fmt.Errorf("every column needs a name")
		}
		if seen[strings.ToLower(c.Name)] {
			return nil, fmt.Errorf("the column '%s' appears more than once", c.Name)
		}
		seen[strings.ToLower(c.Name)] = true
		if _, ok := logAnalyticsColumnKQLType(c.Type); !ok {
			return nil, fmt.Errorf("the column '%s' has the type '%s', which is not one of string, int, long, real, boolean, dateTime, guid, dynamic", c.Name, c.Type)
		}
		if c.Name == "TimeGenerated" {
			if !strings.EqualFold(c.Type, "datetime") {
				return nil, fmt.Errorf("the column 'TimeGenerated' must be of type dateTime")
			}
			hasTime = true
		}
		out = append(out, LogAnalyticsTableColumn{Name: c.Name, Type: c.Type, DataTypeHint: c.DataTypeHint,
			DisplayName: c.DisplayName, Description: c.Description})
	}
	if !hasTime {
		return nil, fmt.Errorf("a custom log table needs a TimeGenerated column of type dateTime")
	}
	return out, nil
}
