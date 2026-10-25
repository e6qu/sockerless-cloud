package azure_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blobPreflight sends the preflight a browser sends before a cross-origin
// request: OPTIONS with Origin and the Access-Control-Request-* headers, and
// no credential.
func blobPreflight(t *testing.T, account, target, origin, method, headers string) *http.Response {
	t.Helper()
	u, err := neturl.Parse(baseURL)
	require.NoError(t, err)
	_, port, ok := strings.Cut(u.Host, ":")
	require.True(t, ok)
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, strings.TrimRight(baseURL, "/")+target, nil)
	require.NoError(t, err)
	req.Host = fmt.Sprintf("%s.blob.localhost:%s", account, port)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)
	if headers != "" {
		req.Header.Set("Access-Control-Request-Headers", headers)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	return resp
}

// The Blob service's CORS rules are one document with two APIs onto it: a rule
// Azure Resource Manager writes is the rule the data plane reports and
// enforces, and a rule the data plane writes is the one ARM reads back.
func TestStorageSDK_BlobCORSRulesAreEnforced(t *testing.T) {
	t.Parallel()
	const account = "blobcorsrules"
	const group = "blob-cors-rg"
	createStorageAccountForARM(t, group, account)
	blobServices, err := armstorage.NewBlobServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const origin = "https://console.example.com"
	_, err = blobServices.SetServiceProperties(ctx, group, account, armstorage.BlobServiceProperties{
		BlobServiceProperties: &armstorage.BlobServicePropertiesProperties{
			Cors: &armstorage.CorsRules{CorsRules: []*armstorage.CorsRule{{
				AllowedOrigins:  []*string{to.Ptr(origin)},
				AllowedMethods:  []*armstorage.CorsRuleAllowedMethodsItem{to.Ptr(armstorage.CorsRuleAllowedMethodsItemGET), to.Ptr(armstorage.CorsRuleAllowedMethodsItemPUT)},
				AllowedHeaders:  []*string{to.Ptr("x-ms-*"), to.Ptr("content-type")},
				ExposedHeaders:  []*string{to.Ptr("x-ms-request-id")},
				MaxAgeInSeconds: to.Ptr[int32](600),
			}}},
		},
	}, nil)
	require.NoError(t, err)

	read, err := blobServices.GetServiceProperties(ctx, group, account, nil)
	require.NoError(t, err)
	require.Len(t, read.BlobServiceProperties.BlobServiceProperties.Cors.CorsRules, 1)
	assert.Equal(t, origin, *read.BlobServiceProperties.BlobServiceProperties.Cors.CorsRules[0].AllowedOrigins[0])

	client := newBlobTestClient(t, account)
	props, err := client.ServiceClient().GetProperties(ctx, nil)
	require.NoError(t, err)
	require.Len(t, props.CORS, 1)
	assert.Equal(t, origin, *props.CORS[0].AllowedOrigins)
	assert.Equal(t, "GET,PUT", *props.CORS[0].AllowedMethods)
	assert.Equal(t, int32(600), *props.CORS[0].MaxAgeInSeconds)

	allowed := blobPreflight(t, account, "/data/report.csv", origin, "PUT", "x-ms-blob-type,x-ms-version,content-type")
	assert.Equal(t, http.StatusOK, allowed.StatusCode)
	assert.Equal(t, origin, allowed.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "PUT", allowed.Header.Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "x-ms-blob-type,x-ms-version,content-type", allowed.Header.Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "600", allowed.Header.Get("Access-Control-Max-Age"))
	assert.Equal(t, "true", allowed.Header.Get("Access-Control-Allow-Credentials"))

	assert.Equal(t, http.StatusForbidden, blobPreflight(t, account, "/data/report.csv", "https://elsewhere.example.com", "PUT", "").StatusCode)
	assert.Equal(t, http.StatusForbidden, blobPreflight(t, account, "/data/report.csv", origin, "DELETE", "").StatusCode)
	refused := blobPreflight(t, account, "/data/report.csv", origin, "PUT", "authorization")
	assert.Equal(t, http.StatusForbidden, refused.StatusCode)
	assert.Equal(t, "CorsPreflightFailure", refused.Header.Get("x-ms-error-code"))

	resp := storageRawRequestWithHeaders(t, http.MethodGet, account, "blob", "/?comp=list", nil, map[string]string{"Origin": origin})
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, origin, resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "x-ms-request-id", resp.Header.Get("Access-Control-Expose-Headers"))

	_, err = client.ServiceClient().SetProperties(ctx, &service.SetPropertiesOptions{
		CORS: []*service.CORSRule{{
			AllowedOrigins:  to.Ptr("*"),
			AllowedMethods:  to.Ptr("GET"),
			AllowedHeaders:  to.Ptr("*"),
			ExposedHeaders:  to.Ptr("*"),
			MaxAgeInSeconds: to.Ptr[int32](60),
		}},
	})
	require.NoError(t, err)
	read, err = blobServices.GetServiceProperties(ctx, group, account, nil)
	require.NoError(t, err)
	require.Len(t, read.BlobServiceProperties.BlobServiceProperties.Cors.CorsRules, 1)
	assert.Equal(t, "*", *read.BlobServiceProperties.BlobServiceProperties.Cors.CorsRules[0].AllowedOrigins[0])
	wildcard := blobPreflight(t, account, "/", "https://any.example.com", "GET", "")
	assert.Equal(t, http.StatusOK, wildcard.StatusCode)
	assert.Equal(t, "*", wildcard.Header.Get("Access-Control-Allow-Origin"))
}
