package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/monitor/azquery"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Application Insights data plane and the resource-scoped Log Analytics
// query.
//
//	POST /v1/apps/{appId}/query
//	GET /v1/apps/{appId}/query
//	GET /v1/apps/{appId}/metadata
//	POST /v1/apps/{appId}/metadata
//	GET /v1/apps/{appId}/events/$metadata
//	GET /v1/apps/{appId}/events/{eventType}
//	GET /v1/apps/{appId}/events/{eventType}/{eventId}
//	GET /v1/apps/{appId}/metrics/metadata
//	GET /v1/apps/{appId}/metrics/{metricId}
//	POST /v1/apps/{appId}/metrics
//	GET /v1/{resourceId}/query
//	POST /v1/{resourceId}/query

func insightsRead(t *testing.T, method, path string, body string) map[string]any {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s %s", method, path)
	decoded := map[string]any{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
	return decoded
}

// An application's telemetry reads back through the query, the events and the
// metrics of one data plane; all three move when it writes, because all three
// read one store.
func TestSDK_ApplicationInsights_DataPlaneReadsTheTelemetry(t *testing.T) {
	t.Parallel()
	rg, site := "insights-dataplane-rg", "insights-dataplane-site"
	// A classic component keeps the telemetry of the site connected to it.
	component := createAppInsightsComponent(t, rg, "insights-dataplane-app", "")
	app, role := *component.Properties.AppID, site
	azureCreateContainerSite(t, rg, site, commandImageName, "serve 80 traced", map[string]string{
		"APPLICATIONINSIGHTS_CONNECTION_STRING": *component.Properties.ConnectionString,
	})
	defer azureDeleteSite(rg, site)
	azureInvokeFunction(t, site)
	azureInvokeFunction(t, site)
	// The engine's log stream delivers each access-log line after its response,
	// and Application Insights offers no event for its arrival.
	require.Eventually(t, func() bool {
		queried := insightsRead(t, http.MethodPost, "/v1/apps/"+app+"/query",
			`{"query":"AppTraces | where AppRoleName == \"`+role+`\" and Message == \"POST /api/function\""}`)
		tables, _ := queried["tables"].([]any)
		first, _ := tables[0].(map[string]any)
		rows, _ := first["rows"].([]any)
		return len(rows) == 2
	}, 30*time.Second, 200*time.Millisecond, "both requests' traces reach the component")

	// The query runs the KQL it is given.
	queried := insightsRead(t, http.MethodPost, "/v1/apps/"+app+"/query",
		`{"query":"AppTraces | where AppRoleName == \"`+role+`\" and Message == \"POST /api/function\" | take 10"}`)
	tables, _ := queried["tables"].([]any)
	require.NotEmpty(t, tables, "the query answers from the application's own telemetry")
	first, _ := tables[0].(map[string]any)
	rows, _ := first["rows"].([]any)
	assert.Len(t, rows, 2, "both traces the application wrote come back")

	// A table the application wrote nothing into comes back empty: the negative
	// control that the rows above were read.
	empty := insightsRead(t, http.MethodPost, "/v1/apps/"+app+"/query",
		`{"query":"AppRequests | take 10"}`)
	emptyTables, _ := empty["tables"].([]any)
	require.NotEmpty(t, emptyTables)
	emptyFirst, _ := emptyTables[0].(map[string]any)
	emptyRows, _ := emptyFirst["rows"].([]any)
	assert.Empty(t, emptyRows)

	// The GET spelling takes the query on the query string.
	got := insightsRead(t, http.MethodGet,
		"/v1/apps/"+app+"/query?query="+url.QueryEscape(`AppTraces | where AppRoleName == "`+role+`" | take 10`), "")
	gotTables, _ := got["tables"].([]any)
	require.NotEmpty(t, gotTables)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		metadata := insightsRead(t, method, "/v1/apps/"+app+"/metadata", "{}")
		assert.NotEmpty(t, metadata, "%s metadata describes the schema", method)
	}

	// Each event carries the ID the single-event read addresses it by.
	events := insightsRead(t, http.MethodGet, "/v1/apps/"+app+"/events/traces", "")
	value, _ := events["value"].([]any)
	require.GreaterOrEqual(t, len(value), 2, "the traces the application wrote are events")
	event, _ := value[0].(map[string]any)
	eventID, _ := event["id"].(string)
	require.NotEmpty(t, eventID)
	assert.Equal(t, "traces", event["type"])

	single := insightsRead(t, http.MethodGet,
		"/v1/apps/"+app+"/events/traces/"+url.PathEscape(eventID), "")
	singleValue, _ := single["value"].([]any)
	require.Len(t, singleValue, 1, "the id addresses exactly the event it came from")

	// An ID nothing matches filters everything out rather than erroring.
	none := insightsRead(t, http.MethodGet, "/v1/apps/"+app+"/events/traces/no-such-event", "")
	noneValue, _ := none["value"].([]any)
	assert.Empty(t, noneValue)

	// A type this API does not serve is refused.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/v1/apps/"+app+"/events/notAType", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// The OData metadata describes the events surface itself.
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/v1/apps/"+app+"/events/$metadata", nil)
	require.NoError(t, err)
	metaReq.Header.Set("Authorization", simARMBearer)
	metaResp, err := http.DefaultClient.Do(metaReq)
	require.NoError(t, err)
	defer metaResp.Body.Close()
	require.Equal(t, http.StatusOK, metaResp.StatusCode)
	assert.Contains(t, metaResp.Header.Get("Content-Type"), "xml")

	// A metric counts the telemetry, so it moves with what was written.
	metrics := insightsRead(t, http.MethodGet, "/v1/apps/"+app+"/metrics/traces/count", "")
	metricValue, _ := metrics["value"].(map[string]any)
	require.NotNil(t, metricValue)
	counted, _ := metricValue["traces/count"].(map[string]any)
	require.NotNil(t, counted, "the metric names itself in its own result")
	sum, _ := counted["sum"].(float64)
	assert.GreaterOrEqual(t, sum, float64(len(value)),
		"the metric counts the events the same telemetry produced")

	metricMeta := insightsRead(t, http.MethodGet, "/v1/apps/"+app+"/metrics/metadata", "")
	declared, _ := metricMeta["metrics"].(map[string]any)
	assert.Contains(t, declared, "traces/count")

	// The batch answers per request ID.
	batchReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v1/apps/"+app+"/metrics",
		strings.NewReader(`[{"id":"a","parameters":{"metricId":"traces/count"}},
		                    {"id":"b","parameters":{"metricId":"requests/count"}}]`))
	require.NoError(t, err)
	batchReq.Header.Set("Authorization", simARMBearer)
	batchReq.Header.Set("Content-Type", "application/json")
	batchResp, err := http.DefaultClient.Do(batchReq)
	require.NoError(t, err)
	defer batchResp.Body.Close()
	require.Equal(t, http.StatusOK, batchResp.StatusCode)
	var batch []map[string]any
	require.NoError(t, json.NewDecoder(batchResp.Body).Decode(&batch))
	require.Len(t, batch, 2)
	assert.Equal(t, "a", batch[0]["id"])
	assert.Equal(t, "b", batch[1]["id"])
}

