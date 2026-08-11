package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorageAccount_PatchMergesProperties pins that a storage-account PATCH
// merges the updatable nested properties it carries into the stored account and
// preserves unlisted ones — real-Azure behavior. Before the fix the PATCH read
// `properties` but applied only sku/kind/tags, silently dropping property
// updates (e.g. allowBlobPublicAccess) → terraform drift on the next plan.
func TestStorageAccount_PatchMergesProperties(t *testing.T) {
	const rg = "storage-patch-rg"
	const acct = "stpatchmerge01"
	doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=2023-07-01", baseURL, subscriptionID, rg),
		`{"location":"eastus"}`)
	base := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s?api-version=2023-01-01",
		baseURL, subscriptionID, rg, acct)

	// Create: accessTier Hot + allowBlobPublicAccess true.
	doPUT(t, base, `{"location":"eastus","sku":{"name":"Standard_LRS"},"kind":"StorageV2","properties":{"accessTier":"Hot","allowBlobPublicAccess":true}}`)

	// PATCH only allowBlobPublicAccess → false; accessTier must be preserved.
	req, _ := http.NewRequestWithContext(ctx, "PATCH", base, strings.NewReader(`{"properties":{"allowBlobPublicAccess":false}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Less(t, resp.StatusCode, 400, "PATCH failed: %d", resp.StatusCode)

	// GET and assert the merge.
	gr, _ := http.NewRequestWithContext(ctx, "GET", base, nil)
	gr.Header.Set("Authorization", simARMBearer)
	gresp, err := http.DefaultClient.Do(gr)
	require.NoError(t, err)
	body, _ := io.ReadAll(gresp.Body)
	gresp.Body.Close()

	var got struct {
		Properties struct {
			AccessTier            string `json:"accessTier"`
			AllowBlobPublicAccess *bool  `json:"allowBlobPublicAccess"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	require.NotNil(t, got.Properties.AllowBlobPublicAccess)
	assert.False(t, *got.Properties.AllowBlobPublicAccess, "patched property updated")
	assert.Equal(t, "Hot", got.Properties.AccessTier, "unlisted property preserved across PATCH")
}
