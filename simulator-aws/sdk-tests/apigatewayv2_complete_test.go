package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayV2_PortalsSDK exercises the developer-portal surface:
// CreatePortal, GetPortal, ListPortals, UpdatePortal, PublishPortal,
// PreviewPortal, DisablePortal, DeletePortal.
func TestAPIGatewayV2_PortalsSDK(t *testing.T) {
	c := apigwv2Client()
	create, err := c.CreatePortal(ctx, &apigatewayv2.CreatePortalInput{
		Authorization:         &apigwv2types.Authorization{None: &apigwv2types.None{}},
		EndpointConfiguration: &apigwv2types.EndpointConfigurationRequest{None: &apigwv2types.None{}},
		PortalContent: &apigwv2types.PortalContent{
			DisplayName: aws.String("docs portal"),
			Description: aws.String("public developer docs"),
			Theme: &apigwv2types.PortalTheme{
				CustomColors: &apigwv2types.CustomColors{
					AccentColor:          aws.String("#0073bb"),
					BackgroundColor:      aws.String("#ffffff"),
					ErrorValidationColor: aws.String("#d13212"),
					HeaderColor:          aws.String("#232f3e"),
					NavigationColor:      aws.String("#16191f"),
					TextColor:            aws.String("#16191f"),
				},
			},
		},
		RumAppMonitorName: aws.String("portal-rum"),
		Tags:              map[string]string{"team": "platform"},
	})
	require.NoError(t, err)
	portalID := aws.ToString(create.PortalId)
	require.NotEmpty(t, portalID)
	require.NotEmpty(t, aws.ToString(create.PortalArn))
	t.Cleanup(func() { _, _ = c.DeletePortal(ctx, &apigatewayv2.DeletePortalInput{PortalId: aws.String(portalID)}) })

	got, err := c.GetPortal(ctx, &apigatewayv2.GetPortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)
	assert.Equal(t, portalID, aws.ToString(got.PortalId))
	assert.Equal(t, "platform", got.Tags["team"])

	list, err := c.ListPortals(ctx, &apigatewayv2.ListPortalsInput{})
	require.NoError(t, err)
	var found bool
	for _, p := range list.Items {
		if aws.ToString(p.PortalId) == portalID {
			found = true
		}
	}
	assert.True(t, found, "created portal must appear in ListPortals")

	upd, err := c.UpdatePortal(ctx, &apigatewayv2.UpdatePortalInput{
		PortalId:          aws.String(portalID),
		RumAppMonitorName: aws.String("portal-rum-v2"),
	})
	require.NoError(t, err)
	assert.Equal(t, "portal-rum-v2", aws.ToString(upd.RumAppMonitorName))

	_, err = c.PublishPortal(ctx, &apigatewayv2.PublishPortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)
	pub, err := c.GetPortal(ctx, &apigatewayv2.GetPortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)
	assert.Equal(t, apigwv2types.PublishStatusPublished, pub.PublishStatus)

	_, err = c.PreviewPortal(ctx, &apigatewayv2.PreviewPortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)

	_, err = c.DisablePortal(ctx, &apigatewayv2.DisablePortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)
	dis, err := c.GetPortal(ctx, &apigatewayv2.GetPortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)
	assert.Equal(t, apigwv2types.PublishStatusDisabled, dis.PublishStatus)

	_, err = c.DeletePortal(ctx, &apigatewayv2.DeletePortalInput{PortalId: aws.String(portalID)})
	require.NoError(t, err)
	_, err = c.GetPortal(ctx, &apigatewayv2.GetPortalInput{PortalId: aws.String(portalID)})
	require.Error(t, err, "deleted portal must 404")
}