// The same query addressed by the Azure resource rather than the workspace: a
// workspace-based component's traces carry its resource ID.
func TestSDK_LogAnalytics_QueryByResourceID(t *testing.T) {
	t.Parallel()
	rg, site := "query-by-resource-rg", "query-by-resource-site"
	ws := createLogWorkspace(t, rg, "query-by-resource-ws")
	component := createAppInsightsComponent(t, rg, "query-by-resource-app", ws.id)
	azureCreateContainerSite(t, rg, site, commandImageName, "serve 80 resource-scoped", map[string]string{
		"APPINSIGHTS_INSTRUMENTATIONKEY": *component.Properties.InstrumentationKey,
	})
	defer azureDeleteSite(rg, site)
	azureInvokeFunction(t, site)

	client, err := azquery.NewLogsClient(&fakeCredential{}, logsClientOpts())
	require.NoError(t, err)
	query := azquery.Body{Query: to.Ptr(`AppTraces | where AppRoleName == "` + site + `" | take 10`)}
	// The engine's log stream delivers the line after the response, and Log
	// Analytics offers no event for its arrival.
	require.Eventually(t, func() bool {
		resp, err := client.QueryResource(ctx, *component.ID, query, nil)
		require.NoError(t, err)
		return len(resp.Tables[0].Rows) > 0
	}, 30*time.Second, 200*time.Millisecond, "the component's traces come back when it is queried by resource id")

	byGroup, err := client.QueryResource(ctx, "/subscriptions/"+subscriptionID+"/resourceGroups/"+rg, query, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, byGroup.Tables[0].Rows, "a resource group's query reads the rows of the resources in it")
	elsewhere, err := client.QueryResource(ctx, "/subscriptions/"+subscriptionID+"/resourceGroups/query-by-resource-other-rg", query, nil)
	require.NoError(t, err)
	assert.Empty(t, elsewhere.Tables[0].Rows, "another resource group's query reads none of them")
}

