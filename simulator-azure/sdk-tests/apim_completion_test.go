package azure_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureAPIM_CompletionSurfaces exercises the completion of the
// Microsoft.ApiManagement APIs and Products surfaces: GraphQL API resolvers and
// resolver policies, API issues with their comments and attachments, API tag
// descriptions, the API and Product wikis, and the operations-by-tag listing.
func TestAzureAPIM_CompletionSurfaces(t *testing.T) {
	sub := "00000000-0000-0000-0000-000000000011"
	rg := "apim-completion-rg"
	name := "completion-apim"
	svc := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ApiManagement/service/%s", sub, rg, name)

	mustStatus := func(method, path, body string, want int) []byte {
		t.Helper()
		resp := armReq(t, method, path, body)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, want, resp.StatusCode, "%s %s -> %s", method, path, string(b))
		return b
	}
	head := func(path string, want int) {
		t.Helper()
		resp := armReq(t, "HEAD", path, "")
		resp.Body.Close()
		require.Equal(t, want, resp.StatusCode, "HEAD %s", path)
	}

	mustStatus("PUT", svc, `{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"ops@example.com","publisherName":"Ops"}}`, http.StatusOK)
	api := svc + "/apis/myapi"
	mustStatus("PUT", api, `{"properties":{"displayName":"My API","path":"v1","apiType":"graphql"}}`, http.StatusOK)

	// ---- GraphQL resolvers + resolver policies ----
	resolver := api + "/resolvers/res1"
	mustStatus("PUT", resolver, `{"properties":{"displayName":"My Resolver","path":"Query/items"}}`, http.StatusOK)
	mustStatus("PATCH", resolver, `{"properties":{"description":"resolves items"}}`, http.StatusOK)
	assert.Contains(t, string(mustStatus("GET", resolver, "", http.StatusOK)), "My Resolver")
	mustStatus("GET", api+"/resolvers", "", http.StatusOK)
	head(resolver, http.StatusOK)
	resolverPolicy := resolver + "/policies/policy"
	mustStatus("PUT", resolverPolicy, `{"properties":{"format":"xml","value":"<http-data-source/>"}}`, http.StatusOK)
	mustStatus("GET", resolverPolicy, "", http.StatusOK)
	mustStatus("GET", resolver+"/policies", "", http.StatusOK)
	head(resolverPolicy, http.StatusOK)

	// ---- API issues + comments + attachments ----
	issue := api + "/issues/issue1"
	mustStatus("PUT", issue, `{"properties":{"title":"Bug","description":"It broke","userId":"`+svc+`/users/u1"}}`, http.StatusOK)
	mustStatus("PATCH", issue, `{"properties":{"title":"Bug (updated)"}}`, http.StatusOK)
	assert.Contains(t, string(mustStatus("GET", issue, "", http.StatusOK)), "Bug")
	mustStatus("GET", api+"/issues", "", http.StatusOK)
	head(issue, http.StatusOK)

	comment := issue + "/comments/c1"
	mustStatus("PUT", comment, `{"properties":{"text":"me too","userId":"`+svc+`/users/u1"}}`, http.StatusOK)
	mustStatus("GET", comment, "", http.StatusOK)
	mustStatus("GET", issue+"/comments", "", http.StatusOK)
	head(comment, http.StatusOK)

	attachment := issue + "/attachments/a1"
	mustStatus("PUT", attachment, `{"properties":{"title":"log","contentFormat":"link","content":"https://example.com/log.txt"}}`, http.StatusOK)
	mustStatus("GET", attachment, "", http.StatusOK)
	mustStatus("GET", issue+"/attachments", "", http.StatusOK)
	head(attachment, http.StatusOK)

	// ---- tag descriptions + API wiki ----
	tagDesc := api + "/tagDescriptions/tagdesc1"
	mustStatus("PUT", tagDesc, `{"properties":{"displayName":"v1 tag","tagId":"`+svc+`/tags/t1"}}`, http.StatusOK)
	mustStatus("GET", tagDesc, "", http.StatusOK)
	mustStatus("GET", api+"/tagDescriptions", "", http.StatusOK)
	head(tagDesc, http.StatusOK)

	apiWiki := api + "/wikis/default"
	mustStatus("PUT", apiWiki, `{"properties":{"documents":[{"documentationId":"doc1"}]}}`, http.StatusOK)
	mustStatus("PATCH", apiWiki, `{"properties":{"documents":[{"documentationId":"doc2"}]}}`, http.StatusOK)
	mustStatus("GET", apiWiki, "", http.StatusOK)
	mustStatus("GET", api+"/wikis", "", http.StatusOK)
	head(apiWiki, http.StatusOK)

	// ---- operations grouped by tag ----
	op := api + "/operations/getthing"
	mustStatus("PUT", op, `{"properties":{"displayName":"Get","method":"GET","urlTemplate":"/thing"}}`, http.StatusOK)
	mustStatus("PUT", op+"/tags/optag", `{"properties":{"displayName":"optag"}}`, http.StatusOK)
	byTags := mustStatus("GET", api+"/operationsByTags", "", http.StatusOK)
	assert.Contains(t, string(byTags), "optag")

	// ---- product wiki ----
	product := svc + "/products/myproduct"
	mustStatus("PUT", product, `{"properties":{"displayName":"My Product","state":"published"}}`, http.StatusOK)
	productWiki := product + "/wikis/default"
	mustStatus("PUT", productWiki, `{"properties":{"documents":[{"documentationId":"pdoc1"}]}}`, http.StatusOK)
	mustStatus("PATCH", productWiki, `{"properties":{"documents":[{"documentationId":"pdoc2"}]}}`, http.StatusOK)
	mustStatus("GET", productWiki, "", http.StatusOK)
	mustStatus("GET", product+"/wikis", "", http.StatusOK)
	head(productWiki, http.StatusOK)

	// ---- cascade delete + child removal ----
	mustStatus("DELETE", resolverPolicy, "", http.StatusOK)
	mustStatus("DELETE", resolver, "", http.StatusOK)
	mustStatus("DELETE", attachment, "", http.StatusOK)
	mustStatus("DELETE", comment, "", http.StatusOK)
	mustStatus("DELETE", issue, "", http.StatusOK)
	mustStatus("DELETE", tagDesc, "", http.StatusOK)
	mustStatus("DELETE", apiWiki, "", http.StatusOK)
	mustStatus("DELETE", productWiki, "", http.StatusOK)
}
