package gcp_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func vpcAccessURL(name string) string {
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/connectors/%s",
		baseURL, project, location, name)
}

func vpcAccessBaseURL() string {
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/connectors",
		baseURL, project, location)
}

func TestVPCAccess_CreateAndDescribe(t *testing.T) {
	t.Parallel()
	url := vpcAccessBaseURL() + "?connectorId=cli-test-connector"
	out := httpDoJSON(t, "POST", url, `{
		"network": "default",
		"ipCidrRange": "10.8.0.0/28"
	}`)

	var op struct {
		Done bool `json:"done"`
	}
	parseJSON(t, out, &op)

	getURL := vpcAccessURL("cli-test-connector")
	out = httpDoJSON(t, "GET", getURL, "")

	var connector struct {
		Name        string `json:"name"`
		Network     string `json:"network"`
		IpCidrRange string `json:"ipCidrRange"`
		State       string `json:"state"`
	}
	parseJSON(t, out, &connector)
	assert.Contains(t, connector.Name, "cli-test-connector")
	assert.Equal(t, "default", connector.Network)
	assert.Equal(t, "10.8.0.0/28", connector.IpCidrRange)
	assert.Equal(t, "READY", connector.State)

	httpDoJSON(t, "DELETE", getURL, "")
}

func TestVPCAccess_List(t *testing.T) {
	t.Parallel()
	url := vpcAccessBaseURL() + "?connectorId=list-test-connector"
	httpDoJSON(t, "POST", url, `{
		"network": "my-network",
		"ipCidrRange": "10.9.0.0/28"
	}`)

	out := httpDoJSON(t, "GET", vpcAccessBaseURL(), "")

	var result struct {
		Connectors []struct {
			Name string `json:"name"`
		} `json:"connectors"`
	}
	parseJSON(t, out, &result)
	require.NotEmpty(t, result.Connectors)

	httpDoJSON(t, "DELETE", vpcAccessURL("list-test-connector"), "")
}

// TestVPCAccess_CreateRequiresNetworkOrSubnet: Serverless VPC Access answers 400
// INVALID_ARGUMENT to a connector naming neither network and ipCidrRange nor a
// subnet.
func TestVPCAccess_CreateRequiresNetworkOrSubnet(t *testing.T) {
	t.Parallel()
	url := vpcAccessBaseURL() + "?connectorId=invalid-connector"
	resp, err := httpDo("POST", url, `{}`)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestVPCAccess_ListPagination(t *testing.T) {
	t.Parallel()
	ids := []string{"page-conn-a", "page-conn-b", "page-conn-c"}
	for _, id := range ids {
		httpDoJSON(t, "POST", vpcAccessBaseURL()+"?connectorId="+id,
			`{"network":"default","ipCidrRange":"10.20.0.0/28"}`)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			httpDoJSON(t, "DELETE", vpcAccessURL(id), "")
		}
	})

	seen := map[string]bool{}
	token := ""
	pages := 0
	for {
		url := vpcAccessBaseURL() + "?pageSize=1"
		if token != "" {
			url += "&pageToken=" + token
		}
		out := httpDoJSON(t, "GET", url, "")
		var result struct {
			Connectors []struct {
				Name string `json:"name"`
			} `json:"connectors"`
			NextPageToken string `json:"nextPageToken"`
		}
		parseJSON(t, out, &result)
		require.LessOrEqual(t, len(result.Connectors), 1, "pageSize=1 must cap the page")
		for _, c := range result.Connectors {
			seen[c.Name] = true
		}
		pages++
		require.Less(t, pages, 50, "pagination must terminate")
		token = result.NextPageToken
		if token == "" {
			break
		}
	}
	for _, id := range ids {
		found := false
		for n := range seen {
			if strings.HasSuffix(n, "/"+id) {
				found = true
			}
		}
		assert.True(t, found, "connector %s must appear across pages", id)
	}
}

func TestVPCAccess_Delete(t *testing.T) {
	t.Parallel()
	url := vpcAccessBaseURL() + "?connectorId=delete-test-connector"
	httpDoJSON(t, "POST", url, `{
		"network": "default",
		"ipCidrRange": "10.10.0.0/28"
	}`)

	httpDoJSON(t, "DELETE", vpcAccessURL("delete-test-connector"), "")

	resp, err := httpDo("GET", vpcAccessURL("delete-test-connector"), "")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
}

// Serverless VPC Access lists the operations it started from its own host.
// Eventarc publishes the same projects.locations.operations path, and its
// operations stay in Eventarc's collection.
func TestVPCAccess_CLI_OperationsListOnlyItsOwnOperations(t *testing.T) {
	t.Parallel()
	runCLI(t, gcloudCLI("eventarc", "channels", "create", "ops-scope-channel",
		"--location", location,
		"--provider", "projects/"+project+"/locations/"+location+"/providers/cloud.pubsub",
		"--format", "json"))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("eventarc", "channels", "delete", "ops-scope-channel", "--location", location, "--quiet"))
	})
	var created struct {
		Name string `json:"name"`
	}
	parseJSON(t, httpDoJSON(t, "POST", vpcAccessBaseURL()+"?connectorId=ops-scope-connector",
		`{"network": "default", "ipCidrRange": "10.8.80.0/28"}`), &created)
	t.Cleanup(func() { httpDoJSON(t, "DELETE", vpcAccessURL("ops-scope-connector"), "") })

	var listed []struct {
		Name     string         `json:"name"`
		Metadata map[string]any `json:"metadata"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("compute", "networks", "vpc-access", "operations", "list",
		"--region", location, "--format", "json")), &listed)
	var names []string
	for _, op := range listed {
		names = append(names, op.Name)
		typeURL, _ := op.Metadata["@type"].(string)
		assert.True(t, strings.HasPrefix(typeURL, "type.googleapis.com/google.cloud.vpcaccess.v1."),
			"Serverless VPC Access listed %s, whose metadata is %s", op.Name, typeURL)
	}
	assert.Contains(t, names, created.Name, "Serverless VPC Access did not list the operation it started")
}
