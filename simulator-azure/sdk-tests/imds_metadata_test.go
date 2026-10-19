package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure IMDS instance metadata. Every read requires the `Metadata: true`
// request header, and only a virtual machine is answered; the reads a
// machine makes are in compute_vm_instances_test.go.

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
	t.Parallel()
	resp, _ := imdsRead(t, "/metadata/instance?api-version=2021-02-01", false)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// The Instance Metadata Service answers a virtual machine and nothing else.
func TestIMDS_RefusesACallerThatIsNoMachine(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/metadata/instance?api-version=2021-02-01",
		"/metadata/instance/compute?api-version=2021-02-01",
		"/metadata/instance/network?api-version=2021-02-01",
		"/metadata/attested/document?api-version=2020-09-01",
	} {
		resp, _ := imdsRead(t, path, true)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}

func TestIMDS_IdentityToken(t *testing.T) {
	t.Parallel()
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
