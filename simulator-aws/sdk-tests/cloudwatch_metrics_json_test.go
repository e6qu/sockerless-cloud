package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_AwsJsonMetrics drives the CloudWatch metrics awsJson1.0 surface
// directly — the protocol current botocore / aws CLI use
// (X-Amz-Target: GraniteServiceVersion20100801.<Op>, application/x-amz-json-1.0).
// This is version-independent coverage: the CLI-driven test only exercises this
// path when the installed CLI is new enough, so assert it at the wire level.
func TestCloudWatch_AwsJsonMetrics(t *testing.T) {
	cw := func(op string, body any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, baseURL+"/", bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("X-Amz-Target", "GraniteServiceVersion20100801."+op)
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		signRawSigV4JSON(t, req, "monitoring", raw)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "op %s", op)
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}

	cw("PutMetricData", map[string]any{
		"Namespace": "MyApp/JSON",
		"MetricData": []map[string]any{{
			"MetricName": "Requests", "Value": 42, "Unit": "Count",
			"Dimensions": []map[string]any{{"Name": "svc", "Value": "api"}},
		}},
	})

	stats := cw("GetMetricStatistics", map[string]any{
		"Namespace": "MyApp/JSON", "MetricName": "Requests",
		"Dimensions": []map[string]any{{"Name": "svc", "Value": "api"}},
		"StartTime":  0, "EndTime": 0, "Period": 60,
		"Statistics": []string{"Sum", "Average"},
	})
	dps, ok := stats["Datapoints"].([]any)
	require.True(t, ok, "Datapoints present")
	require.Len(t, dps, 1)
	dp := dps[0].(map[string]any)
	// Emitted as a JSON number with a decimal so botocore reads it as a Double.
	assert.Equal(t, float64(42), dp["Sum"])
	assert.Equal(t, "Count", dp["Unit"])

	list := cw("ListMetrics", map[string]any{"Namespace": "MyApp/JSON"})
	metrics, ok := list["Metrics"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, metrics)
	found := false
	for _, m := range metrics {
		if m.(map[string]any)["MetricName"] == "Requests" {
			found = true
		}
	}
	assert.True(t, found, "ListMetrics returns the Requests metric")
}
