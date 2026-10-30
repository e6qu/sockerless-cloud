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

// Every instance-metadata read requires the header, which is what stops a
// browser being tricked into making one.
func TestIMDS_InstanceRequiresMetadataHeader(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/metadata/instance?api-version=2021-02-01",
		"/metadata/identity/info?api-version=2021-02-01",
	} {
		resp, _ := imdsRead(t, path, false)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
	}
}

// The Instance Metadata Service answers a virtual machine and nothing else.
func TestIMDS_RefusesACallerThatIsNoMachine(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/metadata/instance?api-version=2021-02-01",
		"/metadata/instance/compute?api-version=2021-02-01",
		"/metadata/instance/network?api-version=2021-02-01",
		"/metadata/attested/document?api-version=2020-09-01",
		"/metadata/instance/compute/vmId?api-version=2021-02-01&format=text",
		"/metadata/loadbalancer?api-version=2020-10-01",
		"/metadata/scheduledevents?api-version=2020-07-01",
		"/metadata/versions",
	} {
		resp, _ := imdsRead(t, path, true)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}

// A request without one of the versions an endpoint serves is refused before
// anything else, with the newest versions in the body.
func TestIMDS_RefusesAnAbsentOrUnservedAPIVersion(t *testing.T) {
	t.Parallel()
	for path, newest := range map[string][]any{
		"/metadata/instance": {"2025-04-07", "2024-07-17", "2024-03-15"},
		"/metadata/instance/compute?api-version=1999-01-01": {"2025-04-07", "2024-07-17", "2024-03-15"},
		"/metadata/loadbalancer?api-version=2020-09-01":     {"2025-04-07", "2024-07-17", "2024-03-15"},
		"/metadata/scheduledevents":                         {"2020-07-01", "2019-08-01", "2019-04-01"},
	} {
		resp, body := imdsRead(t, path, true)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
		var refusal map[string]any
		require.NoError(t, json.Unmarshal(body, &refusal), string(body))
		assert.Equal(t, "Bad request. api-version is invalid or was not specified in the request. For more information refer to aka.ms/azureimds", refusal["error"], path)
		assert.Equal(t, newest, refusal["newest-versions"], path)
	}
}
