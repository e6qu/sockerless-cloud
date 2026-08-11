package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/monitor/azquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logsClientOpts points the azquery Logs client at the simulator's Log
// Analytics data plane (https://api.loganalytics.io/v1). azquery v1.2.0 builds
// its internal BearerTokenPolicy with nil options, so it refuses any non-HTTPS
// endpoint regardless of InsecureAllowCredentialWithHTTP; the client URL must
// therefore advertise HTTPS while kvSchemeRewritingTransport rewrites the scheme
// down to the sim's plain-HTTP listener at request time.
func logsClientOpts() *azquery.LogsClientOptions {
	httpsEndpoint := "https://" + strings.TrimPrefix(baseURL, "http://") + "/v1"
	return &azquery.LogsClientOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloud.Configuration{
				Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					azquery.ServiceNameLogs: {
						Endpoint: httpsEndpoint,
						Audience: "https://api.loganalytics.io",
					},
				},
			},
			Transport: kvSchemeRewritingTransport{},
		},
	}
}

// TestLogAnalytics_QueryWorkspaceSDK runs a KQL query through the azquery Logs
// client (POST /v1/workspaces/{workspaceId}/query).
func TestLogAnalytics_QueryWorkspaceSDK(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339)
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": ts, "ContainerGroupName_s": "azqueryjob", "Log_s": "hello from azquery", "Stream_s": "stdout"},
	})

	client, err := azquery.NewLogsClient(&fakeCredential{}, logsClientOpts())
	require.NoError(t, err)

	resp, err := client.QueryWorkspace(ctx, "default", azquery.Body{
		Query: to.Ptr(`ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "azqueryjob" | take 10`),
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Tables)
	assert.Equal(t, "PrimaryResult", *resp.Tables[0].Name)
	require.NotEmpty(t, resp.Tables[0].Rows)
}

// TestLogAnalytics_QueryBatchSDK runs a batch of queries through the azquery
// Logs client (POST /v1/$batch).
func TestLogAnalytics_QueryBatchSDK(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339)
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": ts, "ContainerGroupName_s": "batchjob", "Log_s": "batch line", "Stream_s": "stdout"},
	})

	client, err := azquery.NewLogsClient(&fakeCredential{}, logsClientOpts())
	require.NoError(t, err)

	req := azquery.NewBatchQueryRequest("default",
		`ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "batchjob" | take 10`,
		azquery.TimeInterval("PT1H"), "req-1", azquery.LogsQueryOptions{})
	resp, err := client.QueryBatch(ctx, azquery.BatchRequest{Requests: []*azquery.BatchQueryRequest{&req}}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Responses)
	assert.Equal(t, "req-1", *resp.Responses[0].CorrelationID)
	require.NotNil(t, resp.Responses[0].Body)
	require.NotEmpty(t, resp.Responses[0].Body.Tables)
}

// TestLogAnalytics_GetQueryAndMetadataREST exercises the GET query overload and
// the workspace metadata endpoints, which the official azquery SDK does not
// wrap but which `az monitor log-analytics` and the data-plane REST API call.
func TestLogAnalytics_GetQueryAndMetadataREST(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339)
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": ts, "ContainerGroupName_s": "getjob", "Log_s": "get line", "Stream_s": "stdout"},
	})

	// GET /v1/workspaces/{workspaceId}/query
	getResp := loganalyticsGET(t, "/v1/workspaces/default/query?query="+
		"ContainerAppConsoleLogs_CL%20%7C%20where%20ContainerGroupName_s%20%3D%3D%20%22getjob%22")
	var qr struct {
		Tables []struct {
			Rows [][]any `json:"rows"`
		} `json:"tables"`
	}
	require.NoError(t, json.Unmarshal(getResp, &qr))
	require.NotEmpty(t, qr.Tables)
	require.NotEmpty(t, qr.Tables[0].Rows)

	// GET /v1/workspaces/{workspaceId}/metadata
	metaResp := loganalyticsGET(t, "/v1/workspaces/default/metadata")
	var meta struct {
		Tables []struct {
			Name    string `json:"name"`
			Columns []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"columns"`
		} `json:"tables"`
	}
	require.NoError(t, json.Unmarshal(metaResp, &meta))
	require.NotEmpty(t, meta.Tables)
	require.NotEmpty(t, meta.Tables[0].Columns)

	// POST /v1/workspaces/{workspaceId}/metadata returns the same shape.
	postReq, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/workspaces/default/metadata", nil)
	postResp, err := http.DefaultClient.Do(postReq)
	require.NoError(t, err)
	defer postResp.Body.Close()
	require.Equal(t, http.StatusOK, postResp.StatusCode)
}

func loganalyticsGET(t *testing.T, path string) []byte {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, _ := io.ReadAll(resp.Body)
	return data
}
