# Sim surface — aws-apigatewayv2

Surface registered in `simulator-aws/apigatewayv2.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did. It does not follow that the answer is built from what it read: a handler that looks its parent up and then answers a fixed body reaches state and is marked ✓
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v2/apis` | ✓ `simulator-aws/apigatewayv2.go:182::handleAPIGWv2CreateApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis` | ✓ `simulator-aws/apigatewayv2.go:183::handleAPIGWv2ListApis` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2.go:184::handleAPIGWv2GetApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2.go:185::handleAPIGWv2DeleteApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/routes` | ✓ `simulator-aws/apigatewayv2.go:186::handleAPIGWv2CreateRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes` | ✓ `simulator-aws/apigatewayv2.go:187::handleAPIGWv2ListRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes/{routeId}` | ✓ `simulator-aws/apigatewayv2.go:188::handleAPIGWv2GetRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/routes/{routeId}` | ✓ `simulator-aws/apigatewayv2.go:189::handleAPIGWv2UpdateRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/routes/{routeId}` | ✓ `simulator-aws/apigatewayv2.go:190::handleAPIGWv2DeleteRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/integrations` | ✓ `simulator-aws/apigatewayv2.go:191::handleAPIGWv2CreateIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations` | ✓ `simulator-aws/apigatewayv2.go:192::handleAPIGWv2ListIntegrations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations/{integrationId}` | ✓ `simulator-aws/apigatewayv2.go:193::handleAPIGWv2GetIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/integrations/{integrationId}` | ✓ `simulator-aws/apigatewayv2.go:194::handleAPIGWv2UpdateIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/integrations/{integrationId}` | ✓ `simulator-aws/apigatewayv2.go:195::handleAPIGWv2DeleteIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/stages` | ✓ `simulator-aws/apigatewayv2.go:196::handleAPIGWv2CreateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/stages` | ✓ `simulator-aws/apigatewayv2.go:197::handleAPIGWv2ListStages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/stages/{stageName}` | ✓ `simulator-aws/apigatewayv2.go:198::handleAPIGWv2GetStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/stages/{stageName}` | ✓ `simulator-aws/apigatewayv2.go:199::handleAPIGWv2UpdateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}` | ✓ `simulator-aws/apigatewayv2.go:200::handleAPIGWv2DeleteStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/deployments` | ✓ `simulator-aws/apigatewayv2.go:201::handleAPIGWv2CreateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/deployments` | ✓ `simulator-aws/apigatewayv2.go:202::handleAPIGWv2ListDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigatewayv2.go:203::handleAPIGWv2GetDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigatewayv2.go:204::handleAPIGWv2DeleteDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/authorizers` | ✓ `simulator-aws/apigatewayv2.go:207::handleAPIGWv2CreateAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/authorizers` | ✓ `simulator-aws/apigatewayv2.go:208::handleAPIGWv2ListAuthorizers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigatewayv2.go:209::handleAPIGWv2GetAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigatewayv2.go:210::handleAPIGWv2UpdateAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigatewayv2.go:211::handleAPIGWv2DeleteAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/models` | ✓ `simulator-aws/apigatewayv2.go:214::handleAPIGWv2CreateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/models` | ✓ `simulator-aws/apigatewayv2.go:215::handleAPIGWv2ListModels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/models/{modelId}` | ✓ `simulator-aws/apigatewayv2.go:216::handleAPIGWv2GetModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/models/{modelId}` | ✓ `simulator-aws/apigatewayv2.go:217::handleAPIGWv2DeleteModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/domainnames` | ✓ `simulator-aws/apigatewayv2.go:221::handleAPIGWv2CreateDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames` | ✓ `simulator-aws/apigatewayv2.go:222::handleAPIGWv2ListDomainNames` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}` | ✓ `simulator-aws/apigatewayv2.go:223::handleAPIGWv2GetDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/domainnames/{domainName}` | ✓ `simulator-aws/apigatewayv2.go:224::handleAPIGWv2DeleteDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/domainnames/{domainName}/apimappings` | ✓ `simulator-aws/apigatewayv2.go:225::handleAPIGWv2CreateApiMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/apimappings` | ✓ `simulator-aws/apigatewayv2.go:226::handleAPIGWv2ListApiMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/apimappings/{apiMappingId}` | ✓ `simulator-aws/apigatewayv2.go:227::handleAPIGWv2GetApiMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/domainnames/{domainName}/apimappings/{apiMappingId}` | ✓ `simulator-aws/apigatewayv2.go:228::handleAPIGWv2DeleteApiMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/vpclinks` | ✓ `simulator-aws/apigatewayv2.go:232::handleAPIGWv2CreateVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/vpclinks` | ✓ `simulator-aws/apigatewayv2.go:233::handleAPIGWv2ListVpcLinks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigatewayv2.go:234::handleAPIGWv2GetVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigatewayv2.go:235::handleAPIGWv2DeleteVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portals` | ✓ `simulator-aws/apigatewayv2_complete.go:123::handleAPIGWv2CreatePortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portals` | ✓ `simulator-aws/apigatewayv2_complete.go:124::handleAPIGWv2ListPortals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portals/{portalId}` | ✓ `simulator-aws/apigatewayv2_complete.go:125::handleAPIGWv2GetPortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portals/{portalId}` | ✓ `simulator-aws/apigatewayv2_complete.go:126::handleAPIGWv2UpdatePortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portals/{portalId}` | ✓ `simulator-aws/apigatewayv2_complete.go:127::handleAPIGWv2DeletePortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portals/{portalId}/preview` | ✓ `simulator-aws/apigatewayv2_complete.go:128::handleAPIGWv2PreviewPortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portals/{portalId}/publish` | ✓ `simulator-aws/apigatewayv2_complete.go:129::handleAPIGWv2PublishPortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portals/{portalId}/publish` | ✓ `simulator-aws/apigatewayv2_complete.go:130::handleAPIGWv2DisablePortal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portalproducts` | ✓ `simulator-aws/apigatewayv2_complete.go:133::handleAPIGWv2CreatePortalProduct` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts` | ✓ `simulator-aws/apigatewayv2_complete.go:134::handleAPIGWv2ListPortalProducts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}` | ✓ `simulator-aws/apigatewayv2_complete.go:135::handleAPIGWv2GetPortalProduct` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portalproducts/{portalProductId}` | ✓ `simulator-aws/apigatewayv2_complete.go:136::handleAPIGWv2UpdatePortalProduct` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}` | ✓ `simulator-aws/apigatewayv2_complete.go:137::handleAPIGWv2DeletePortalProduct` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/sharingpolicy` | ✓ `simulator-aws/apigatewayv2_complete.go:140::handleAPIGWv2GetPortalProductSharingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/portalproducts/{portalProductId}/sharingpolicy` | ✓ `simulator-aws/apigatewayv2_complete.go:141::handleAPIGWv2PutPortalProductSharingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}/sharingpolicy` | ✓ `simulator-aws/apigatewayv2_complete.go:142::handleAPIGWv2DeletePortalProductSharingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portalproducts/{portalProductId}/productpages` | ✓ `simulator-aws/apigatewayv2_complete.go:145::handleAPIGWv2CreateProductPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productpages` | ✓ `simulator-aws/apigatewayv2_complete.go:146::handleAPIGWv2ListProductPages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productpages/{productPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:147::handleAPIGWv2GetProductPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portalproducts/{portalProductId}/productpages/{productPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:148::handleAPIGWv2UpdateProductPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}/productpages/{productPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:149::handleAPIGWv2DeleteProductPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portalproducts/{portalProductId}/productrestendpointpages` | ✓ `simulator-aws/apigatewayv2_complete.go:152::handleAPIGWv2CreateProductRestEndpointPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productrestendpointpages` | ✓ `simulator-aws/apigatewayv2_complete.go:153::handleAPIGWv2ListProductRestEndpointPages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:154::handleAPIGWv2GetProductRestEndpointPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:155::handleAPIGWv2UpdateProductRestEndpointPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:156::handleAPIGWv2DeleteProductRestEndpointPage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/domainnames/{domainName}/routingrules` | ✓ `simulator-aws/apigatewayv2_complete.go:159::handleAPIGWv2CreateRoutingRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/routingrules` | ✓ `simulator-aws/apigatewayv2_complete.go:160::handleAPIGWv2ListRoutingRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/routingrules/{routingRuleId}` | ✓ `simulator-aws/apigatewayv2_complete.go:161::handleAPIGWv2GetRoutingRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/domainnames/{domainName}/routingrules/{routingRuleId}` | ✓ `simulator-aws/apigatewayv2_complete.go:162::handleAPIGWv2PutRoutingRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/domainnames/{domainName}/routingrules/{routingRuleId}` | ✓ `simulator-aws/apigatewayv2_complete.go:163::handleAPIGWv2DeleteRoutingRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2_complete.go:166::handleAPIGWv2UpdateApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/domainnames/{domainName}/apimappings/{apiMappingId}` | ✓ `simulator-aws/apigatewayv2_complete.go:167::handleAPIGWv2UpdateApiMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}/cache/authorizers` | ✓ `simulator-aws/apigatewayv2_complete.go:170::handleAPIGWv2ResetAuthorizersCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:128::handleAPIGWv2CreateIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:129::handleAPIGWv2ListIntegrationResponses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:130::handleAPIGWv2GetIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:131::handleAPIGWv2UpdateIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:132::handleAPIGWv2DeleteIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/routes/{routeId}/routeresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:135::handleAPIGWv2CreateRouteResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes/{routeId}/routeresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:136::handleAPIGWv2ListRouteResponses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:137::handleAPIGWv2GetRouteResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:138::handleAPIGWv2UpdateRouteResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:139::handleAPIGWv2DeleteRouteResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/models/{modelId}/template` | ✓ `simulator-aws/apigatewayv2_extras.go:142::handleAPIGWv2GetModelTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/apis` | ✓ `simulator-aws/apigatewayv2_extras.go:145::handleAPIGWv2ImportApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2_extras.go:146::handleAPIGWv2ReimportApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/exports/{specification}` | ✓ `simulator-aws/apigatewayv2_extras.go:147::handleAPIGWv2ExportApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/tags/{resourceArn}` | ✓ `simulator-aws/apigatewayv2_extras.go:150::handleAPIGWv2GetTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}/accesslogsettings` | ✓ `simulator-aws/apigatewayv2_extras.go:153::handleAPIGWv2DeleteAccessLogSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/cors` | ✓ `simulator-aws/apigatewayv2_extras.go:154::handleAPIGWv2DeleteCorsConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/routes/{routeId}/requestparameters/{requestParameterKey}` | ✓ `simulator-aws/apigatewayv2_extras.go:155::handleAPIGWv2DeleteRouteRequestParameter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}/routesettings/{routeKey}` | ✓ `simulator-aws/apigatewayv2_extras.go:156::handleAPIGWv2DeleteRouteSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
