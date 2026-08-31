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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the Application Insights data plane and the two reads that
// completed the Log Analytics and instance-metadata documents:
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
//	GET /metadata/attested/document
//	GET /metadata/identity/info

// insightsRead performs one data-plane read and decodes the JSON it answers.
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

// An application's telemetry is what the application wrote, read back through
// the query, the events and the metrics of the same data plane. All three move
// when it writes, because all three read the one store.
func TestSDK_ApplicationInsights_DataPlaneReadsTheTelemetry(t *testing.T) {
	const app = "insights-dataplane-app"
	const role = "insights-dataplane-role"
	stamp := time.Now().UTC().Format(time.RFC3339)
	// The application's own telemetry, tagged with a role nothing else writes
	// so the reads below can be held to exactly these rows.
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": stamp, "Message": "first trace", "AppRoleName": role},
		{"TimeGenerated": stamp, "Message": "second trace", "AppRoleName": role},
	})

	// The query runs the KQL it is given rather than answering a fixed shape —
	// which is what it used to do, ignoring the query entirely.
	queried := insightsRead(t, http.MethodPost, "/v1/apps/"+app+"/query",
		`{"query":"AppTraces | where AppRoleName == \"insights-dataplane-role\" | take 10"}`)
	tables, _ := queried["tables"].([]any)
	require.NotEmpty(t, tables, "the query answers from the application's own telemetry")
	first, _ := tables[0].(map[string]any)
	rows, _ := first["rows"].([]any)
	assert.Len(t, rows, 2, "both traces the application wrote come back")

	// A query for a table the application wrote nothing into comes back empty,
	// which is the negative control that the rows above were really read.
	empty := insightsRead(t, http.MethodPost, "/v1/apps/"+app+"/query",
		`{"query":"AppRequests | take 10"}`)
	emptyTables, _ := empty["tables"].([]any)
	require.NotEmpty(t, emptyTables)
	emptyFirst, _ := emptyTables[0].(map[string]any)
	emptyRows, _ := emptyFirst["rows"].([]any)
	assert.Empty(t, emptyRows)

	// The GET spelling takes the same query on the query string.
	got := insightsRead(t, http.MethodGet,
		"/v1/apps/"+app+"/query?query="+url.QueryEscape(`AppTraces | where AppRoleName == "`+role+`" | take 10`), "")
	gotTables, _ := got["tables"].([]any)
	require.NotEmpty(t, gotTables)

	// The metadata describes what can be queried.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		metadata := insightsRead(t, method, "/v1/apps/"+app+"/metadata", "{}")
		assert.NotEmpty(t, metadata, "%s metadata describes the schema", method)
	}

	// The events of a type are that type's telemetry, and each carries the id
	// the single-event read addresses it by.
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

	// An id nothing matches filters everything out rather than erroring.
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

	// A metric is the telemetry counted, so it moves with what was written.
	metrics := insightsRead(t, http.MethodGet, "/v1/apps/"+app+"/metrics/traces/count", "")
	metricValue, _ := metrics["value"].(map[string]any)
	require.NotNil(t, metricValue)
	counted, _ := metricValue["traces/count"].(map[string]any)
	require.NotNil(t, counted, "the metric names itself in its own result")
	sum, _ := counted["sum"].(float64)
	assert.GreaterOrEqual(t, sum, float64(len(value)),
		"the metric counts the events the same telemetry produced")

	// The metrics metadata names the metrics an application has.
	metricMeta := insightsRead(t, http.MethodGet, "/v1/apps/"+app+"/metrics/metadata", "")
	declared, _ := metricMeta["metrics"].(map[string]any)
	assert.Contains(t, declared, "traces/count")

	// The batch answers per request id, so a caller can tell which is which.
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

// The same query, addressed by the Azure resource whose logs are being read
// rather than by the workspace they land in.
func TestSDK_LogAnalytics_QueryByResourceID(t *testing.T) {
	resourceID := "/subscriptions/" + subscriptionID +
		"/resourceGroups/query-by-resource-rg/providers/Microsoft.Web/sites/queried-site"
	stamp := time.Now().UTC().Format(time.RFC3339)
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": stamp, "Message": "resource-scoped line", "AppRoleName": "resource-scoped"},
	})

	client, err := azquery.NewLogsClient(&fakeCredential{}, logsClientOpts())
	require.NoError(t, err)
	resp, err := client.QueryResource(ctx, resourceID, azquery.Body{
		Query: to.Ptr(`AppTraces | where AppRoleName == "resource-scoped" | take 10`),
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tables)
	require.NotEmpty(t, resp.Tables[0].Rows,
		"the resource's own logs come back when it is queried by resource id")
}

// The instance metadata service attests the instance it is asked on, and names
// the tenant its managed identity belongs to.
func TestSDK_InstanceMetadata_AttestationAndIdentity(t *testing.T) {
	read := func(path string) (*http.Response, map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Metadata", "true")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != http.StatusOK {
			return resp, nil
		}
		decoded := map[string]any{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
		return resp, decoded
	}

	_, attested := read("/metadata/attested/document?api-version=2021-02-01&nonce=abc123")
	require.NotNil(t, attested)
	assert.Equal(t, "pkcs7", attested["encoding"])
	signature, _ := attested["signature"].(string)
	require.NotEmpty(t, signature)

	// The nonce is inside what was signed, so a document minted for one
	// challenge cannot answer another.
	_, other := read("/metadata/attested/document?api-version=2021-02-01&nonce=zzz999")
	otherSignature, _ := other["signature"].(string)
	assert.NotEqual(t, signature, otherSignature,
		"the caller's nonce must change the document that was signed")

	_, identity := read("/metadata/identity/info?api-version=2021-02-01")
	require.NotNil(t, identity)
	assert.NotEmpty(t, identity["tenantId"])

	// Every instance-metadata read requires the header, which is what stops a
	// browser being tricked into making one.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/metadata/identity/info?api-version=2021-02-01", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
