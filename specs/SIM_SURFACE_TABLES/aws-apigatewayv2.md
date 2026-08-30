# Sim surface — aws-apigatewayv2

Surface registered in `simulator-aws/apigatewayv2.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v2/apis` | ✓ `simulator-aws/apigatewayv2.go:182::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis` | ✓ `simulator-aws/apigatewayv2.go:183::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2.go:184::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2.go:185::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/routes` | ✓ `simulator-aws/apigatewayv2.go:186::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes` | ✓ `simulator-aws/apigatewayv2.go:187::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes/{routeId}` | ✓ `simulator-aws/apigatewayv2.go:188::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/routes/{routeId}` | ✓ `simulator-aws/apigatewayv2.go:189::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/routes/{routeId}` | ✓ `simulator-aws/apigatewayv2.go:190::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/integrations` | ✓ `simulator-aws/apigatewayv2.go:191::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations` | ✓ `simulator-aws/apigatewayv2.go:192::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations/{integrationId}` | ✓ `simulator-aws/apigatewayv2.go:193::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/integrations/{integrationId}` | ✓ `simulator-aws/apigatewayv2.go:194::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/integrations/{integrationId}` | ✓ `simulator-aws/apigatewayv2.go:195::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/stages` | ✓ `simulator-aws/apigatewayv2.go:196::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/stages` | ✓ `simulator-aws/apigatewayv2.go:197::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/stages/{stageName}` | ✓ `simulator-aws/apigatewayv2.go:198::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/stages/{stageName}` | ✓ `simulator-aws/apigatewayv2.go:199::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}` | ✓ `simulator-aws/apigatewayv2.go:200::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/deployments` | ✓ `simulator-aws/apigatewayv2.go:201::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/deployments` | ✓ `simulator-aws/apigatewayv2.go:202::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigatewayv2.go:203::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigatewayv2.go:204::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/authorizers` | ✓ `simulator-aws/apigatewayv2.go:207::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/authorizers` | ✓ `simulator-aws/apigatewayv2.go:208::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigatewayv2.go:209::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigatewayv2.go:210::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigatewayv2.go:211::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/models` | ✓ `simulator-aws/apigatewayv2.go:214::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/models` | ✓ `simulator-aws/apigatewayv2.go:215::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/models/{modelId}` | ✓ `simulator-aws/apigatewayv2.go:216::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/models/{modelId}` | ✓ `simulator-aws/apigatewayv2.go:217::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/domainnames` | ✓ `simulator-aws/apigatewayv2.go:221::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames` | ✓ `simulator-aws/apigatewayv2.go:222::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}` | ✓ `simulator-aws/apigatewayv2.go:223::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/domainnames/{domainName}` | ✓ `simulator-aws/apigatewayv2.go:224::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/domainnames/{domainName}/apimappings` | ✓ `simulator-aws/apigatewayv2.go:225::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/apimappings` | ✓ `simulator-aws/apigatewayv2.go:226::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/apimappings/{apiMappingId}` | ✓ `simulator-aws/apigatewayv2.go:227::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/domainnames/{domainName}/apimappings/{apiMappingId}` | ✓ `simulator-aws/apigatewayv2.go:228::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/vpclinks` | ✓ `simulator-aws/apigatewayv2.go:232::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/vpclinks` | ✓ `simulator-aws/apigatewayv2.go:233::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigatewayv2.go:234::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigatewayv2.go:235::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portals` | ✓ `simulator-aws/apigatewayv2_complete.go:123::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portals` | ✓ `simulator-aws/apigatewayv2_complete.go:124::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portals/{portalId}` | ✓ `simulator-aws/apigatewayv2_complete.go:125::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portals/{portalId}` | ✓ `simulator-aws/apigatewayv2_complete.go:126::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portals/{portalId}` | ✓ `simulator-aws/apigatewayv2_complete.go:127::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portals/{portalId}/preview` | ✓ `simulator-aws/apigatewayv2_complete.go:128::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portals/{portalId}/publish` | ✓ `simulator-aws/apigatewayv2_complete.go:129::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portals/{portalId}/publish` | ✓ `simulator-aws/apigatewayv2_complete.go:130::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portalproducts` | ✓ `simulator-aws/apigatewayv2_complete.go:133::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts` | ✓ `simulator-aws/apigatewayv2_complete.go:134::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}` | ✓ `simulator-aws/apigatewayv2_complete.go:135::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portalproducts/{portalProductId}` | ✓ `simulator-aws/apigatewayv2_complete.go:136::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}` | ✓ `simulator-aws/apigatewayv2_complete.go:137::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/sharingpolicy` | ✓ `simulator-aws/apigatewayv2_complete.go:140::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/portalproducts/{portalProductId}/sharingpolicy` | ✓ `simulator-aws/apigatewayv2_complete.go:141::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}/sharingpolicy` | ✓ `simulator-aws/apigatewayv2_complete.go:142::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portalproducts/{portalProductId}/productpages` | ✓ `simulator-aws/apigatewayv2_complete.go:145::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productpages` | ✓ `simulator-aws/apigatewayv2_complete.go:146::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productpages/{productPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:147::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portalproducts/{portalProductId}/productpages/{productPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:148::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}/productpages/{productPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:149::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/portalproducts/{portalProductId}/productrestendpointpages` | ✓ `simulator-aws/apigatewayv2_complete.go:152::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productrestendpointpages` | ✓ `simulator-aws/apigatewayv2_complete.go:153::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:154::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:155::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}` | ✓ `simulator-aws/apigatewayv2_complete.go:156::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/domainnames/{domainName}/routingrules` | ✓ `simulator-aws/apigatewayv2_complete.go:159::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/routingrules` | ✓ `simulator-aws/apigatewayv2_complete.go:160::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/domainnames/{domainName}/routingrules/{routingRuleId}` | ✓ `simulator-aws/apigatewayv2_complete.go:161::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/domainnames/{domainName}/routingrules/{routingRuleId}` | ✓ `simulator-aws/apigatewayv2_complete.go:162::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/domainnames/{domainName}/routingrules/{routingRuleId}` | ✓ `simulator-aws/apigatewayv2_complete.go:163::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2_complete.go:166::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/domainnames/{domainName}/apimappings/{apiMappingId}` | ✓ `simulator-aws/apigatewayv2_complete.go:167::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}/cache/authorizers` | ✓ `simulator-aws/apigatewayv2_complete.go:170::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:128::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:129::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:130::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:131::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:132::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/apis/{apiId}/routes/{routeId}/routeresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:135::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes/{routeId}/routeresponses` | ✓ `simulator-aws/apigatewayv2_extras.go:136::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:137::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:138::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}` | ✓ `simulator-aws/apigatewayv2_extras.go:139::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/models/{modelId}/template` | ✓ `simulator-aws/apigatewayv2_extras.go:142::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/apis` | ✓ `simulator-aws/apigatewayv2_extras.go:145::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/apis/{apiId}` | ✓ `simulator-aws/apigatewayv2_extras.go:146::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/apis/{apiId}/exports/{specification}` | ✓ `simulator-aws/apigatewayv2_extras.go:147::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/tags/{resourceArn}` | ✓ `simulator-aws/apigatewayv2_extras.go:150::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}/accesslogsettings` | ✓ `simulator-aws/apigatewayv2_extras.go:153::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/cors` | ✓ `simulator-aws/apigatewayv2_extras.go:154::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/routes/{routeId}/requestparameters/{requestParameterKey}` | ✓ `simulator-aws/apigatewayv2_extras.go:155::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/apis/{apiId}/stages/{stageName}/routesettings/{routeKey}` | ✓ `simulator-aws/apigatewayv2_extras.go:156::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
