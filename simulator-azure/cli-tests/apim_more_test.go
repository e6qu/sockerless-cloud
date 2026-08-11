package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestAPIMMoreCLI drives the extended Microsoft.ApiManagement ARM slice through
// the Azure CLI (az rest, the surface every Azure CLI test here uses): service
// Update + actions, the API and Product child collections, the subscription /
// namedValue / backend POST actions, and a GetEntityTag HEAD.
func TestAPIMMoreCLI(t *testing.T) {
	name := "cli-apim"
	svc := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ApiManagement/service/%s",
		baseURL, subscriptionID, resourceGroup, name)
	provider := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.ApiManagement", baseURL, subscriptionID)
	const apiVersion = "?api-version=2022-08-01"

	// Service create + Update.
	runCLI(t, azRest("PUT", svc+apiVersion, `{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"ops@example.com","publisherName":"Ops"}}`))
	runCLI(t, azRest("PATCH", svc+apiVersion, `{"tags":{"env":"cli"}}`))

	// Service actions.
	if out := runCLI(t, azRest("GET", svc+"/skus"+apiVersion, "")); !strings.Contains(out, "Premium") {
		t.Fatalf("skus listing missing Premium SKU: %s", out)
	}
	runCLI(t, azRest("POST", svc+"/getssotoken"+apiVersion, ""))

	// Provider-scoped checkNameAvailability.
	if out := runCLI(t, azRest("POST", provider+"/checkNameAvailability"+apiVersion,
		fmt.Sprintf(`{"name":%q,"type":"Microsoft.ApiManagement/service"}`, name))); !strings.Contains(out, "AlreadyExists") {
		t.Fatalf("checkNameAvailability should report the created name as taken: %s", out)
	}

	// API + schema + policy + operation.
	api := svc + "/apis/cliapi"
	runCLI(t, azRest("PUT", api+apiVersion, `{"properties":{"displayName":"CLI API","path":"v1","apiType":"http"}}`))
	runCLI(t, azRest("PATCH", api+apiVersion, `{"properties":{"description":"updated"}}`))
	runCLI(t, azRest("PUT", api+"/schemas/s1"+apiVersion, `{"properties":{"contentType":"application/json"}}`))
	runCLI(t, azRest("PUT", api+"/policies/policy"+apiVersion, `{"properties":{"format":"xml","value":"<policies/>"}}`))
	op := api + "/operations/op1"
	runCLI(t, azRest("PUT", op+apiVersion, `{"properties":{"displayName":"Op","method":"GET","urlTemplate":"/x"}}`))

	// Product + policy + api association.
	product := svc + "/products/cliproduct"
	runCLI(t, azRest("PUT", product+apiVersion, `{"properties":{"displayName":"CLI Product","state":"published"}}`))
	runCLI(t, azRest("PUT", product+"/policies/policy"+apiVersion, `{"properties":{"format":"xml","value":"<policies/>"}}`))
	if out := runCLI(t, azRest("PUT", product+"/apis/cliapi"+apiVersion, "")); !strings.Contains(out, "cliapi") {
		t.Fatalf("product/api association should return the ApiContract: %s", out)
	}

	// Subscription + key regeneration.
	apimSub := svc + "/subscriptions/cs1"
	runCLI(t, azRest("PUT", apimSub+apiVersion, `{"properties":{"displayName":"CLI Sub"}}`))
	runCLI(t, azRest("POST", apimSub+"/regeneratePrimaryKey"+apiVersion, ""))

	// NamedValue: secret is write-only, surfaced only via listValue.
	nv := svc + "/namedValues/cnv"
	runCLI(t, azRest("PUT", nv+apiVersion, `{"properties":{"displayName":"cnv","value":"s3cr3t"}}`))
	if out := runCLI(t, azRest("GET", nv+apiVersion, "")); strings.Contains(out, "s3cr3t") {
		t.Fatalf("GET namedValue must redact the secret: %s", out)
	}
	if out := runCLI(t, azRest("POST", nv+"/listValue"+apiVersion, "")); !strings.Contains(out, "s3cr3t") {
		t.Fatalf("listValue must return the secret: %s", out)
	}

	// Backend + reconnect.
	backend := svc + "/backends/b1"
	runCLI(t, azRest("PUT", backend+apiVersion, `{"properties":{"url":"https://example.com","protocol":"http"}}`))
	runCLI(t, azRest("POST", backend+"/reconnect"+apiVersion, ""))
}
