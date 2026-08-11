package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCLI_CompletionSurfaces exercises a representative slice of the
// Microsoft.ApiManagement, Microsoft.Resources, and Microsoft.DBforPostgreSQL
// completion ratchet through the `az rest` CLI surface. Kept lean: one resource
// group, one APIM service, and one PostgreSQL server back a handful of calls.
func TestCLI_CompletionSurfaces(t *testing.T) {
	const apimAPIVersion = "2022-08-01"
	rgURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=%s", baseURL, subscriptionID, resourceGroup, resourcesAPIVersion)
	runCLI(t, azRest("PUT", rgURL, `{"location":"eastus"}`))

	// ---- Microsoft.ApiManagement: API resolver + issue ----
	apimURL := func(p string) string {
		return armURL("Microsoft.ApiManagement", p, apimAPIVersion)
	}
	svc := "cli-completion-apim"
	runCLI(t, azRest("PUT", apimURL("service/"+svc),
		`{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"ops@example.com","publisherName":"Ops"}}`))
	runCLI(t, azRest("PUT", apimURL("service/"+svc+"/apis/api1"), `{"properties":{"displayName":"API1","path":"v1","apiType":"graphql"}}`))
	runCLI(t, azRest("PUT", apimURL("service/"+svc+"/apis/api1/resolvers/res1"), `{"properties":{"displayName":"Resolver","path":"Query/items"}}`))
	out := runCLI(t, azRest("GET", apimURL("service/"+svc+"/apis/api1/resolvers"), ""))
	assert.Contains(t, out, "Resolver")
	runCLI(t, azRest("PUT", apimURL("service/"+svc+"/apis/api1/issues/issue1"),
		`{"properties":{"title":"Bug","description":"It broke","userId":"u1"}}`))

	// ---- Microsoft.Resources: tags at resource-group scope + MG register ----
	tagsURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Resources/tags/default?api-version=%s",
		baseURL, subscriptionID, resourceGroup, resourcesAPIVersion)
	runCLI(t, azRest("PUT", tagsURL, `{"properties":{"tags":{"team":"data"}}}`))
	out = runCLI(t, azRest("GET", tagsURL, ""))
	assert.Contains(t, out, "data")
	mgURL := fmt.Sprintf("%s/providers/Microsoft.Management/managementGroups/mygroup/providers/Microsoft.Storage/register?api-version=%s",
		baseURL, resourcesAPIVersion)
	runCLI(t, azRest("POST", mgURL, ""))

	// ---- Microsoft.DBforPostgreSQL: operations, DNS suffix, capabilities, migration ----
	out = runCLI(t, azRest("GET", tenantScopeURL("providers/Microsoft.DBforPostgreSQL/operations", pgAPIVersion), ""))
	assert.Contains(t, out, "Microsoft.DBforPostgreSQL/flexibleServers/read")
	out = runCLI(t, azRest("POST", tenantScopeURL("providers/Microsoft.DBforPostgreSQL/getPrivateDnsZoneSuffix", pgAPIVersion), ""))
	assert.Contains(t, out, "postgres.database.azure.com")
	out = runCLI(t, azRest("GET", subURL("providers/Microsoft.DBforPostgreSQL/locations/eastus/capabilities", pgAPIVersion), ""))
	assert.Contains(t, out, "eastus")

	server := "cli-completion-pg"
	runCLI(t, azRest("PUT", pgURL("flexibleServers/"+server),
		`{"location":"eastus","properties":{"administratorLogin":"psqladmin","administratorLoginPassword":"Sup3rSecret!","version":"15","storage":{"storageSizeGB":32}}}`))
	runCLI(t, azRest("PUT", pgURL("flexibleServers/"+server+"/migrations/mig1"),
		`{"location":"eastus","properties":{"migrationMode":"Offline"}}`))
	out = runCLI(t, azRest("GET", pgURL("flexibleServers/"+server+"/migrations/mig1"), ""))
	assert.Contains(t, out, "mig1")
}