// TestAPIGatewayV2_PortalProductsSDK exercises the portal-product surface plus
// the product sharing policy: CreatePortalProduct, GetPortalProduct,
// ListPortalProducts, UpdatePortalProduct, PutPortalProductSharingPolicy,
// GetPortalProductSharingPolicy, DeletePortalProductSharingPolicy,
// DeletePortalProduct.
func TestAPIGatewayV2_PortalProductsSDK(t *testing.T) {
	c := apigwv2Client()
	create, err := c.CreatePortalProduct(ctx, &apigatewayv2.CreatePortalProductInput{
		DisplayName: aws.String("orders-api"),
		Description: aws.String("the orders product"),
		Tags:        map[string]string{"tier": "gold"},
	})
	require.NoError(t, err)
	productID := aws.ToString(create.PortalProductId)
	require.NotEmpty(t, productID)
	require.NotEmpty(t, aws.ToString(create.PortalProductArn))
	t.Cleanup(func() {
		_, _ = c.DeletePortalProduct(ctx, &apigatewayv2.DeletePortalProductInput{PortalProductId: aws.String(productID)})
	})

	got, err := c.GetPortalProduct(ctx, &apigatewayv2.GetPortalProductInput{PortalProductId: aws.String(productID)})
	require.NoError(t, err)
	assert.Equal(t, "orders-api", aws.ToString(got.DisplayName))

	list, err := c.ListPortalProducts(ctx, &apigatewayv2.ListPortalProductsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, list.Items)

	upd, err := c.UpdatePortalProduct(ctx, &apigatewayv2.UpdatePortalProductInput{
		PortalProductId: aws.String(productID),
		Description:     aws.String("updated orders product"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated orders product", aws.ToString(upd.Description))

	policy := `{"Version":"2012-10-17","Statement":[]}`
	_, err = c.PutPortalProductSharingPolicy(ctx, &apigatewayv2.PutPortalProductSharingPolicyInput{
		PortalProductId: aws.String(productID),
		PolicyDocument:  aws.String(policy),
	})
	require.NoError(t, err)

	sp, err := c.GetPortalProductSharingPolicy(ctx, &apigatewayv2.GetPortalProductSharingPolicyInput{
		PortalProductId: aws.String(productID),
	})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(sp.PolicyDocument))

	_, err = c.DeletePortalProductSharingPolicy(ctx, &apigatewayv2.DeletePortalProductSharingPolicyInput{
		PortalProductId: aws.String(productID),
	})
	require.NoError(t, err)
	spGone, err := c.GetPortalProductSharingPolicy(ctx, &apigatewayv2.GetPortalProductSharingPolicyInput{
		PortalProductId: aws.String(productID),
	})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(spGone.PolicyDocument), "deleted sharing policy returns no document")

	_, err = c.DeletePortalProduct(ctx, &apigatewayv2.DeletePortalProductInput{PortalProductId: aws.String(productID)})
	require.NoError(t, err)
	_, err = c.GetPortalProduct(ctx, &apigatewayv2.GetPortalProductInput{PortalProductId: aws.String(productID)})
	require.Error(t, err, "deleted portal product must 404")
}

// TestAPIGatewayV2_ProductPagesSDK exercises product pages and product
// REST-endpoint pages: Create/Get/List/Update/Delete ProductPage +
// Create/Get/List/Update/Delete ProductRestEndpointPage.
func TestAPIGatewayV2_ProductPagesSDK(t *testing.T) {
	c := apigwv2Client()
	prod, err := c.CreatePortalProduct(ctx, &apigatewayv2.CreatePortalProductInput{
		DisplayName: aws.String("pages-product"),
	})
	require.NoError(t, err)
	productID := aws.ToString(prod.PortalProductId)
	t.Cleanup(func() {
		_, _ = c.DeletePortalProduct(ctx, &apigatewayv2.DeletePortalProductInput{PortalProductId: aws.String(productID)})
	})

	// Product page round-trip.
	page, err := c.CreateProductPage(ctx, &apigatewayv2.CreateProductPageInput{
		PortalProductId: aws.String(productID),
		DisplayContent: &apigwv2types.DisplayContent{
			Title: aws.String("Getting Started"),
			Body:  aws.String("# Welcome"),
		},
	})
	require.NoError(t, err)
	pageID := aws.ToString(page.ProductPageId)
	require.NotEmpty(t, pageID)

	gotPage, err := c.GetProductPage(ctx, &apigatewayv2.GetProductPageInput{
		PortalProductId: aws.String(productID), ProductPageId: aws.String(pageID),
	})
	require.NoError(t, err)
	require.NotNil(t, gotPage.DisplayContent)
	assert.Equal(t, "Getting Started", aws.ToString(gotPage.DisplayContent.Title))

	pageList, err := c.ListProductPages(ctx, &apigatewayv2.ListProductPagesInput{
		PortalProductId: aws.String(productID),
	})
	require.NoError(t, err)
	require.Len(t, pageList.Items, 1)
	assert.Equal(t, "Getting Started", aws.ToString(pageList.Items[0].PageTitle))

	updPage, err := c.UpdateProductPage(ctx, &apigatewayv2.UpdateProductPageInput{
		PortalProductId: aws.String(productID), ProductPageId: aws.String(pageID),
		DisplayContent: &apigwv2types.DisplayContent{
			Title: aws.String("Getting Started v2"),
			Body:  aws.String("# Welcome v2"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Getting Started v2", aws.ToString(updPage.DisplayContent.Title))

	_, err = c.DeleteProductPage(ctx, &apigatewayv2.DeleteProductPageInput{
		PortalProductId: aws.String(productID), ProductPageId: aws.String(pageID),
	})
	require.NoError(t, err)
	_, err = c.GetProductPage(ctx, &apigatewayv2.GetProductPageInput{
		PortalProductId: aws.String(productID), ProductPageId: aws.String(pageID),
	})
	require.Error(t, err, "deleted product page must 404")

	// Product REST-endpoint page round-trip.
	rep, err := c.CreateProductRestEndpointPage(ctx, &apigatewayv2.CreateProductRestEndpointPageInput{
		PortalProductId: aws.String(productID),
		RestEndpointIdentifier: &apigwv2types.RestEndpointIdentifier{
			IdentifierParts: &apigwv2types.IdentifierParts{
				Method:    aws.String("GET"),
				Path:      aws.String("/orders"),
				RestApiId: aws.String("abc1234567"),
				Stage:     aws.String("prod"),
			},
		},
		TryItState: apigwv2types.TryItStateEnabled,
		DisplayContent: &apigwv2types.EndpointDisplayContent{
			Overrides: &apigwv2types.DisplayContentOverrides{
				Body:          aws.String("list orders"),
				OperationName: aws.String("ListOrders"),
			},
		},
	})
	require.NoError(t, err)
	repID := aws.ToString(rep.ProductRestEndpointPageId)
	require.NotEmpty(t, repID)

	gotRep, err := c.GetProductRestEndpointPage(ctx, &apigatewayv2.GetProductRestEndpointPageInput{
		PortalProductId: aws.String(productID), ProductRestEndpointPageId: aws.String(repID),
	})
	require.NoError(t, err)
	assert.Equal(t, apigwv2types.TryItStateEnabled, gotRep.TryItState)

	repList, err := c.ListProductRestEndpointPages(ctx, &apigatewayv2.ListProductRestEndpointPagesInput{
		PortalProductId: aws.String(productID),
	})
	require.NoError(t, err)
	require.Len(t, repList.Items, 1)

	updRep, err := c.UpdateProductRestEndpointPage(ctx, &apigatewayv2.UpdateProductRestEndpointPageInput{
		PortalProductId: aws.String(productID), ProductRestEndpointPageId: aws.String(repID),
		TryItState: apigwv2types.TryItStateDisabled,
	})
	require.NoError(t, err)
	assert.Equal(t, apigwv2types.TryItStateDisabled, updRep.TryItState)

	_, err = c.DeleteProductRestEndpointPage(ctx, &apigatewayv2.DeleteProductRestEndpointPageInput{
		PortalProductId: aws.String(productID), ProductRestEndpointPageId: aws.String(repID),
	})
	require.NoError(t, err)
	_, err = c.GetProductRestEndpointPage(ctx, &apigatewayv2.GetProductRestEndpointPageInput{
		PortalProductId: aws.String(productID), ProductRestEndpointPageId: aws.String(repID),
	})
	require.Error(t, err, "deleted REST-endpoint page must 404")
}

// TestAPIGatewayV2_RoutingRulesSDK exercises routing rules under a domain name:
// CreateRoutingRule, GetRoutingRule, ListRoutingRules, PutRoutingRule,
// DeleteRoutingRule.
func TestAPIGatewayV2_RoutingRulesSDK(t *testing.T) {
	c := apigwv2Client()
	dn := fmt.Sprintf("rr-%d.example.com", time.Now().UnixNano())
	_, err := c.CreateDomainName(ctx, &apigatewayv2.CreateDomainNameInput{DomainName: aws.String(dn)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDomainName(ctx, &apigatewayv2.DeleteDomainNameInput{DomainName: aws.String(dn)})
	})

	create, err := c.CreateRoutingRule(ctx, &apigatewayv2.CreateRoutingRuleInput{
		DomainName: aws.String(dn),
		Priority:   aws.Int32(10),
		Conditions: []apigwv2types.RoutingRuleCondition{{
			MatchBasePaths: &apigwv2types.RoutingRuleMatchBasePaths{AnyOf: []string{"orders"}},
		}},
		Actions: []apigwv2types.RoutingRuleAction{{
			InvokeApi: &apigwv2types.RoutingRuleActionInvokeApi{
				ApiId: aws.String("abc1234567"),
				Stage: aws.String("prod"),
			},
		}},
	})
	require.NoError(t, err)
	ruleID := aws.ToString(create.RoutingRuleId)
	require.NotEmpty(t, ruleID)
	require.NotEmpty(t, aws.ToString(create.RoutingRuleArn))
	assert.Equal(t, int32(10), aws.ToInt32(create.Priority))

	got, err := c.GetRoutingRule(ctx, &apigatewayv2.GetRoutingRuleInput{
		DomainName: aws.String(dn), RoutingRuleId: aws.String(ruleID),
	})
	require.NoError(t, err)
	require.Len(t, got.Actions, 1)
	require.NotNil(t, got.Actions[0].InvokeApi)
	assert.Equal(t, "abc1234567", aws.ToString(got.Actions[0].InvokeApi.ApiId))

	list, err := c.ListRoutingRules(ctx, &apigatewayv2.ListRoutingRulesInput{DomainName: aws.String(dn)})
	require.NoError(t, err)
	require.Len(t, list.RoutingRules, 1)

	put, err := c.PutRoutingRule(ctx, &apigatewayv2.PutRoutingRuleInput{
		DomainName:    aws.String(dn),
		RoutingRuleId: aws.String(ruleID),
		Priority:      aws.Int32(20),
		Conditions: []apigwv2types.RoutingRuleCondition{{
			MatchBasePaths: &apigwv2types.RoutingRuleMatchBasePaths{AnyOf: []string{"invoices"}},
		}},
		Actions: []apigwv2types.RoutingRuleAction{{
			InvokeApi: &apigwv2types.RoutingRuleActionInvokeApi{
				ApiId: aws.String("abc1234567"),
				Stage: aws.String("prod"),
			},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(20), aws.ToInt32(put.Priority))

	_, err = c.DeleteRoutingRule(ctx, &apigatewayv2.DeleteRoutingRuleInput{
		DomainName: aws.String(dn), RoutingRuleId: aws.String(ruleID),
	})
	require.NoError(t, err)
	_, err = c.GetRoutingRule(ctx, &apigatewayv2.GetRoutingRuleInput{
		DomainName: aws.String(dn), RoutingRuleId: aws.String(ruleID),
	})
	require.Error(t, err, "deleted routing rule must 404")
}

// TestAPIGatewayV2_ApiUpdatesSDK exercises UpdateApi, UpdateApiMapping, and
// ResetAuthorizersCache against the existing api / api-mapping / stage stores.
func TestAPIGatewayV2_ApiUpdatesSDK(t *testing.T) {
	c := apigwv2Client()
	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("update-api"),
		ProtocolType: apigwv2types.ProtocolTypeHttp,
	})
	require.NoError(t, err)
	apiID := aws.ToString(api.ApiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiID)}) })

	upd, err := c.UpdateApi(ctx, &apigatewayv2.UpdateApiInput{
		ApiId: aws.String(apiID),
		Name:  aws.String("update-api-renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, "update-api-renamed", aws.ToString(upd.Name))

	stage, err := c.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId:     aws.String(apiID),
		StageName: aws.String("prod"),
	})
	require.NoError(t, err)
	require.Equal(t, "prod", aws.ToString(stage.StageName))

	_, err = c.ResetAuthorizersCache(ctx, &apigatewayv2.ResetAuthorizersCacheInput{
		ApiId: aws.String(apiID), StageName: aws.String("prod"),
	})
	require.NoError(t, err)

	// UpdateApiMapping against a domain name.
	dn := fmt.Sprintf("map-%d.example.com", time.Now().UnixNano())
	_, err = c.CreateDomainName(ctx, &apigatewayv2.CreateDomainNameInput{DomainName: aws.String(dn)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDomainName(ctx, &apigatewayv2.DeleteDomainNameInput{DomainName: aws.String(dn)})
	})

	mapping, err := c.CreateApiMapping(ctx, &apigatewayv2.CreateApiMappingInput{
		DomainName: aws.String(dn),
		ApiId:      aws.String(apiID),
		Stage:      aws.String("prod"),
	})
	require.NoError(t, err)
	mappingID := aws.ToString(mapping.ApiMappingId)

	updMap, err := c.UpdateApiMapping(ctx, &apigatewayv2.UpdateApiMappingInput{
		DomainName:    aws.String(dn),
		ApiMappingId:  aws.String(mappingID),
		ApiId:         aws.String(apiID),
		ApiMappingKey: aws.String("v1"),
		Stage:         aws.String("prod"),
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", aws.ToString(updMap.ApiMappingKey))
}
