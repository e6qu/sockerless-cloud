package azure_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureServiceBus_ARMLifecycle covers namespace, queue, topic and
// subscription CRUD.
func TestAzureServiceBus_ARMLifecycle(t *testing.T) {
	t.Parallel()
	sub := subscriptionID
	rg := "test-rg-cea0"
	ns := uniquename.New("lifecycle-sb")
	ensureRGIn(t, sub, rg)
	nsPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ServiceBus/namespaces/%s", sub, rg, ns)

	resp := armReq(t, "PUT", nsPath, `{"location":"eastus","sku":{"name":"Standard","tier":"Standard"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"provisioningState":"Succeeded"`)
	port := strings.TrimPrefix(baseURL, "http://127.0.0.1:")
	assert.Contains(t, string(body), ns+".servicebus.shim.localhost:"+port)

	resp = armReq(t, "POST", nsPath+"/authorizationRules/RootManageSharedAccessKey/listKeys", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	keysBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(keysBody), "Endpoint=sb://"+ns+".servicebus.shim.localhost:"+port+"/")

	qPath := nsPath + "/queues/myqueue"
	resp = armReq(t, "PUT", qPath, `{"properties":{"maxSizeInMegabytes":2048}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	deleteQPath := nsPath + "/queues/deletequeue"
	resp = armReq(t, "PUT", deleteQPath, `{"properties":{"maxSizeInMegabytes":1024}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	resp = armReq(t, "DELETE", deleteQPath, "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	tPath := nsPath + "/topics/mytopic"
	resp = armReq(t, "PUT", tPath, `{"properties":{}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	subPath := tPath + "/subscriptions/mysub"
	resp = armReq(t, "PUT", subPath, `{"properties":{}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = armReq(t, "GET", tPath+"/subscriptions", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(listBody), "mysub")

	// Deleting the namespace cascades.
	resp = armReq(t, "DELETE", nsPath, "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	resp = armReq(t, "GET", qPath, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"queue should be cascade-deleted when namespace is deleted")
	resp.Body.Close()
}

// TestAzureAPIM_ARMLifecycle covers service, API, product and subscription
// CRUD.
func TestAzureAPIM_ARMLifecycle(t *testing.T) {
	t.Parallel()
	sub := subscriptionID
	rg := "test-rg"
	name := "lifecycle-apim"
	ensureRGIn(t, sub, rg)
	svcPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ApiManagement/service/%s", sub, rg, name)

	resp := armReq(t, "PUT", svcPath, `{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"ops@example.com","publisherName":"Ops"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"provisioningState":"Succeeded"`)
	_, hostPort := simHostParts(t)
	assert.Contains(t, string(body), "lifecycle-apim.azure-api."+hostPort)

	apiPath := svcPath + "/apis/myapi"
	resp = armReq(t, "PUT", apiPath, `{"properties":{"displayName":"My API","path":"v1","apiType":"http"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	apiBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// The create names the kind apiType; read shapes expose it as type
	// (ApiEntityBaseContract) and never echo apiType.
	assert.Contains(t, string(apiBody), `"type":"http"`)
	assert.NotContains(t, string(apiBody), `"apiType"`)

	prodPath := svcPath + "/products/myproduct"
	resp = armReq(t, "PUT", prodPath, `{"properties":{"displayName":"My Product","state":"published"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// A subscription's contract requires the scope it subscribes to; a create
	// without one is refused.
	apimSubPath := svcPath + "/subscriptions/sub1"
	resp = armReq(t, "PUT", apimSubPath, `{"properties":{"displayName":"Sub 1"}}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"a subscription with no scope is not a subscription")
	resp.Body.Close()

	resp = armReq(t, "PUT", apimSubPath,
		`{"properties":{"displayName":"Sub 1","scope":"`+prodPath+`"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// listSecrets returns SubscriptionKeysContract; terraform-provider-azurerm's
	// subscription Read calls it after create.
	resp = armReq(t, "POST", apimSubPath+"/listSecrets", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	secretsBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(secretsBody), `"primaryKey"`)
	assert.Contains(t, string(secretsBody), `"secondaryKey"`)

	resp = armReq(t, "GET", svcPath+"/apis", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(listBody), "myapi")

	resp = armReq(t, "DELETE", svcPath, "")
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"APIM deletes are synchronous 200 (terraform-provider-azurerm errors on a bare 202)")
	resp.Body.Close()

	resp = armReq(t, "GET", apiPath, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"api should be cascade-deleted when service is deleted")
	resp.Body.Close()

	// A deleted service is recoverable at the location-scoped deletedServices
	// path, then purgeable; terraform-provider-azurerm purges on destroy by
	// default.
	deletedPath := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.ApiManagement/locations/eastus/deletedServices/%s", sub, name)
	resp = armReq(t, "GET", deletedPath, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "deleted service is recoverable until purged")
	delBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(delBody), svcPath, "deleted service contract carries the original serviceId")

	resp = armReq(t, "DELETE", deletedPath, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "purge removes the soft-deleted service")
	resp.Body.Close()

	resp = armReq(t, "GET", deletedPath, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "service is gone after purge")
	resp.Body.Close()
}
