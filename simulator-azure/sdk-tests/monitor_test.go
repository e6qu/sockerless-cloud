package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/monitor/azquery"
	"github.com/Azure/azure-sdk-for-go/sdk/monitor/ingestion/azlogs"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for Log Analytics workspaces' tables, data collection
// endpoints and rules, and the Logs Ingestion API that uploads through a rule:
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/workspaces/{workspaceName}/tables
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/workspaces/{workspaceName}/tables/{tableName}
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints/{dataCollectionEndpointName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/dataCollectionEndpoints
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionRules/{dataCollectionRuleName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionRules
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/dataCollectionRules
//	POST /dataCollectionRules/{ruleId}/streams/{stream}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionRules/{dataCollectionRuleName}/associations (declared 501)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints/{dataCollectionEndpointName}/associations (declared 501)

// logWorkspace is a Log Analytics workspace: its ARM id and the customer id
// the query API addresses it by.
type logWorkspace struct {
	rg, name, id, customerID string
}

func createLogWorkspace(t *testing.T, rg, name string) logWorkspace {
	t.Helper()
	ensureRG(t, rg)
	client, err := armoperationalinsights.NewWorkspacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armoperationalinsights.Workspace{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	ws, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return logWorkspace{rg: rg, name: name, id: *ws.ID, customerID: *ws.Properties.CustomerID}
}

// createCustomLogTable creates a custom log table in a workspace.
func createCustomLogTable(t *testing.T, ws logWorkspace, table string, columns map[string]armoperationalinsights.ColumnTypeEnum) {
	t.Helper()
	client, err := armoperationalinsights.NewTablesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	cols := []*armoperationalinsights.Column{{Name: to.Ptr("TimeGenerated"), Type: to.Ptr(armoperationalinsights.ColumnTypeEnumDateTime)}}
	for name, typ := range columns {
		cols = append(cols, &armoperationalinsights.Column{Name: to.Ptr(name), Type: to.Ptr(typ)})
	}
	poller, err := client.BeginCreateOrUpdate(ctx, ws.rg, ws.name, table, armoperationalinsights.Table{
		Properties: &armoperationalinsights.TableProperties{
			Schema: &armoperationalinsights.Schema{Name: to.Ptr(table), Columns: cols},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// ingestionClient is an azlogs client for a logs ingestion endpoint.
// azlogs refuses a credential over plain HTTP, so the client is handed the
// endpoint's HTTPS spelling and kvSchemeRewritingTransport carries it to the
// simulator's listener, as logsClientOpts does for azquery.
func ingestionClient(t *testing.T, endpoint string) *azlogs.Client {
	t.Helper()
	endpoint = "https://" + strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
	client, err := azlogs.NewClient(endpoint, &fakeCredential{}, &azlogs.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				azlogs.ServiceNameIngestion: {Audience: "https://monitor.azure.com"},
			}},
			Transport: kvSchemeRewritingTransport{},
		},
	})
	require.NoError(t, err)
	return client
}

// logsPipeline is a workspace whose custom table a data collection rule
// writes one declared stream into unchanged.
type logsPipeline struct {
	ws          logWorkspace
	table       string
	stream      string
	immutableID string
	client      *azlogs.Client
}

// newLogsPipeline creates a workspace, a custom log table with the given
// string columns, and a Direct data collection rule whose stream carries
// TimeGenerated and those columns into the table.
func newLogsPipeline(t *testing.T, rg, table string, columns ...string) logsPipeline {
	t.Helper()
	ws := createLogWorkspace(t, rg, rg+"-ws")
	tableCols := map[string]armoperationalinsights.ColumnTypeEnum{}
	streamCols := []*armmonitor.ColumnDefinition{{Name: to.Ptr("TimeGenerated"), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeDatetime)}}
	for _, c := range columns {
		tableCols[c] = armoperationalinsights.ColumnTypeEnumString
		streamCols = append(streamCols, &armmonitor.ColumnDefinition{Name: to.Ptr(c), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeString)})
	}
	createCustomLogTable(t, ws, table, tableCols)
	stream := "Custom-" + table
	rules, err := armmonitor.NewDataCollectionRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created, err := rules.Create(ctx, rg, rg+"-dcr", armmonitor.DataCollectionRuleResource{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr(armmonitor.KnownDataCollectionRuleResourceKind("Direct")),
		Properties: &armmonitor.DataCollectionRuleResourceProperties{
			StreamDeclarations: map[string]*armmonitor.StreamDeclaration{stream: {Columns: streamCols}},
			Destinations: &armmonitor.DataCollectionRuleDestinations{
				LogAnalytics: []*armmonitor.LogAnalyticsDestination{{Name: to.Ptr("ws"), WorkspaceResourceID: to.Ptr(ws.id)}},
			},
			DataFlows: []*armmonitor.DataFlow{{
				Streams:      []*armmonitor.KnownDataFlowStreams{to.Ptr(armmonitor.KnownDataFlowStreams(stream))},
				Destinations: []*string{to.Ptr("ws")},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties.Endpoints, "a Direct rule carries its own logs ingestion endpoint")
	return logsPipeline{
		ws: ws, table: table, stream: stream,
		immutableID: *created.Properties.ImmutableID,
		client:      ingestionClient(t, *created.Properties.Endpoints.LogsIngestion),
	}
}

func (p logsPipeline) upload(t *testing.T, rows []map[string]any) {
	t.Helper()
	body, err := json.Marshal(rows)
	require.NoError(t, err)
	_, err = p.client.Upload(ctx, p.immutableID, p.stream, body, nil)
	require.NoError(t, err)
}

// queryWorkspace sends a KQL query and returns the parsed response.
func queryWorkspace(t *testing.T, workspaceID, kql string) queryResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": kql})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v1/workspaces/"+workspaceID+"/query",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", data)

	var result queryResponse
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

type queryResponse struct {
	Tables []struct {
		Name    string `json:"name"`
		Columns []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	} `json:"tables"`
}

func requireColumnIndex(t *testing.T, table struct {
	Name    string `json:"name"`
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Rows [][]any `json:"rows"`
}, name string) int {
	t.Helper()
	for i, col := range table.Columns {
		if col.Name == name {
			return i
		}
	}
	require.Failf(t, "missing query result column", "column %q not found in %v", name, table.Columns)
	return -1
}

// acaLogs is the Container Apps environment the suite's jobs run in, linked
// to a workspace, so their logs land where the tests query them.
var acaLogs struct {
	sync.Mutex
	envID string
	ws    logWorkspace
}

// acaLogsEnvironment creates, once per run, the environment the suite's jobs
// run in and the workspace its appLogsConfiguration names.
func acaLogsEnvironment(t *testing.T) (envID, customerID string) {
	t.Helper()
	acaLogs.Lock()
	defer acaLogs.Unlock()
	if acaLogs.envID != "" {
		return acaLogs.envID, acaLogs.ws.customerID
	}
	ws := createLogWorkspace(t, "aca-logs-rg", "aca-logs-ws")
	keys, err := armoperationalinsights.NewSharedKeysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	shared, err := keys.GetSharedKeys(ctx, ws.rg, ws.name, nil)
	require.NoError(t, err)
	envs, err := armappcontainers.NewManagedEnvironmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := envs.BeginCreateOrUpdate(ctx, ws.rg, "aca-logs-env", armappcontainers.ManagedEnvironment{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ManagedEnvironmentProperties{
			AppLogsConfiguration: &armappcontainers.AppLogsConfiguration{
				Destination: to.Ptr("log-analytics"),
				LogAnalyticsConfiguration: &armappcontainers.LogAnalyticsConfiguration{
					CustomerID: to.Ptr(ws.customerID),
					SharedKey:  shared.PrimarySharedKey,
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	env, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	acaLogs.envID, acaLogs.ws = *env.ID, ws
	return acaLogs.envID, ws.customerID
}

// acaLogsEnvironmentID is the environment the suite's jobs run in.
func acaLogsEnvironmentID(t *testing.T) string {
	t.Helper()
	envID, _ := acaLogsEnvironment(t)
	return envID
}

// acaLogsCustomerID is the workspace the suite's jobs log to.
func acaLogsCustomerID(t *testing.T) string {
	t.Helper()
	_, customerID := acaLogsEnvironment(t)
	return customerID
}

func TestMonitor_KQLOverIngestedRows(t *testing.T) {
	p := newLogsPipeline(t, "monitor-kql-rg", "SimJobs_CL", "Job", "Log")
	now := time.Now().UTC()
	job := uniqueName("myjob")
	p.upload(t, []map[string]any{
		{"TimeGenerated": now.Add(-10 * time.Minute).Format(time.RFC3339), "Job": job, "Log": "old entry"},
		{"TimeGenerated": now.Add(-2 * time.Minute).Format(time.RFC3339), "Job": job, "Log": "starting"},
		{"TimeGenerated": now.Add(-1 * time.Minute).Format(time.RFC3339), "Job": job, "Log": "running"},
		{"TimeGenerated": now.Add(-1 * time.Minute).Format(time.RFC3339), "Job": "otherjob", "Log": "other"},
	})

	result := queryWorkspace(t, p.ws.customerID, `SimJobs_CL | where Job == "`+job+`" | take 100`)
	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	assert.Equal(t, "PrimaryResult", table.Name)
	assert.Equal(t, "TimeGenerated", table.Columns[0].Name)
	logColumn := requireColumnIndex(t, table, "Log")
	require.Len(t, table.Rows, 3, "only this job's rows match")
	var logs []any
	for _, row := range table.Rows {
		logs = append(logs, row[logColumn])
	}
	assert.ElementsMatch(t, []any{"old entry", "starting", "running"}, logs)

	mid := now.Add(-5 * time.Minute).Format(time.RFC3339)
	result = queryWorkspace(t, p.ws.customerID,
		`SimJobs_CL | where Job == "`+job+`" | where TimeGenerated > datetime(`+mid+`) | order by TimeGenerated asc | project Log`)
	require.Len(t, result.Tables[0].Rows, 2)
	assert.Equal(t, "starting", result.Tables[0].Rows[0][0])

	result = queryWorkspace(t, p.ws.customerID, `SimJobs_CL | where Job == "`+job+`" | take 2`)
	assert.Len(t, result.Tables[0].Rows, 2)
}

// A data collection rule decides where an upload lands: its stream's data
// flow runs the transform over the rows and writes what comes out to the
// output table of the destination workspace, and no other workspace sees it.
func TestMonitor_LogsIngestionRoutesThroughTheRule(t *testing.T) {
	rg := "monitor-dcr-rg"
	target := createLogWorkspace(t, rg, "monitor-dcr-target")
	other := createLogWorkspace(t, rg, "monitor-dcr-other")
	createCustomLogTable(t, target, "SimEvents_CL", map[string]armoperationalinsights.ColumnTypeEnum{
		"Job":     armoperationalinsights.ColumnTypeEnumString,
		"Message": armoperationalinsights.ColumnTypeEnumString,
		"Attempt": armoperationalinsights.ColumnTypeEnumInt,
		"Detail":  armoperationalinsights.ColumnTypeEnumDynamic,
	})

	endpoints, err := armmonitor.NewDataCollectionEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	dce, err := endpoints.Create(ctx, rg, "monitor-dce", armmonitor.DataCollectionEndpointResource{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	require.NotNil(t, dce.Properties.LogsIngestion)
	require.NotEmpty(t, *dce.Properties.LogsIngestion.Endpoint)
	assert.True(t, strings.HasPrefix(*dce.Properties.ImmutableID, "dce-"))
	gotDCE, err := endpoints.Get(ctx, rg, "monitor-dce", nil)
	require.NoError(t, err)
	assert.Equal(t, *dce.Properties.ImmutableID, *gotDCE.Properties.ImmutableID)

	rules, err := armmonitor.NewDataCollectionRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	stream := "Custom-SimEventsRaw"
	rule := func(transform, outputStream string) armmonitor.DataCollectionRuleResource {
		return armmonitor.DataCollectionRuleResource{
			Location: to.Ptr("eastus"),
			Properties: &armmonitor.DataCollectionRuleResourceProperties{
				DataCollectionEndpointID: dce.ID,
				StreamDeclarations: map[string]*armmonitor.StreamDeclaration{stream: {Columns: []*armmonitor.ColumnDefinition{
					{Name: to.Ptr("Time"), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeString)},
					{Name: to.Ptr("Job"), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeString)},
					{Name: to.Ptr("Message"), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeString)},
					{Name: to.Ptr("Attempt"), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeInt)},
					{Name: to.Ptr("Payload"), Type: to.Ptr(armmonitor.KnownColumnDefinitionTypeString)},
				}}},
				Destinations: &armmonitor.DataCollectionRuleDestinations{
					LogAnalytics: []*armmonitor.LogAnalyticsDestination{{Name: to.Ptr("target"), WorkspaceResourceID: to.Ptr(target.id)}},
				},
				DataFlows: []*armmonitor.DataFlow{{
					Streams:      []*armmonitor.KnownDataFlowStreams{to.Ptr(armmonitor.KnownDataFlowStreams(stream))},
					Destinations: []*string{to.Ptr("target")},
					TransformKql: to.Ptr(transform),
					OutputStream: to.Ptr(outputStream),
				}},
			},
		}
	}

	// The service refuses a rule whose transform writes a column the output
	// table lacks, or that writes to a table the workspace does not have.
	_, err = rules.Create(ctx, rg, "monitor-dcr-bad", rule(`source | extend TimeGenerated = todatetime(Time), Extra = 1`, "Custom-SimEvents_CL"), nil)
	requireAzureErrorCode(t, err, "InvalidPayload", "a transform column the table lacks")
	_, err = rules.Create(ctx, rg, "monitor-dcr-bad", rule(`source | extend TimeGenerated = todatetime(Time) | project TimeGenerated, Job`, "Custom-Missing_CL"), nil)
	requireAzureErrorCode(t, err, "InvalidPayload", "an output table the workspace lacks")

	const transform = `source
| extend TimeGenerated = todatetime(Time), Detail = parse_json(Payload)
| where Attempt > 0
| project TimeGenerated, Job, Message = strcat(Message, " #", tostring(Attempt)), Attempt, Detail`
	created, err := rules.Create(ctx, rg, "monitor-dcr", rule(transform, "Custom-SimEvents_CL"), nil)
	require.NoError(t, err)
	immutableID := *created.Properties.ImmutableID
	assert.True(t, strings.HasPrefix(immutableID, "dcr-"))
	require.Len(t, created.Properties.Destinations.LogAnalytics, 1)
	assert.Equal(t, target.customerID, *created.Properties.Destinations.LogAnalytics[0].WorkspaceID,
		"the rule records the destination workspace's customer id")

	client := ingestionClient(t, *dce.Properties.LogsIngestion.Endpoint)
	job := uniqueName("routed-job")
	now := time.Now().UTC().Format(time.RFC3339)
	body, err := json.Marshal([]map[string]any{
		{"Time": now, "Job": job, "Message": "first", "Attempt": 1, "Payload": `{"kind":"build","steps":[1,2]}`},
		{"Time": now, "Job": job, "Message": "second", "Attempt": 2, "Payload": `{"kind":"test"}`},
		{"Time": now, "Job": job, "Message": "dropped", "Attempt": 0, "Payload": `{}`},
	})
	require.NoError(t, err)
	_, err = client.Upload(ctx, immutableID, stream, body, nil)
	require.NoError(t, err)

	logs, err := azquery.NewLogsClient(&fakeCredential{}, logsClientOpts())
	require.NoError(t, err)
	resp, err := logs.QueryWorkspace(ctx, target.customerID, azquery.Body{
		Query: to.Ptr(`SimEvents_CL | where Job == "` + job + `" | order by Attempt asc | project Message, Kind = tostring(Detail.kind), Steps = array_length(Detail.steps)`),
	}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Tables[0].Rows, 2, "the transform's where keeps the rows with an attempt")
	assert.Equal(t, []any{"first #1", "build", float64(2)}, []any(resp.Tables[0].Rows[0]))
	assert.Equal(t, "test", resp.Tables[0].Rows[1][1])

	// The other workspace holds no such table, and the rule wrote nothing
	// to it.
	_, err = logs.QueryWorkspace(ctx, other.customerID, azquery.Body{Query: to.Ptr(`SimEvents_CL | take 1`)}, nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "a workspace without the table refuses the query: %v", err)
	assert.Equal(t, "BadArgumentError", respErr.ErrorCode)

	// The upload names the rule by its immutable id and a stream it declares.
	_, err = client.Upload(ctx, immutableID, "Custom-Undeclared", body, nil)
	require.True(t, errors.As(err, &respErr), "%v", err)
	assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)
	_, err = client.Upload(ctx, "dcr-00000000000000000000000000000000", stream, body, nil)
	require.True(t, errors.As(err, &respErr), "%v", err)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	// The simulator runs no Azure Monitor Agent, so it serves no rule
	// associations and says so.
	associations, err := armmonitor.NewDataCollectionRuleAssociationsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = associations.NewListByRulePager(rg, "monitor-dcr", nil).NextPage(ctx)
	requireAzureErrorCode(t, err, "NotImplemented", "a rule's associations")
	_, err = associations.NewListByDataCollectionEndpointPager(rg, "monitor-dce", nil).NextPage(ctx)
	requireAzureErrorCode(t, err, "NotImplemented", "an endpoint's associations")

	// Read, list, tag and delete the rule and the endpoint.
	got, err := rules.Get(ctx, rg, "monitor-dcr", nil)
	require.NoError(t, err)
	assert.Equal(t, immutableID, *got.Properties.ImmutableID)
	assert.Equal(t, transform, *got.Properties.DataFlows[0].TransformKql)
	var listed []string
	pager := rules.NewListByResourceGroupPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, r := range page.Value {
			listed = append(listed, *r.Name)
		}
	}
	assert.Equal(t, []string{"monitor-dcr"}, listed)
	subPager := rules.NewListBySubscriptionPager(nil)
	found := false
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, r := range page.Value {
			found = found || *r.ID == *created.ID
		}
	}
	assert.True(t, found, "the subscription lists the rule")
	tagged, err := rules.Update(ctx, rg, "monitor-dcr", armmonitor.ResourceForUpdate{Tags: map[string]*string{"team": to.Ptr("logs")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "logs", *tagged.Tags["team"])
	_, err = rules.Delete(ctx, rg, "monitor-dcr", nil)
	require.NoError(t, err)
	_, err = rules.Get(ctx, rg, "monitor-dcr", nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a deleted rule")
	_, err = client.Upload(ctx, immutableID, stream, body, nil)
	require.True(t, errors.As(err, &respErr), "%v", err)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode, "a deleted rule takes no uploads")

	taggedDCE, err := endpoints.Update(ctx, rg, "monitor-dce", armmonitor.ResourceForUpdate{Tags: map[string]*string{"team": to.Ptr("logs")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "logs", *taggedDCE.Tags["team"])
	dcePager := endpoints.NewListByResourceGroupPager(rg, nil)
	page, err := dcePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Value, 1)
	subDCEPager := endpoints.NewListBySubscriptionPager(nil)
	_, err = subDCEPager.NextPage(ctx)
	require.NoError(t, err)
	_, err = endpoints.Delete(ctx, rg, "monitor-dce", nil)
	require.NoError(t, err)
}

// A workspace's tables: the Azure tables it holds from the start, and the
// custom log tables a customer creates, changes and deletes.
func TestMonitor_WorkspaceTables(t *testing.T) {
	ws := createLogWorkspace(t, "monitor-tables-rg", "monitor-tables-ws")
	client, err := armoperationalinsights.NewTablesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	traces, err := client.Get(ctx, ws.rg, ws.name, "AppTraces", nil)
	require.NoError(t, err)
	assert.Equal(t, armoperationalinsights.TableTypeEnumMicrosoft, *traces.Properties.Schema.TableType)
	assert.EqualValues(t, 30, *traces.Properties.RetentionInDays, "a table keeps the workspace's retention")

	createCustomLogTable(t, ws, "Orders_CL", map[string]armoperationalinsights.ColumnTypeEnum{
		"OrderId": armoperationalinsights.ColumnTypeEnumString,
		"Amount":  armoperationalinsights.ColumnTypeEnumReal,
	})
	orders, err := client.Get(ctx, ws.rg, ws.name, "Orders_CL", nil)
	require.NoError(t, err)
	assert.Equal(t, armoperationalinsights.TableTypeEnumCustomLog, *orders.Properties.Schema.TableType)
	assert.Equal(t, armoperationalinsights.TableSubTypeEnumDataCollectionRuleBased, *orders.Properties.Schema.TableSubType)
	assert.Len(t, orders.Properties.Schema.Columns, 3)

	poller, err := client.BeginUpdate(ctx, ws.rg, ws.name, "Orders_CL", armoperationalinsights.Table{
		Properties: &armoperationalinsights.TableProperties{RetentionInDays: to.Ptr[int32](60), TotalRetentionInDays: to.Ptr[int32](90)},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	orders, err = client.Get(ctx, ws.rg, ws.name, "Orders_CL", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 60, *orders.Properties.RetentionInDays)
	assert.EqualValues(t, 30, *orders.Properties.ArchiveRetentionInDays)

	names := map[string]bool{}
	pager := client.NewListByWorkspacePager(ws.rg, ws.name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, tbl := range page.Value {
			names[*tbl.Name] = true
		}
	}
	assert.True(t, names["Orders_CL"] && names["AppTraces"], "the list holds the custom and the Azure tables: %v", names)

	// The query engine reads the custom table's schema.
	result := queryWorkspace(t, ws.customerID, `Orders_CL | take 1`)
	var cols []string
	for _, c := range result.Tables[0].Columns {
		cols = append(cols, c.Name+":"+c.Type)
	}
	assert.ElementsMatch(t, []string{"TimeGenerated:datetime", "OrderId:string", "Amount:real"}, cols)

	_, err = client.BeginCreateOrUpdate(ctx, ws.rg, ws.name, "Orders", armoperationalinsights.Table{
		Properties: &armoperationalinsights.TableProperties{Schema: &armoperationalinsights.Schema{
			Name: to.Ptr("Orders"), Columns: []*armoperationalinsights.Column{{Name: to.Ptr("TimeGenerated"), Type: to.Ptr(armoperationalinsights.ColumnTypeEnumDateTime)}},
		}},
	}, nil)
	requireAzureErrorCode(t, err, "InvalidParameter", "a custom table name without _CL")

	del, err := client.BeginDelete(ctx, ws.rg, ws.name, "Orders_CL", nil)
	require.NoError(t, err)
	_, err = del.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, ws.rg, ws.name, "Orders_CL", nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a deleted table")
}
