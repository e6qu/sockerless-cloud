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
	url := vpcAccessBaseURL() + "?connectorId=cli-test-connector"
	out := httpDoJSON(t, "POST", url, `{
		"network": "default",
		"ipCidrRange": "10.8.0.0/28"
	}`)

	// Create returns an operation
	var op struct {
		Done bool `json:"done"`
	}
	parseJSON(t, out, &op)

	// GET the connector
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

	// Cleanup
	httpDoJSON(t, "DELETE", getURL, "")
}

func TestVPCAccess_List(t *testing.T) {
	url := vpcAccessBaseURL() + "?connectorId=list-test-connector"
	httpDoJSON(t, "POST", url, `{
		"network": "my-network",
		"ipCidrRange": "10.9.0.0/28"
	}`)

	// List connectors
	out := httpDoJSON(t, "GET", vpcAccessBaseURL(), "")

	var result struct {
		Connectors []struct {
			Name string `json:"name"`
		} `json:"connectors"`
	}
	parseJSON(t, out, &result)
	require.NotEmpty(t, result.Connectors)

	// Cleanup
	httpDoJSON(t, "DELETE", vpcAccessURL("list-test-connector"), "")
}

// TestVPCAccess_CreateRequiresNetworkOrSubnet asserts the sim rejects a
// connector that specifies neither (network + ipCidrRange) nor a subnet, the
// way real Cloud VPC Access does (INVALID_ARGUMENT / 400).
func TestVPCAccess_CreateRequiresNetworkOrSubnet(t *testing.T) {
	url := vpcAccessBaseURL() + "?connectorId=invalid-connector"
	resp, err := httpDo("POST", url, `{}`)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

// TestVPCAccess_ListPagination asserts the List handler honors pageSize +
// pageToken instead of dumping the full set.
func TestVPCAccess_ListPagination(t *testing.T) {
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
	url := vpcAccessBaseURL() + "?connectorId=delete-test-connector"
	httpDoJSON(t, "POST", url, `{
		"network": "default",
		"ipCidrRange": "10.10.0.0/28"
	}`)

	// Delete
	httpDoJSON(t, "DELETE", vpcAccessURL("delete-test-connector"), "")

	// Verify gone
	resp, err := httpDo("GET", vpcAccessURL("delete-test-connector"), "")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
}
