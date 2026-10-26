package azure_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureAPIM_MoreOperations exercises the extended Microsoft.ApiManagement
// slice: service Update and actions, the API and Product child collections,
// the GetEntityTag HEAD operations, and the subscription, namedValue and
// backend POST actions.
func TestAzureAPIM_MoreOperations(t *testing.T) {
	t.Parallel()
	sub := "00000000-0000-0000-0000-000000000010"
	rg := "apim-more-rg"
	name := "more-apim"
	ensureRGIn(t, sub, rg)
	svc := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ApiManagement/service/%s", sub, rg, name)
	provider := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.ApiManagement", sub)

	mustStatus := func(method, path, body string, want int) []byte {
		t.Helper()
		resp := armReq(t, method, path, body)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, want, resp.StatusCode, "%s %s -> %s", method, path, string(b))
		return b
	}

	mustStatus("PUT", svc, `{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"ops@example.com","publisherName":"Ops"}}`, http.StatusOK)

	upd := mustStatus("PATCH", svc, `{"tags":{"env":"test"},"properties":{"publisherName":"Ops2"}}`, http.StatusOK)
	assert.Contains(t, string(upd), `"Ops2"`)
	assert.Contains(t, string(upd), `"env":"test"`)

	skus := mustStatus("GET", svc+"/skus", "", http.StatusOK)
	assert.Contains(t, string(skus), `"resourceType":"Microsoft.ApiManagement/service"`)
	assert.Contains(t, string(skus), `"Premium"`)

	sso := mustStatus("POST", svc+"/getssotoken", "", http.StatusOK)
	assert.Contains(t, string(sso), `"redirectUri"`)

	// Long-running service actions answer 202 with Azure-AsyncOperation.
	for _, action := range []string{"backup", "restore", "migrateToStv2", "applynetworkconfigurationupdates"} {
		resp := armReq(t, "POST", svc+"/"+action, "{}")
		resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode, action)
		assert.NotEmpty(t, resp.Header.Get("Azure-AsyncOperation"), action+" must advertise an async operation")
	}

	ops := mustStatus("GET", "/providers/Microsoft.ApiManagement/operations", "", http.StatusOK)
	assert.Contains(t, string(ops), `"Microsoft.ApiManagement/service/read"`)

	bySub := mustStatus("GET", provider+"/service", "", http.StatusOK)
	assert.Contains(t, string(bySub), name)

	taken := mustStatus("POST", provider+"/checknameavailability", fmt.Sprintf(`{"name":%q,"type":"Microsoft.ApiManagement/service"}`, name), http.StatusOK)
	assert.Contains(t, string(taken), `"nameAvailable":false`)
	assert.Contains(t, string(taken), `"AlreadyExists"`)
	free := mustStatus("POST", provider+"/checknameavailability", `{"name":"brand-new-apim-xyz","type":"Microsoft.ApiManagement/service"}`, http.StatusOK)
	assert.Contains(t, string(free), `"nameAvailable":true`)

	dom := mustStatus("POST", provider+"/getDomainOwnershipIdentifier", "", http.StatusOK)
	assert.Contains(t, string(dom), `"domainOwnershipIdentifier"`)

	api := svc + "/apis/myapi"
	mustStatus("PUT", api, `{"properties":{"displayName":"My API","path":"v1","apiType":"http"}}`, http.StatusOK)
	patched := mustStatus("PATCH", api, `{"properties":{"description":"updated"}}`, http.StatusOK)
	assert.Contains(t, string(patched), `"updated"`)

	resp := armReq(t, "HEAD", api, "")
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("ETag"))
	resp = armReq(t, "HEAD", svc+"/apis/missing", "")
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	revs := mustStatus("GET", api+"/revisions", "", http.StatusOK)
	assert.Contains(t, string(revs), `"apiRevision"`)

	// A schema's contract requires its document as well as its content type.
	mustStatus("PUT", api+"/schemas/schema1",
		`{"properties":{"contentType":"application/vnd.oai.openapi.components+json",`+
			`"document":{"components":{"schemas":{"Thing":{"type":"object"}}}}}}`, http.StatusOK)
	mustStatus("PUT", api+"/schemas/nodocument",
		`{"properties":{"contentType":"application/vnd.oai.openapi.components+json"}}`, http.StatusBadRequest)
	gotSchema := mustStatus("GET", api+"/schemas/schema1", "", http.StatusOK)
	assert.Contains(t, string(gotSchema), `"contentType"`)
	assert.Contains(t, string(gotSchema), `"document"`)
	assert.Contains(t, string(mustStatus("GET", api+"/schemas", "", http.StatusOK)), "schema1")

	mustStatus("PUT", api+"/policies/policy", `{"properties":{"format":"xml","value":"<policies/>"}}`, http.StatusOK)
	mustStatus("GET", api+"/policies/policy", "", http.StatusOK)
	mustStatus("GET", api+"/policies", "", http.StatusOK)

	mustStatus("PUT", api+"/diagnostics/applicationinsights", `{"properties":{"loggerId":"/loggers/ai"}}`, http.StatusOK)
	mustStatus("PATCH", api+"/diagnostics/applicationinsights", `{"properties":{"alwaysLog":"allErrors"}}`, http.StatusOK)
	mustStatus("GET", api+"/diagnostics", "", http.StatusOK)

	mustStatus("PUT", api+"/releases/rel1", `{"properties":{"apiId":"`+api+`","notes":"v1"}}`, http.StatusOK)
	mustStatus("PATCH", api+"/releases/rel1", `{"properties":{"notes":"v2"}}`, http.StatusOK)
	mustStatus("GET", api+"/releases", "", http.StatusOK)
	resp = armReq(t, "HEAD", api+"/releases/rel1", "")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	mustStatus("PUT", api+"/tags/tag1", `{"properties":{"displayName":"tag1"}}`, http.StatusOK)
	mustStatus("GET", api+"/tags", "", http.StatusOK)

	op := api + "/operations/getthing"
	mustStatus("PUT", op, `{"properties":{"displayName":"Get","method":"GET","urlTemplate":"/thing"}}`, http.StatusOK)
	mustStatus("PATCH", op, `{"properties":{"description":"gets a thing"}}`, http.StatusOK)
	resp = armReq(t, "HEAD", op, "")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	mustStatus("PUT", op+"/policies/policy", `{"properties":{"format":"xml","value":"<policies/>"}}`, http.StatusOK)
	mustStatus("GET", op+"/policies", "", http.StatusOK)
	mustStatus("PUT", op+"/tags/optag", `{"properties":{"displayName":"optag"}}`, http.StatusOK)
	mustStatus("GET", op+"/tags", "", http.StatusOK)

	product := svc + "/products/myproduct"
	mustStatus("PUT", product, `{"properties":{"displayName":"My Product","state":"published"}}`, http.StatusOK)
	mustStatus("PATCH", product, `{"properties":{"description":"prod desc"}}`, http.StatusOK)
	resp = armReq(t, "HEAD", product, "")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	mustStatus("PUT", product+"/policies/policy", `{"properties":{"format":"xml","value":"<policies/>"}}`, http.StatusOK)
	mustStatus("GET", product+"/policies", "", http.StatusOK)
	mustStatus("PUT", product+"/tags/ptag", `{"properties":{"displayName":"ptag"}}`, http.StatusOK)
	mustStatus("GET", product+"/tags", "", http.StatusOK)

	// The product-API association resolves to the ApiContract.
	addedApi := mustStatus("PUT", product+"/apis/myapi", "", http.StatusOK)
	assert.Contains(t, string(addedApi), `"Microsoft.ApiManagement/service/apis"`)
	listApis := mustStatus("GET", product+"/apis", "", http.StatusOK)
	assert.Contains(t, string(listApis), "myapi")
	apiProducts := mustStatus("GET", api+"/products", "", http.StatusOK)
	assert.Contains(t, string(apiProducts), "myproduct")
	mustStatus("DELETE", product+"/apis/myapi", "", http.StatusOK)

	addedGroup := mustStatus("PUT", product+"/groups/developers", "", http.StatusOK)
	assert.Contains(t, string(addedGroup), `"Microsoft.ApiManagement/service/groups"`)
	mustStatus("GET", product+"/groups", "", http.StatusOK)
	mustStatus("DELETE", product+"/groups/developers", "", http.StatusOK)

	mustStatus("GET", product+"/subscriptions", "", http.StatusOK)

	apimSub := svc + "/subscriptions/sub1"
	mustStatus("PUT", apimSub, `{"properties":{"displayName":"Sub 1","scope":"`+product+`"}}`, http.StatusOK)
	mustStatus("PATCH", apimSub, `{"properties":{"displayName":"Sub 1b"}}`, http.StatusOK)
	mustStatus("POST", apimSub+"/regeneratePrimaryKey", "", http.StatusNoContent)
	mustStatus("POST", apimSub+"/regenerateSecondaryKey", "", http.StatusNoContent)
	resp = armReq(t, "HEAD", apimSub, "")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	prodSubs := mustStatus("GET", product+"/subscriptions", "", http.StatusOK)
	assert.Contains(t, string(prodSubs), "sub1")

	// The NamedValue secret is write-only: reads redact it.
	nv := svc + "/namedValues/nv1"
	created := mustStatus("PUT", nv, `{"properties":{"displayName":"nv1","value":"s3cr3t"}}`, http.StatusOK)
	assert.NotContains(t, string(created), "s3cr3t", "create response must redact the secret value")
	got := mustStatus("GET", nv, "", http.StatusOK)
	assert.NotContains(t, string(got), "s3cr3t", "GET must redact the secret value")
	assert.Contains(t, string(got), `"displayName":"nv1"`)
	listed := mustStatus("GET", svc+"/namedValues", "", http.StatusOK)
	assert.NotContains(t, string(listed), "s3cr3t", "LIST must redact the secret value")
	// Only listValue returns the secret.
	val := mustStatus("POST", nv+"/listValue", "", http.StatusOK)
	assert.Contains(t, string(val), "s3cr3t")
	mustStatus("PATCH", nv, `{"properties":{"displayName":"nv1b"}}`, http.StatusOK)
	mustStatus("POST", nv+"/refreshSecret", "", http.StatusOK)
	resp = armReq(t, "HEAD", nv, "")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	backend := svc + "/backends/backend1"
	mustStatus("PUT", backend, `{"properties":{"url":"https://example.com","protocol":"http"}}`, http.StatusOK)
	mustStatus("PATCH", backend, `{"properties":{"title":"My Backend"}}`, http.StatusOK)
	mustStatus("POST", backend+"/reconnect", "", http.StatusAccepted)
	resp = armReq(t, "HEAD", backend, "")
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	mustStatus("DELETE", svc, "", http.StatusOK)
	delList := mustStatus("GET", provider+"/deletedServices", "", http.StatusOK)
	assert.Contains(t, string(delList), name)

	// Deleting the service removes its children.
	resp = armReq(t, "GET", api+"/schemas/schema1", "")
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "child schema must be cascade-deleted with the service")
}
