package gcp_sdk_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	discovery "google.golang.org/api/discovery/v1"
)

// fetchDiscoveryDocument sends GET /$discovery/rest?version=… to the
// simulator under host, as a client whose requests reach it through a proxy
// names the API's own host.
func fetchDiscoveryDocument(t *testing.T, host, version string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/$discovery/rest?version="+url.QueryEscape(version), nil)
	require.NoError(t, err)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

// Every API the simulator implements serves the Discovery document vendored
// for it, byte for byte, under the host its rootUrl names; the document reads
// as the Discovery service's RestDescription.
//
//	GET /$discovery/rest
func TestDiscovery_EachAPIServesItsVendoredDocument(t *testing.T) {
	files, err := filepath.Glob("../../specs/cloud-api/gcp/*.discovery.json.gz")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		compressed, err := os.ReadFile(file)
		require.NoError(t, err)
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		require.NoError(t, err)
		vendored, err := io.ReadAll(reader)
		require.NoError(t, err)
		var want discovery.RestDescription
		require.NoError(t, json.Unmarshal(vendored, &want), file)
		root, err := url.Parse(want.RootUrl)
		require.NoError(t, err, file)

		status, body := fetchDiscoveryDocument(t, root.Host, want.Version)
		require.Equal(t, http.StatusOK, status, "%s version %s: %s", root.Host, want.Version, body)
		require.True(t, bytes.Equal(vendored, body), "%s version %s served a document other than %s", root.Host, want.Version, filepath.Base(file))
		var got discovery.RestDescription
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, want.Name, got.Name)
		assert.Equal(t, want.Revision, got.Revision)
	}

	// The bq CLI asks the API root it is given for v2; at a bare origin
	// that is BigQuery's.
	status, body := fetchDiscoveryDocument(t, "", "v2")
	require.Equal(t, http.StatusOK, status)
	var bq discovery.RestDescription
	require.NoError(t, json.Unmarshal(body, &bq))
	assert.Equal(t, "bigquery", bq.Name)

	status, _ = fetchDiscoveryDocument(t, "run.googleapis.com", "v9")
	assert.Equal(t, http.StatusNotFound, status, "a version the API does not publish has no document")
}
