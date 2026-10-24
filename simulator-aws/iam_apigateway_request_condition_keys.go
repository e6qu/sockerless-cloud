package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func init() {
	registerIAMRequestConditionPopulator("apigateway", iamPopulateAPIGatewayRequestConditionKeys)
}

// apigatewayRoutingRuleCondition is a routing rule condition as both the
// request and the stored rule carry it.
type apigatewayRoutingRuleCondition struct {
	MatchBasePaths *struct {
		AnyOf []string `json:"anyOf"`
	} `json:"matchBasePaths"`
}

func apigatewayRoutingRuleBasePaths(conditions []apigatewayRoutingRuleCondition) []string {
	var paths []string
	for _, condition := range conditions {
		if condition.MatchBasePaths != nil {
			paths = append(paths, condition.MatchBasePaths.AnyOf...)
		}
	}
	return paths
}

// iamPopulateAPIGatewayRequestConditionKeys adds the Amazon API Gateway keys of
// the portal and routing-rule actions. An apigateway:Request/ key is the value
// the request passes; an apigateway:Resource/ key is the value the existing
// routing rule holds, which the Service Authorization Reference makes
// available to UpdateRoutingRule and DeleteRoutingRule.
func iamPopulateAPIGatewayRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "CreatePortal", "UpdatePortal":
		var request struct {
			Authorization *struct {
				CognitoConfig *struct {
					UserPoolArn string `json:"userPoolArn"`
				} `json:"cognitoConfig"`
			} `json:"authorization"`
			EndpointConfiguration *struct {
				AcmManaged *struct {
					DomainName string `json:"domainName"`
				} `json:"acmManaged"`
			} `json:"endpointConfiguration"`
			PortalContent *struct {
				DisplayName string `json:"displayName"`
			} `json:"portalContent"`
		}
		if !iamDecodeJSONRequest(body, &request) {
			return
		}
		if a := request.Authorization; a != nil && a.CognitoConfig != nil {
			iamSetConditionValues(ctx, "apigateway:Request/CognitoUserPoolArn", a.CognitoConfig.UserPoolArn)
		}
		if e := request.EndpointConfiguration; e != nil && e.AcmManaged != nil {
			iamSetConditionValues(ctx, "apigateway:Request/PortalDomainName", e.AcmManaged.DomainName)
		}
		if c := request.PortalContent; c != nil {
			iamSetConditionValues(ctx, "apigateway:Request/PortalDisplayName", c.DisplayName)
		}
	case "CreatePortalProduct", "UpdatePortalProduct":
		var request struct {
			DisplayName string `json:"displayName"`
		}
		if iamDecodeJSONRequest(body, &request) {
			iamSetConditionValues(ctx, "apigateway:Request/PortalProductDisplayName", request.DisplayName)
		}
	case "CreateProductPage", "UpdateProductPage":
		var request struct {
			DisplayContent *struct {
				Title string `json:"title"`
			} `json:"displayContent"`
		}
		if iamDecodeJSONRequest(body, &request) && request.DisplayContent != nil {
			iamSetConditionValues(ctx, "apigateway:Request/ProductPageTitle", request.DisplayContent.Title)
		}
	case "CreateProductRestEndpointPage":
		var request struct {
			RestEndpointIdentifier *struct {
				IdentifierParts *struct {
					Method    string `json:"method"`
					RestApiId string `json:"restApiId"`
					Stage     string `json:"stage"`
				} `json:"identifierParts"`
			} `json:"restEndpointIdentifier"`
		}
		if !iamDecodeJSONRequest(body, &request) || request.RestEndpointIdentifier == nil || request.RestEndpointIdentifier.IdentifierParts == nil {
			return
		}
		parts := request.RestEndpointIdentifier.IdentifierParts
		iamSetConditionValues(ctx, "apigateway:Request/Method", parts.Method)
		iamSetConditionValues(ctx, "apigateway:Request/RestApiId", parts.RestApiId)
		iamSetConditionValues(ctx, "apigateway:Request/Stage", parts.Stage)
	case "CreateRoutingRule", "UpdateRoutingRule", "DeleteRoutingRule":
		iamPopulateAPIGatewayRoutingRuleKeys(r, operation, body, ctx)
	}
}

func iamPopulateAPIGatewayRoutingRuleKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	if operation != "DeleteRoutingRule" {
		var request struct {
			Conditions []apigatewayRoutingRuleCondition `json:"conditions"`
			Priority   *int64                           `json:"priority"`
		}
		if iamDecodeJSONRequest(body, &request) {
			iamSetConditionValues(ctx, "apigateway:Request/ConditionBasePaths", apigatewayRoutingRuleBasePaths(request.Conditions)...)
			iamSetConditionInt(ctx, "apigateway:Request/Priority", request.Priority)
		}
	}
	if operation == "CreateRoutingRule" {
		return
	}
	rule, ok := apigwv2RoutingRules.Get(apigwv2ChildKey(iamRESTLabel(r, "DomainName"), iamRESTLabel(r, "RoutingRuleId")))
	if !ok {
		return
	}
	var conditions []apigatewayRoutingRuleCondition
	if len(rule.Conditions) > 0 && json.Unmarshal(rule.Conditions, &conditions) == nil {
		iamSetConditionValues(ctx, "apigateway:Resource/ConditionBasePaths", apigatewayRoutingRuleBasePaths(conditions)...)
	}
	iamSetConditionValues(ctx, "apigateway:Resource/Priority", strconv.Itoa(rule.Priority))
}
