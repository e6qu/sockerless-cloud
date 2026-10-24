package azure_sdk_test

import (
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
	} {
		resp, _ := imdsRead(t, path, true)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}
