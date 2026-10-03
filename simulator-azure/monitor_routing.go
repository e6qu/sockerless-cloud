package main

import (
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// monitor_routing.go decides where a log row lands and what a query reads.
// A workspace keeps its rows under its customer id, so one workspace's query
// never reads another's. Rows reach a workspace only through what names it:
// a Container Apps environment's appLogsConfiguration, the workspace a
// workspace-based Application Insights component names, or a data collection
// rule's Log Analytics destination. A classic Application Insights component
// keeps its telemetry in its own store.

// logRowsKey is where a workspace keeps one table's rows.
func logRowsKey(customerID, table string) string {
	return strings.ToLower(customerID) + ":" + table
}

// appLogRowsKey is where a classic Application Insights component keeps one
// table's rows.
func appLogRowsKey(appID, table string) string {
	return "app/" + strings.ToLower(appID) + ":" + table
}

func appendWorkspaceLogRow(customerID, table string, row monitorLogRow) {
	appendLogRow(logRowsKey(customerID, table), row)
}

// dropLogRows deletes the rows a store key holds.
func dropLogRows(key string) {
	logMu.Lock()
	defer logMu.Unlock()
	monitorLogs.Delete(key)
}

// dropWorkspaceLogs deletes a workspace's rows and tables.
func dropWorkspaceLogs(ws Workspace) {
	for _, t := range logAnalyticsWorkspaceTables(ws) {
		dropLogRows(logRowsKey(ws.Properties.CustomerID, t.Name))
		logAnalyticsTables.Delete(t.ID)
	}
}

// logWorkspaceByCustomerID reads the workspace the query API addresses.
func logWorkspaceByCustomerID(customerID string) (Workspace, bool) {
	if azureMonitorWorkspaces == nil || customerID == "" {
		return Workspace{}, false
	}
	return azureWorkspacesByCustomerID.Lookup(azureMonitorWorkspaces, strings.ToLower(customerID), azureWorkspaceCustomerIDKeys)
}

// Container Apps.

// acaLogWorkspace is the workspace a Container Apps environment sends its
// logs to: the one its appLogsConfiguration names by customer id when its
// destination is log-analytics.
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

// acaWorkspaceLog writes a Container Apps row to the table of the workspace the
// resource's environment names; an environment that names none keeps no
// Log Analytics rows.
func acaWorkspaceLog(envID, resourceID, table string, row monitorLogRow) {
	ws, ok := acaLogWorkspace(envID)
	if !ok {
		return
	}
	if _, set := row["TimeGenerated"]; !set {
		row["TimeGenerated"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	row["EnvironmentName_s"] = acaEnvironmentName(envID)
	row["_ResourceId"] = resourceID
	appendWorkspaceLogRow(ws.Properties.CustomerID, table, row)
}

// acaConsoleLog writes a line a container printed to ContainerAppConsoleLogs_CL.
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
	acaWorkspaceLog(envID, resourceID, "ContainerAppConsoleLogs_CL", row)
}

// acaJobSystemLog writes a job's lifecycle event to ContainerAppSystemLogs_CL.
func acaJobSystemLog(job ContainerAppJob, execName, eventType, message string) {
	acaWorkspaceLog(job.Properties.EnvironmentID, job.ID, "ContainerAppSystemLogs_CL", monitorLogRow{
		"JobName_s":       job.Name,
		"ExecutionName_s": execName,
		"Log_s":           message,
		"Type_s":          eventType,
	})
}

// acaAppEnvironmentID is the environment an app runs in.
func acaAppEnvironmentID(app ContainerApp) string {
	if app.Properties.EnvironmentID != "" {
		return app.Properties.EnvironmentID
	}
	return app.Properties.ManagedEnvironmentID
}

// App Service.

// siteAppInsightsComponent is the Application Insights component a site's
// settings connect it to: APPLICATIONINSIGHTS_CONNECTION_STRING's
// InstrumentationKey, else APPINSIGHTS_INSTRUMENTATIONKEY.
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

// appInsightsTelemetryKey is where a component keeps a table's rows: the
// workspace a workspace-based component names, or the component's own store.
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

// siteLogDestination is where a site's container output goes: the site's own
// docker log, and the AppTraces table of the component it is connected to.
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

// injectSiteTrace records a platform message about a site the way its
// container's output is recorded.
func injectSiteTrace(site *Site, message string) {
	newSiteLogDestination(site).write(time.Now(), message)
}

// funcLogSink writes a site container's output to the site's log
// destination.
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

// Queries.

// logScope is what a query reads: the store keys of the table's rows in each
// place it reads, and, for a query addressing one resource, that resource,
// whose rows alone it keeps.
type logScope struct {
	workspaces []Workspace
	appID      string
	resourceID string
}

// tableSchema resolves a table against the scope: a workspace's table, or an
// Azure table for a classic component's store.
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

// rows reads the table's rows in scope.
func (s logScope) rows(table string) []monitorLogRow {
	var keys []string
	for _, ws := range s.workspaces {
		keys = append(keys, logRowsKey(ws.Properties.CustomerID, table))
	}
	if s.appID != "" {
		keys = append(keys, appLogRowsKey(s.appID, table))
	}
	prefix := strings.ToLower(s.resourceID)
	var out []monitorLogRow
	logMu.RLock()
	defer logMu.RUnlock()
	for _, key := range keys {
		rows, _ := monitorLogs.Get(key)
		for _, row := range rows {
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

// workspaceLogScope is the scope of a query addressed to a workspace by its
// customer id.
func workspaceLogScope(customerID string) (logScope, bool) {
	ws, ok := logWorkspaceByCustomerID(customerID)
	if !ok {
		return logScope{}, false
	}
	return logScope{workspaces: []Workspace{ws}}, true
}

// appLogScope is the scope of a query addressed to an Application Insights
// component by its app id: the component's rows in the workspace it names,
// or its own store.
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

// resourceLogScope is the scope of a resource-centric query: a workspace
// queried as a resource reads all its rows, a component reads its
// telemetry, and any other scope reads the rows each workspace holds for
// resources at or under it.
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

// azureAllWorkspaces holds every workspace under one key, so a query reading
// them all decodes the store once per change rather than once per query.
var azureAllWorkspaces sim.GenerationIndex[Workspace]
