package azure_sdk_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func simHostParts(t *testing.T) (string, string) {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	return u.Hostname(), u.Host
}

func armReq(t *testing.T, method, path string, body string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != "" {
		br = bytes.NewReader([]byte(body))
	}
	if !strings.Contains(path, "api-version=") {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "api-version=2025-01-01"
	}
	req, err := http.NewRequest(method, baseURL+path, br)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func waitAzureAsyncOperation(t *testing.T, opURL string) {
	t.Helper()
	// Generous deadline so a loaded CI runner doesn't expire before the async
	// operation reports Succeeded; the loop still returns immediately on success.
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opURL, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", simARMBearer)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		if strings.Contains(string(body), `"status":"Succeeded"`) {
			return
		}
		require.True(t, time.Now().Before(deadline), "operation did not succeed: %s", string(body))
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAzureRedisCache_ARMLifecycle exercises CRUD on the
// Microsoft.Cache/Redis ARM surface.
func TestAzureRedisCache_ARMLifecycle(t *testing.T) {
	sub := "00000000-0000-0000-0000-000000000000"
	rg := "test-rg"
	name := "lifecycle-redis"
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/%s", sub, rg, name)

	resp := armReq(t, "PUT", path, `{"location":"eastus","properties":{"sku":{"name":"Standard","family":"C","capacity":1}}}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	opURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, opURL)
	assert.Equal(t, "0", resp.Header.Get("Retry-After"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"provisioningState":"Creating"`)
	host, _ := simHostParts(t)
	assert.Contains(t, string(body), `"hostName":"lifecycle-redis.redis.cache.`+host+`"`)

	waitAzureAsyncOperation(t, opURL)

	resp = armReq(t, "GET", path, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"provisioningState":"Succeeded"`)

	listPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis", sub, rg)
	resp = armReq(t, "GET", listPath, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(listBody), name)

	resp = armReq(t, "DELETE", path, "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	resp = armReq(t, "GET", path, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestAzurePGFlexibleServer_ARMLifecycle exercises Postgres
// FlexibleServer + Database + FirewallRule.
func TestAzurePGFlexibleServer_ARMLifecycle(t *testing.T) {
	sub := "00000000-0000-0000-0000-000000000000"
	rg := "test-rg"
	name := "lifecycle-pg"
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DBforPostgreSQL/flexibleServers/%s",
		sub, rg, name)

	// Servers_Create declares 202-only (no success body): the caller
	// polls Azure-AsyncOperation, then reads the server back via GET.
	resp := armReq(t, "PUT", armBase, `{"location":"eastus","properties":{"version":"15","administratorLogin":"psqladmin"}}`)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	opURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, opURL)
	require.NotEmpty(t, resp.Header.Get("Location"))
	assert.Equal(t, "0", resp.Header.Get("Retry-After"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Empty(t, string(body), "PUT flexibleServers must not return a success body")

	waitAzureAsyncOperation(t, opURL)

	resp = armReq(t, "GET", armBase, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"state":"Ready"`)
	host, _ := simHostParts(t)
	assert.Contains(t, string(body), `"fullyQualifiedDomainName":"lifecycle-pg.postgres.database.`+host+`"`)

	// Create a database — same 202 + poll choreography.
	dbPath := armBase + "/databases/appdb"
	resp = armReq(t, "PUT", dbPath, `{"properties":{"charset":"UTF8"}}`)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	dbOpURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, dbOpURL)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Empty(t, string(body), "PUT databases must not return a success body")
	waitAzureAsyncOperation(t, dbOpURL)

	resp = armReq(t, "GET", armBase+"/databases", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(listBody), "appdb")

	// Create a firewall rule — same 202 + poll choreography.
	frPath := armBase + "/firewallRules/AllowAzureServices"
	resp = armReq(t, "PUT", frPath, `{"properties":{"startIpAddress":"0.0.0.0","endIpAddress":"0.0.0.0"}}`)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	frOpURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, frOpURL)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Empty(t, string(body), "PUT firewallRules must not return a success body")
	waitAzureAsyncOperation(t, frOpURL)

	resp = armReq(t, "GET", frPath, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	frBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(frBody), "AllowAzureServices")

	// Delete server (cascade) — 202 with the same poll headers.
	resp = armReq(t, "DELETE", armBase, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("Azure-AsyncOperation"))
	resp.Body.Close()

	// Confirm database + firewall rule were cascaded.
	resp = armReq(t, "GET", dbPath, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.True(t, strings.Contains(string(body), "not found"),
		"database should be cascade-deleted; got %s", string(body))
}
