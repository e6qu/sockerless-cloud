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
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
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
// for it, byte for byte, under the host its name names; the document reads as
// the Discovery service's RestDescription.
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
		host := want.Name + ".googleapis.com"
		status, body := fetchDiscoveryDocument(t, host, want.Version)
		require.Equal(t, http.StatusOK, status, "%s version %s: %s", host, want.Version, body)
		require.True(t, bytes.Equal(vendored, body), "%s version %s served a document other than %s", host, want.Version, filepath.Base(file))
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

// The Discovery service's directory lists the APIs the simulator serves and
// hands out their documents, to a client that presents no credential.
//
//	GET /discovery/v1/apis
//	GET /discovery/v1/apis/{api}/{version}/rest
func TestDiscovery_DirectoryListAndGetRest(t *testing.T) {
	svc, err := discovery.NewService(ctx, option.WithEndpoint(baseURL+"/discovery/v1/"), option.WithoutAuthentication())
	require.NoError(t, err)

	all, err := svc.Apis.List().Do()
	require.NoError(t, err)
	assert.Equal(t, "discovery#directoryList", all.Kind)
	ids := map[string]bool{}
	for _, item := range all.Items {
		ids[item.Id] = true
	}
	assert.True(t, ids["compute:v1"] && ids["run:v2"] && ids["discovery:v1"], "the directory lists the served APIs: %v", ids)

	run, err := svc.Apis.List().Name("run").Preferred(true).Do()
	require.NoError(t, err)
	require.Len(t, run.Items, 1)
	assert.Equal(t, "run:v2", run.Items[0].Id)
	assert.Equal(t, "https://run.googleapis.com/$discovery/rest?version=v2", run.Items[0].DiscoveryRestUrl)
	assert.True(t, run.Items[0].Preferred)

	doc, err := svc.Apis.GetRest("compute", "v1").Do()
	require.NoError(t, err)
	assert.Equal(t, "compute:v1", doc.Id)
	assert.Equal(t, "https://compute.googleapis.com/", doc.RootUrl)

	_, err = svc.Apis.GetRest("run", "v2").Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
	assert.Equal(t, "Requested entity was not found.", apiErr.Message)
}
