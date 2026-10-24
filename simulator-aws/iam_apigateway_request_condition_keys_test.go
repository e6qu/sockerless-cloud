package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// apigatewayConditionRequest routes a request the way the REST gate does and
// returns it with the operation and labels its route names.
func apigatewayConditionRequest(t *testing.T, method, path, body string) (*http.Request, string, map[string][]string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	operation, labels, ok := awsRESTOperationFor(awsRESTGates["apigateway.amazonaws.com"].models, r)
	if !ok {
		t.Fatalf("%s %s matches no Amazon API Gateway route", method, path)
	}
	return iamWithRESTOperation(r, operation, labels), operation, labels
}

func TestAPIGatewayOperationsAreAuthorizedAsTheReferenceMapsThem(t *testing.T) {
	buildConformanceSimulator(t)
	const (
		region  = "us-east-1"
		product = "arn:aws:apigateway:us-east-1:123456789012:/portalproducts/p1"
	)
	apigwv2Portals.Put("portal1", APIGWv2Portal{PortalId: "portal1", IncludedPortalProductArns: []string{product}})
	account := awsAccountID()
	for name, tc := range map[string]struct {
		method, path, body string
		want               []string
	}{
		"an HTTP API create is the POST method on its path": {
			http.MethodPost, "/v2/apis", `{"name":"a","protocolType":"HTTP"}`,
			[]string{"apigateway:POST arn:aws:apigateway:" + region + "::/v2/apis"},
		},
		"a portal create is its own action, reads the products it includes, and is a POST": {
			http.MethodPost, "/v2/portals", `{"includedPortalProductArns":["` + product + `"]}`,
			[]string{
				"apigateway:CreatePortal arn:aws:apigateway:" + region + ":" + account + ":/portals/*",
				"apigateway:GetPortalProduct " + product,
				"apigateway:POST arn:aws:apigateway:" + region + "::/v2/portals",
			},
		},
		"publishing a portal reads the products the portal includes": {
			http.MethodPost, "/v2/portals/portal1/publish", `{}`,
			[]string{
				"apigateway:GetPortalProduct " + product,
				"apigateway:PublishPortal arn:aws:apigateway:" + region + ":" + account + ":/portals/portal1",
			},
		},
		"replacing a routing rule is UpdateRoutingRule on the rule": {
			http.MethodPut, "/v2/domainnames/api.example.com/routingrules/r1", `{"priority":10}`,
			[]string{"apigateway:UpdateRoutingRule arn:aws:apigateway:" + region + ":" + account + ":/domainnames/api.example.com/routingrules/r1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, operation, labels := apigatewayConditionRequest(t, tc.method, tc.path, tc.body)
			got := targetStrings(iamRESTAuthorizationTargets(r, "apigateway", operation, labels))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAPIGatewayConditionKeysReadThePortalRequest(t *testing.T) {
	cases := []struct {
		operation, method, path, body string
		want                          map[string][]string
	}{
		{"CreatePortal", http.MethodPost, "/v2/portals", `{
			"authorization": {"cognitoConfig": {"appClientId": "c", "userPoolArn": "arn:aws:cognito-idp:us-east-1:123456789012:userpool/us-east-1_a", "userPoolDomain": "https://auth.example.com"}},
			"endpointConfiguration": {"acmManaged": {"certificateArn": "arn:aws:acm:us-east-1:123456789012:certificate/c", "domainName": "developers.example.com"}},
			"portalContent": {"displayName": "Developers", "theme": {"customColors": {}}}}`,
			map[string][]string{
				"apigateway:Request/CognitoUserPoolArn": {"arn:aws:cognito-idp:us-east-1:123456789012:userpool/us-east-1_a"},
				"apigateway:Request/PortalDomainName":   {"developers.example.com"},
				"apigateway:Request/PortalDisplayName":  {"Developers"},
			}},
		{"UpdatePortalProduct", http.MethodPatch, "/v2/portalproducts/p1", `{"displayName": "Payments"}`,
			map[string][]string{"apigateway:Request/PortalProductDisplayName": {"Payments"}}},
		{"CreateProductPage", http.MethodPost, "/v2/portalproducts/p1/productpages", `{"displayContent": {"title": "Overview", "body": "b"}}`,
			map[string][]string{"apigateway:Request/ProductPageTitle": {"Overview"}}},
		{"CreateProductRestEndpointPage", http.MethodPost, "/v2/portalproducts/p1/productrestendpointpages",
			`{"restEndpointIdentifier": {"identifierParts": {"method": "GET", "path": "/pets", "restApiId": "abc123", "stage": "prod"}}}`,
			map[string][]string{
				"apigateway:Request/Method":    {"GET"},
				"apigateway:Request/RestApiId": {"abc123"},
				"apigateway:Request/Stage":     {"prod"},
			}},
	}
	for _, tc := range cases {
		r, _, _ := apigatewayConditionRequest(t, tc.method, tc.path, tc.body)
		assertPopulatedConditionValues(t, populatedConditionContext(r, "apigateway", tc.operation, tc.body), tc.want)
	}
	r, _, _ := apigatewayConditionRequest(t, http.MethodPatch, "/v2/portalproducts/p1", `{"description": "d"}`)
	assertConditionKeysAbsent(t, populatedConditionContext(r, "apigateway", "UpdatePortalProduct", `{"description": "d"}`),
		"apigateway:Request/PortalProductDisplayName")
}

// A routing rule write carries the request's base paths and priority, and an
// update or delete also the rule's existing ones.
func TestAPIGatewayConditionKeysReadTheRoutingRule(t *testing.T) {
	buildConformanceSimulator(t)
	conditions, err := json.Marshal([]map[string]any{
		{"matchBasePaths": map[string]any{"anyOf": []string{"orders"}}},
		{"matchHeaders": map[string]any{"anyOf": []map[string]string{{"header": "x-version", "valueGlob": "2"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	apigwv2RoutingRules.Put(apigwv2ChildKey("api.example.com", "r1"), APIGWv2RoutingRule{
		RoutingRuleId: "r1", DomainName: "api.example.com", Priority: 50, Conditions: conditions,
	})
	const body = `{"priority": 10, "conditions": [{"matchBasePaths": {"anyOf": ["v2", "beta"]}}], "actions": []}`

	r, _, _ := apigatewayConditionRequest(t, http.MethodPost, "/v2/domainnames/api.example.com/routingrules", body)
	ctx := populatedConditionContext(r, "apigateway", "CreateRoutingRule", body)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"apigateway:Request/ConditionBasePaths": {"v2", "beta"},
		"apigateway:Request/Priority":           {"10"},
	})
	assertConditionKeysAbsent(t, ctx, "apigateway:Resource/ConditionBasePaths", "apigateway:Resource/Priority")

	r, _, _ = apigatewayConditionRequest(t, http.MethodPut, "/v2/domainnames/api.example.com/routingrules/r1", body)
	assertPopulatedConditionValues(t, populatedConditionContext(r, "apigateway", "UpdateRoutingRule", body), map[string][]string{
		"apigateway:Request/ConditionBasePaths":  {"v2", "beta"},
		"apigateway:Request/Priority":            {"10"},
		"apigateway:Resource/ConditionBasePaths": {"orders"},
		"apigateway:Resource/Priority":           {"50"},
	})

	r, _, _ = apigatewayConditionRequest(t, http.MethodDelete, "/v2/domainnames/api.example.com/routingrules/r1", "")
	ctx = populatedConditionContext(r, "apigateway", "DeleteRoutingRule", "")
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"apigateway:Resource/ConditionBasePaths": {"orders"},
		"apigateway:Resource/Priority":           {"50"},
	})
	assertConditionKeysAbsent(t, ctx, "apigateway:Request/ConditionBasePaths", "apigateway:Request/Priority")

	r, _, _ = apigatewayConditionRequest(t, http.MethodDelete, "/v2/domainnames/api.example.com/routingrules/missing", "")
	assertConditionKeysAbsent(t, populatedConditionContext(r, "apigateway", "DeleteRoutingRule", ""),
		"apigateway:Resource/ConditionBasePaths", "apigateway:Resource/Priority")
}
