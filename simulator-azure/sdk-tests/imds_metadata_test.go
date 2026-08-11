package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure IMDS instance metadata.
//
// /metadata/instance returns {compute, network} on real Azure (port 80,
// link-local 169.254.169.254). Sim serves it on its main listener so
// workloads in Docker hosts reach it via host.docker.internal:<port>.
// All reads require the `Metadata: true` request header.

func imdsRead(t *testing.T, path string, withHeader bool) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	if withHeader {
		req.Header.Set("Metadata", "true")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := readAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}

func TestIMDS_InstanceRequiresMetadataHeader(t *testing.T) {
	resp, _ := imdsRead(t, "/metadata/instance?api-version=2021-02-01", false)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIMDS_InstanceComputeAndNetwork(t *testing.T) {
	resp, body := imdsRead(t, "/metadata/instance?api-version=2021-02-01", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc struct {
		Compute struct {
			Location          string
			SubscriptionID    string `json:"subscriptionId"`
			ResourceGroupName string `json:"resourceGroupName"`
			Name              string
			AzEnvironment     string `json:"azEnvironment"`
			VMID              string `json:"vmId"`
		}
		Network struct {
			Interface []struct {
				IPv4 struct {
					IPAddress []struct {
						PrivateIPAddress string `json:"privateIpAddress"`
					} `json:"ipAddress"`
				} `json:"ipv4"`
			}
		}
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.NotEmpty(t, doc.Compute.Location)
	assert.NotEmpty(t, doc.Compute.SubscriptionID)
	assert.Equal(t, "AzurePublicCloud", doc.Compute.AzEnvironment)
	require.GreaterOrEqual(t, len(doc.Network.Interface), 1)
	require.GreaterOrEqual(t, len(doc.Network.Interface[0].IPv4.IPAddress), 1)
	assert.NotEmpty(t, doc.Network.Interface[0].IPv4.IPAddress[0].PrivateIPAddress)
}

func TestIMDS_InstanceComputeShortcut(t *testing.T) {
	resp, body := imdsRead(t, "/metadata/instance/compute?api-version=2021-02-01", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var c map[string]any
	require.NoError(t, json.Unmarshal(body, &c))
	assert.NotEmpty(t, c["location"])
	assert.NotEmpty(t, c["subscriptionId"])
}

func TestIMDS_IdentityToken(t *testing.T) {
	// Existing /metadata/identity/oauth2/token (managedidentity.go).
	// Verify it round-trips with the resource query.
	req, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/metadata/identity/oauth2/token?api-version=2018-02-01&resource=https://management.azure.com/",
		nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var tok struct {
		AccessToken string `json:"access_token"`
		Resource    string `json:"resource"`
		TokenType   string `json:"token_type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tok))
	assert.NotEmpty(t, tok.AccessToken)
	assert.Equal(t, "https://management.azure.com/", tok.Resource)
	assert.Equal(t, "Bearer", tok.TokenType)
}