// A component's billing plan decides what it is entitled to, and its quota
// status compares the telemetry it wrote against the cap it set.
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{resourceName}/featurecapabilities
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{resourceName}/getavailablebillingfeatures
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{resourceName}/quotastatus
func TestSDK_ApplicationInsights_FeaturesAndPricing(t *testing.T) {
	t.Parallel()
	rg, component := "insights-features-rg", uniquename.New("insights-features-component")
	createAppInsightsComponent(t, rg, component, "")

	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Insights/components/" + component

	// The Basic plan does not carry continuous export.
	basic := insightsRead(t, http.MethodGet, base+"/featurecapabilities?api-version=2015-05-01", "")
	assert.Equal(t, false, basic["SupportExportData"])
	assert.Equal(t, "Standard", basic["BurstThrottlePolicy"])

	// Moving to the Enterprise plan changes the capabilities, so they read the
	// plan rather than a fixed answer.
	put := insightsRead(t, http.MethodPut, base+"/currentbillingfeatures?api-version=2015-05-01",
		`{"CurrentBillingFeatures":["Application Insights Enterprise"],
		  "DataVolumeCap":{"Cap":0.5,"ResetTime":4,"WarningThreshold":90}}`)
	require.NotNil(t, put)

	enterprise := insightsRead(t, http.MethodGet, base+"/featurecapabilities?api-version=2015-05-01", "")
	assert.Equal(t, true, enterprise["SupportExportData"])
	assert.Equal(t, "Burst", enterprise["BurstThrottlePolicy"])
	assert.EqualValues(t, 0.5, enterprise["DailyCap"], "the cap the component set is the cap it reports")
	assert.EqualValues(t, 4, enterprise["DailyCapResetTime"])

	// The available features are the plans the component could be on, with its
	// current one marked.
	available := insightsRead(t, http.MethodGet,
		base+"/getavailablebillingfeatures?api-version=2015-05-01", "")
	plans, _ := available["Result"].([]any)
	require.NotEmpty(t, plans)
	main := ""
	for _, entry := range plans {
		plan, _ := entry.(map[string]any)
		if isMain, _ := plan["IsMainFeature"].(bool); isMain {
			main, _ = plan["FeatureName"].(string)
		}
	}
	assert.Equal(t, "Application Insights Enterprise", main,
		"the plan the component is on is the one marked")

	status := insightsRead(t, http.MethodGet, base+"/quotastatus?api-version=2015-05-01", "")
	created := insightsRead(t, http.MethodGet, base+"?api-version=2020-02-02", "")
	props, _ := created["properties"].(map[string]any)
	require.NotEmpty(t, props["AppId"])
	assert.Equal(t, props["AppId"], status["AppId"], "the quota status names the component by its app id")
	assert.Equal(t, component, props["ApplicationId"], "ApplicationId mirrors the component's name")
	assert.Equal(t, false, status["ShouldBeThrottled"])
	assert.NotContains(t, status, "ExpirationTime",
		"a component that is not throttled has no throttle to expire")
}
