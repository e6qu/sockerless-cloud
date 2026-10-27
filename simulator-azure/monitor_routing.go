package main

import (
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A workspace keeps its rows under its customer id, so one workspace's query
// never reads another's. Rows reach a workspace only through what names it: a
// Container Apps environment's appLogsConfiguration, a diagnostic setting, a
// workspace-based Application Insights component, or a data collection rule's
// destination. A classic Application Insights component keeps its own store.

func logRowsKey(customerID, table string) string {
	return strings.ToLower(customerID) + ":" + table
}

func appLogRowsKey(appID, table string) string {
	return "app/" + strings.ToLower(appID) + ":" + table
}

func appendWorkspaceLogRow(customerID, table string, row monitorLogRow) {
	appendLogRow(logRowsKey(customerID, table), row)
}

func dropLogRows(key string) {
	monitorLogs.Drop(key)
}

func dropWorkspaceLogs(ws Workspace) {
	for _, t := range logAnalyticsWorkspaceTables(ws) {
		dropLogRows(logRowsKey(ws.Properties.CustomerID, t.Name))
		logAnalyticsTables.Delete(t.ID)
	}
}

func logWorkspaceByCustomerID(customerID string) (Workspace, bool) {
	if azureMonitorWorkspaces == nil || customerID == "" {
		return Workspace{}, false
	}
	return azureWorkspacesByCustomerID.Lookup(azureMonitorWorkspaces, strings.ToLower(customerID), azureWorkspaceCustomerIDKeys)
}

// The workspace appLogsConfiguration names by customer id when the destination
// is log-analytics.
func acaLogWorkspace(envID string) (Workspace, bool) {
	if envID == "" || acaEnvironments == nil {
		return Workspace{}, false
	}
	env, ok := acaEnvironments.Get(envID)
	if !ok {
		return Workspace{}, false
	}
	cfg := env.Properties.AppLogsConfiguration
	if cfg == nil || !strings.EqualFold(cfg.Destination, "log-analytics") || cfg.LogAnalyticsConfiguration == nil {
		return Workspace{}, false
	}
	ws, ok := logWorkspaceByCustomerID(cfg.LogAnalyticsConfiguration.CustomerId)
	if ok {
		ensurePlatformCustomLogTables(ws, "ContainerAppConsoleLogs_CL", "ContainerAppSystemLogs_CL")
	}
	return ws, ok
}

// The custom log table (<category>_CL) of the appLogsConfiguration workspace
// when the destination is log-analytics, or the resource-specific table of
// each workspace a diagnostic setting sends the category to when it is
// azure-monitor ("Log options in Azure Container Apps").
type acaLogDestination struct {
	customerID       string
	resourceSpecific bool
}

func acaLogDestinations(envID, category string) []acaLogDestination {
	if envID == "" || acaEnvironments == nil {
		return nil
	}
	env, ok := acaEnvironments.Get(envID)
	if !ok || env.Properties.AppLogsConfiguration == nil {
		return nil
	}
	switch strings.ToLower(env.Properties.AppLogsConfiguration.Destination) {
	case "log-analytics":
		if ws, ok := acaLogWorkspace(envID); ok {
			return []acaLogDestination{{customerID: ws.Properties.CustomerID}}
		}
	case "azure-monitor":
		var out []acaLogDestination
		for _, ws := range azureDiagnosticLogWorkspaces(envID, category) {
			out = append(out, acaLogDestination{customerID: ws.Properties.CustomerID, resourceSpecific: true})
		}
		return out
	}
	return nil
}

// The row carries the custom log table's columns, suffixed _s; an environment
// that names no workspace keeps no Log Analytics rows.
func acaWorkspaceLog(envID, resourceID, category string, row monitorLogRow) {
	destinations := acaLogDestinations(envID, category)
	if len(destinations) == 0 {
		return
	}
	if _, set := row["TimeGenerated"]; !set {
		row["TimeGenerated"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	row["EnvironmentName_s"] = acaEnvironmentName(envID)
	row["_ResourceId"] = resourceID
	for _, d := range destinations {
		if !d.resourceSpecific {
			appendWorkspaceLogRow(d.customerID, category+"_CL", row)
			continue
		}
		appendWorkspaceLogRow(d.customerID, category, acaResourceSpecificRow(row, category, d.customerID))
	}
}

// The custom log columns without their _s suffix, plus the columns every Azure
// table carries. ContainerAppSystemLogs' Type is the event's severity, which
// the row carries; elsewhere Type names the table.
func acaResourceSpecificRow(row monitorLogRow, table, customerID string) monitorLogRow {
	out := monitorLogRow{}
	for key, value := range row {
		out[strings.TrimSuffix(key, "_s")] = value
	}
	if _, set := out["Type"]; !set {
		out["Type"] = table
	}
	out["TenantId"] = customerID
	out["SourceSystem"] = "Azure"
	out["_SubscriptionId"] = azureSubscriptionOfID(row["_ResourceId"])
	return out
}

func acaConsoleLog(envID, resourceID string, line sim.LogLine, row monitorLogRow) {
	stream := line.Stream
	if stream == "" {
		stream = "stdout"
	}
	row["Log_s"] = line.Text
	row["Stream_s"] = stream
	if !line.Timestamp.IsZero() {
		row["TimeGenerated"] = line.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	acaWorkspaceLog(envID, resourceID, "ContainerAppConsoleLogs", row)
}

func acaJobSystemLog(job ContainerAppJob, execName, eventType, message string) {
	acaWorkspaceLog(job.Properties.EnvironmentID, job.ID, "ContainerAppSystemLogs", monitorLogRow{
		"JobName_s":       job.Name,
		"ExecutionName_s": execName,
		"Log_s":           message,
		"Type_s":          eventType,
	})
}

func acaAppEnvironmentID(app ContainerApp) string {
	if app.Properties.EnvironmentID != "" {
		return app.Properties.EnvironmentID
	}
	return app.Properties.ManagedEnvironmentID
}

// APPLICATIONINSIGHTS_CONNECTION_STRING's InstrumentationKey, else
// APPINSIGHTS_INSTRUMENTATIONKEY.
func siteAppInsightsComponent(site *Site) (AppInsightsComponent, bool) {
	settings := siteAppSettings(site)
	ikey := ""
	for _, part := range strings.Split(settings["APPLICATIONINSIGHTS_CONNECTION_STRING"], ";") {
		if k, v, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(k), "InstrumentationKey") {
			ikey = strings.TrimSpace(v)
		}
	}
	if ikey == "" {
		ikey = strings.TrimSpace(settings["APPINSIGHTS_INSTRUMENTATIONKEY"])
	}
	if ikey == "" || azureAppInsightsComponents == nil {
		return AppInsightsComponent{}, false
	}
	return azureInsightsByInstrumentationKey.Lookup(azureAppInsightsComponents, strings.ToLower(ikey), appInsightsInstrumentationKeyKeys)
}

// The workspace a workspace-based component names, or the component's own
// store.
func appInsightsTelemetryKey(c AppInsightsComponent, table string) (string, bool) {
	if c.Properties.WorkspaceResourceId == "" {
		return appLogRowsKey(c.Properties.ApplicationID, table), true
	}
	ws, ok := logAnalyticsWorkspaceByID(c.Properties.WorkspaceResourceId)
	if !ok {
		return "", false
	}
	return logRowsKey(ws.Properties.CustomerID, table), true
}

// The site's docker log, the AppTraces table of its connected component, and
// the AppServiceConsoleLogs table of every workspace a diagnostic setting
// sends that category to.
type siteLogDestination struct {
	siteID    string
	roleName  string
	tracesKey string
	component string
}

func newSiteLogDestination(site *Site) siteLogDestination {
	d := siteLogDestination{siteID: site.ID, roleName: site.Name}
	if c, ok := siteAppInsightsComponent(site); ok {
		if key, ok := appInsightsTelemetryKey(c, "AppTraces"); ok {
			d.tracesKey, d.component = key, c.ID
		}
	}
	return d
}

func (d siteLogDestination) write(at time.Time, message string) {
	ts := at.UTC().Format(time.RFC3339Nano)
	appendSiteDockerLog(d.siteID, ts, message)
	for _, ws := range azureDiagnosticLogWorkspaces(d.siteID, "AppServiceConsoleLogs") {
		appendWorkspaceLogRow(ws.Properties.CustomerID, "AppServiceConsoleLogs", monitorLogRow{
			"TimeGenerated":     ts,
			"Category":          "AppServiceConsoleLogs",
			"ResultDescription": message,
			"_ResourceId":       d.siteID,
			"_SubscriptionId":   azureSubscriptionOfID(d.siteID),
			"TenantId":          ws.Properties.CustomerID,
			"SourceSystem":      "Azure",
			"Type":              "AppServiceConsoleLogs",
		})
	}
	if d.tracesKey == "" {
		return
	}
	appendLogRow(d.tracesKey, monitorLogRow{
		"TimeGenerated": ts,
		"AppRoleName":   d.roleName,
		"Message":       message,
		"_ResourceId":   d.component,
	})
}

// Record a platform message as the container's output is recorded.
func injectSiteTrace(site *Site, message string) {
	newSiteLogDestination(site).write(time.Now(), message)
}

type funcLogSink struct {
	dest siteLogDestination
}

func newFuncLogSink(site *Site) *funcLogSink {
	return &funcLogSink{dest: newSiteLogDestination(site)}
}

func (s *funcLogSink) WriteLog(line sim.LogLine) {
	at := line.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	s.dest.write(at, line.Text)
}

// The store keys of the table's rows in each place the query reads and, for a
// query addressing one resource, that resource, whose rows alone it keeps.
type logScope struct {
	workspaces []Workspace
	appID      string
	resourceID string
}

// A workspace's table, or an Azure table for a classic component's store.
func (s logScope) tableSchema(table string) ([]Column, bool) {
	for _, ws := range s.workspaces {
		if t, ok := logAnalyticsWorkspaceTable(ws, table); ok {
			return logAnalyticsTableSchema(t), true
		}
	}
	if s.appID != "" {
		cols, ok := kqlTableSchemas[table]
		return cols, ok
	}
	return nil, false
}

func (s logScope) rows(table string) []monitorLogRow {
	var keys []string
	for _, ws := range s.workspaces {
		keys = append(keys, logRowsKey(ws.Properties.CustomerID, table))
	}
	if s.appID != "" {
		keys = append(keys, appLogRowsKey(s.appID, table))
	}
	prefix := strings.ToLower(s.resourceID)
	now := time.Now()
	var out []monitorLogRow
	for _, key := range keys {
		for _, row := range monitorQueryableRows(key, now) {
			if prefix != "" {
				rid := strings.ToLower(row["_ResourceId"])
				if rid != prefix && !strings.HasPrefix(rid, prefix+"/") {
					continue
				}
			}
			out = append(out, row)
		}
	}
	return out
}

func workspaceLogScope(customerID string) (logScope, bool) {
	ws, ok := logWorkspaceByCustomerID(customerID)
	if !ok {
		return logScope{}, false
	}
	return logScope{workspaces: []Workspace{ws}}, true
}

// The component's rows in the workspace it names, or its own store.
func appLogScope(appID string) (logScope, bool) {
	if azureAppInsightsComponents == nil {
		return logScope{}, false
	}
	c, ok := azureInsightsByApplicationID.Lookup(azureAppInsightsComponents, strings.ToLower(appID), azureInsightsApplicationIDKeys)
	if !ok {
		return logScope{}, false
	}
	return componentLogScope(c)
}

func componentLogScope(c AppInsightsComponent) (logScope, bool) {
	if c.Properties.WorkspaceResourceId == "" {
		return logScope{appID: c.Properties.ApplicationID}, true
	}
	ws, ok := logAnalyticsWorkspaceByID(c.Properties.WorkspaceResourceId)
	if !ok {
		return logScope{}, false
	}
	return logScope{workspaces: []Workspace{ws}, resourceID: c.ID}, true
}

// A workspace queried as a resource reads all its rows, a component its
// telemetry, and any other scope the rows each workspace holds for resources
// at or under it.
func resourceLogScope(resourceID string) logScope {
	resourceID = "/" + strings.Trim(resourceID, "/")
	if ws, ok := logAnalyticsWorkspaceByID(resourceID); ok {
		return logScope{workspaces: []Workspace{ws}}
	}
	if azureAppInsightsComponents != nil {
		if c, ok := azureInsightsByResourceID.Lookup(azureAppInsightsComponents, strings.ToLower(resourceID), appInsightsResourceIDKeys); ok {
			if scope, ok := componentLogScope(c); ok {
				return scope
			}
		}
	}
	scope := logScope{resourceID: resourceID}
	if azureMonitorWorkspaces != nil {
		scope.workspaces = azureAllWorkspaces.LookupAll(azureMonitorWorkspaces, "all", func(Workspace) []string { return []string{"all"} })
	}
	return scope
}

// One key, so a query reading every workspace decodes the store once per
// change.
var azureAllWorkspaces sim.GenerationIndex[Workspace]
