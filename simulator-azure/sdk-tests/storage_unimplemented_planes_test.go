package azure_sdk_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/require"
)

// storageEndpointRequest issues a request at one of the data-plane endpoints an
// ARM storage account advertises. The endpoint hostname is carried in the Host
// header — `*.localhost` names do not resolve on every host the suite runs on,
// so the connection goes to the simulator's address and the host header is what
// selects the data plane, exactly as it does behind a resolver.
func storageEndpointRequest(t *testing.T, method, endpoint, target string) *http.Response {
	t.Helper()
	host := strings.TrimSuffix(strings.TrimPrefix(endpoint, "http://"), "/")
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+target, nil)
	require.NoError(t, err)
	req.Host = host
	req.Header.Set("x-ms-version", "2025-01-05")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestStorageSDK_UnimplementedDataPlanesDeclareGap follows the storage
// account's own advertised endpoints. A storage account publishes six data
// planes and the simulator serves four; the static website and Azure Data Lake
// Storage Gen2 endpoints answer with the declared gap rather than a success, so
// a client that follows the advertised URL is told the simulator does not
// implement that plane instead of being handed an empty 200.
func TestStorageSDK_UnimplementedDataPlanesDeclareGap(t *testing.T) {
	const (
		rg      = "sdk-storage-endpoints-rg"
		account = "sdkendpointacct"
	)
	ensureRG(t, rg)

	accountsClient, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := accountsClient.BeginCreate(ctx, rg, account, armstorage.AccountCreateParameters{
		Location: to.Ptr("eastus"),
		SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		Kind:     to.Ptr(armstorage.KindStorageV2),
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.PrimaryEndpoints)

	endpoints := created.Properties.PrimaryEndpoints
	require.NotNil(t, endpoints.Web, "the storage account must advertise a static website endpoint")
	require.NotNil(t, endpoints.Dfs, "the storage account must advertise a Data Lake Storage Gen2 endpoint")

	for _, tc := range []struct {
		name     string
		endpoint string
		method   string
		target   string
	}{
		{"static website root", *endpoints.Web, http.MethodGet, "/"},
		{"static website page", *endpoints.Web, http.MethodGet, "/index.html"},
		{"Data Lake Storage Gen2 account", *endpoints.Dfs, http.MethodGet, "/?resource=account"},
		{"Data Lake Storage Gen2 filesystem", *endpoints.Dfs, http.MethodPut, "/fs?resource=filesystem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := storageEndpointRequest(t, tc.method, tc.endpoint, tc.target)
			assertStorageXMLError(t, resp, http.StatusNotImplemented, "NotImplemented")
		})
	}

	// The four served planes answer the same account's endpoints for real: an
	// absent share is ShareNotFound, not a gap.
	require.NotNil(t, endpoints.File)
	resp := storageEndpointRequest(t, http.MethodGet, *endpoints.File, "/absent-share?restype=share")
	assertStorageXMLError(t, resp, http.StatusNotFound, "ShareNotFound")

	_, err = accountsClient.Delete(ctx, rg, account, nil)
	require.NoError(t, err, fmt.Sprintf("delete storage account %s", account))
}
