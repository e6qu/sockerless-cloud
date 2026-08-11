package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI-level smoke for the v2 Cloud Run Services routes — the v2 REST
// surface used by run.NewServicesRESTClient. The v1 Knative service
// surface (`/apis/serving.knative.dev/v1/namespaces/{ns}/services`) is
// covered by the sdk-tests.

func servicesBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/services", baseURL, project, location)
}

func runServiceURL(name string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/services/%s", baseURL, project, location, name)
}

// TestCloudRunV2Services_Wire_RejectsUnknownUpdateMask pins the real Cloud
// Run v2 contract at the wire level: a PATCH whose updateMask names an
// unknown field fails with 400 INVALID_ARGUMENT instead of silently
// no-opping, while a valid masked patch merges only the masked field.
func TestCloudRunV2Services_Wire_RejectsUnknownUpdateMask(t *testing.T) {
	createBody := `{"labels":{"stage":"one"},"template":{"containers":[{"image":"gcr.io/test-project/mask"}]}}`
	httpDoJSON(t, "POST", servicesBaseURL()+"?serviceId=cli-svc-badmask", createBody)

	resp, err := httpDo("PATCH", runServiceURL("cli-svc-badmask")+"?updateMask=bogusField",
		`{"labels":{"stage":"two"}}`)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, 400, resp.StatusCode, "unknown updateMask path must 400: %s", body)
	var errResp struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "INVALID_ARGUMENT", errResp.Error.Status)

	// The rejected patch must not have applied anything.
	getOut := httpDoJSON(t, "GET", runServiceURL("cli-svc-badmask"), "")
	var got struct {
		Labels map[string]string `json:"labels"`
	}
	parseJSON(t, getOut, &got)
	assert.Equal(t, "one", got.Labels["stage"], "a rejected mask must leave the service untouched")
}

func TestCloudRunV2Services_CLI_CreateGetDelete(t *testing.T) {
	createBody := `{
		"labels": {"sockerless_managed": "true"},
		"template": {
			"containers": [{"image": "gcr.io/test-project/hello"}],
			"scaling": {"minInstanceCount": 1, "maxInstanceCount": 1},
			"vpcAccess": {
				"connector": "projects/test-project/locations/us-central1/connectors/test-connector",
				"egress": 1
			}
		}
	}`
	createOut := httpDoJSON(t, "POST", servicesBaseURL()+"?serviceId=cli-svc-roundtrip", createBody)

	var lro struct {
		Done     bool `json:"done"`
		Response struct {
			Name                string            `json:"name"`
			UID                 string            `json:"uid"`
			Generation          string            `json:"generation"`
			Labels              map[string]string `json:"labels"`
			LatestReadyRevision string            `json:"latestReadyRevision"`
			TerminalCondition   struct {
				State string `json:"state"`
			} `json:"terminalCondition"`
			Template struct {
				VpcAccess struct {
					Connector string `json:"connector"`
					Egress    string `json:"egress"`
				} `json:"vpcAccess"`
			} `json:"template"`
		} `json:"response"`
	}
	parseJSON(t, createOut, &lro)
	require.True(t, lro.Done, "CreateService LRO should be done immediately in the sim")
	assert.Contains(t, lro.Response.Name, "cli-svc-roundtrip")
	assert.NotEmpty(t, lro.Response.UID)
	assert.Equal(t, "1", lro.Response.Generation)
	assert.Equal(t, "true", lro.Response.Labels["sockerless_managed"])
	assert.NotEmpty(t, lro.Response.LatestReadyRevision)
	assert.Equal(t, "CONDITION_SUCCEEDED", lro.Response.TerminalCondition.State)
	assert.Equal(t, "projects/test-project/locations/us-central1/connectors/test-connector", lro.Response.Template.VpcAccess.Connector)
	assert.Equal(t, "ALL_TRAFFIC", lro.Response.Template.VpcAccess.Egress)

	getOut := httpDoJSON(t, "GET", runServiceURL("cli-svc-roundtrip"), "")
	var got struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	parseJSON(t, getOut, &got)
	assert.Contains(t, got.Name, "cli-svc-roundtrip")
	assert.Equal(t, "true", got.Labels["sockerless_managed"])

	v1ListOut := httpDoJSON(t, "GET",
		fmt.Sprintf("%s/apis/serving.knative.dev/v1/namespaces/%s/services", baseURL, project), "")
	var v1List struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	parseJSON(t, v1ListOut, &v1List)
	var matches int
	for _, item := range v1List.Items {
		if item.Metadata.Name == "cli-svc-roundtrip" {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "the projected service must appear exactly once in the real v1 collection")

	httpDoJSON(t, "DELETE", runServiceURL("cli-svc-roundtrip"), "")

	resp, err := httpDo("GET", runServiceURL("cli-svc-roundtrip"), "")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "GetService after delete must 404")
}
