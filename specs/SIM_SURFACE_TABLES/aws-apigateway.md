# Sim surface — aws-apigateway

Surface registered in `simulator-aws/apigateway.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /restapis` | ✓ `simulator-aws/apigateway.go:231::handleAPIGWCreateRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis` | ✓ `simulator-aws/apigateway.go:232::handleAPIGWListRestApis` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:233::handleAPIGWGetRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:234::handleAPIGWDeleteRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{parentId}` | ✓ `simulator-aws/apigateway.go:235::handleAPIGWCreateResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources` | ✓ `simulator-aws/apigateway.go:236::handleAPIGWListResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:237::handleAPIGWGetResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:238::handleAPIGWDeleteResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:239::handleAPIGWPutMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:240::handleAPIGWGetMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:241::handleAPIGWDeleteMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:242::handleAPIGWPutIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:243::handleAPIGWGetIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:244::handleAPIGWDeleteIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:245::handleAPIGWCreateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:246::handleAPIGWListDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:247::handleAPIGWGetDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:248::handleAPIGWDeleteDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:249::handleAPIGWCreateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:250::handleAPIGWListStages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:251::handleAPIGWGetStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:252::handleAPIGWDeleteStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:259::handleAPIGWPutMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:260::handleAPIGWGetMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:261::handleAPIGWDeleteMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:262::handleAPIGWPutIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:263::handleAPIGWGetIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:264::handleAPIGWDeleteIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apikeys` | ✓ `simulator-aws/apigateway.go:268::handleAPIGWCreateApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys` | ✓ `simulator-aws/apigateway.go:269::handleAPIGWListApiKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:270::handleAPIGWGetApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:271::handleAPIGWUpdateApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:272::handleAPIGWDeleteApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans` | ✓ `simulator-aws/apigateway.go:276::handleAPIGWCreateUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans` | ✓ `simulator-aws/apigateway.go:277::handleAPIGWListUsagePlans` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:278::handleAPIGWGetUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:279::handleAPIGWUpdateUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:280::handleAPIGWDeleteUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:281::handleAPIGWCreateUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:282::handleAPIGWListUsagePlanKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:283::handleAPIGWGetUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:284::handleAPIGWDeleteUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:287::handleAPIGWCreateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:288::handleAPIGWListModels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:289::handleAPIGWGetModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:290::handleAPIGWDeleteModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:291::handleAPIGWCreateRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:292::handleAPIGWListRequestValidators` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway.go:293::handleAPIGWDeleteRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:294::handleAPIGWCreateAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:295::handleAPIGWListAuthorizers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:296::handleAPIGWGetAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:297::handleAPIGWDeleteAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:103::handleAPIGWUpdateRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway_complete.go:104::handleAPIGWUpdateResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:105::handleAPIGWUpdateMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway_complete.go:106::handleAPIGWUpdateMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway_complete.go:107::handleAPIGWUpdateIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway_complete.go:108::handleAPIGWUpdateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_complete.go:109::handleAPIGWUpdateRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway_complete.go:110::handleAPIGWUpdateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway_complete.go:111::handleAPIGWUpdateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}` | ✓ `simulator-aws/apigateway_complete.go:112::handleAPIGWUpdateDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigateway_complete.go:113::handleAPIGWUpdateVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}/keys/{keyId}/usage` | ✓ `simulator-aws/apigateway_complete.go:114::handleAPIGWUpdateUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:117::handleAPIGWTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:118::handleAPIGWUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:127::handleAPIGWPutRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/data` | ✓ `simulator-aws/apigateway_complete.go:131::handleAPIGWFlushStageCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/authorizers` | ✓ `simulator-aws/apigateway_complete.go:132::handleAPIGWFlushStageAuthorizersCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:135::handleAPIGWTestInvokeMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway_complete.go:136::handleAPIGWTestInvokeAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:139::handleAPIGWCreateDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:140::handleAPIGWGetDomainNameAccessAssociations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnameaccessassociations/{domainNameAccessAssociationArn}` | ✓ `simulator-aws/apigateway_complete.go:141::handleAPIGWDeleteDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /rejectdomainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:142::handleAPIGWRejectDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:193::handleAPIGWCreateBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:194::handleAPIGWListBasePathMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:195::handleAPIGWGetBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:196::handleAPIGWUpdateBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:197::handleAPIGWDeleteBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:200::handleAPIGWGenerateClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:201::handleAPIGWListClientCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:202::handleAPIGWGetClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:203::handleAPIGWUpdateClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:204::handleAPIGWDeleteClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:207::handleAPIGWCreateDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:208::handleAPIGWListDocumentationParts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:209::handleAPIGWImportDocumentationParts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:210::handleAPIGWGetDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:211::handleAPIGWUpdateDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:212::handleAPIGWDeleteDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:215::handleAPIGWCreateDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:216::handleAPIGWListDocumentationVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:217::handleAPIGWGetDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:218::handleAPIGWUpdateDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:219::handleAPIGWDeleteDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:222::handleAPIGWPutGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses` | ✓ `simulator-aws/apigateway_extras.go:223::handleAPIGWListGatewayResponses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:224::handleAPIGWGetGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:225::handleAPIGWUpdateGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:226::handleAPIGWDeleteGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_extras.go:229::handleAPIGWGetRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}/default_template` | ✓ `simulator-aws/apigateway_extras.go:230::handleAPIGWGetModelTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /account` | ✓ `simulator-aws/apigateway_extras.go:233::handleAPIGWGetAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /account` | ✓ `simulator-aws/apigateway_extras.go:234::handleAPIGWUpdateAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/exports/{exportType}` | ✓ `simulator-aws/apigateway_extras.go:237::handleAPIGWGetExport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/sdks/{sdkType}` | ✓ `simulator-aws/apigateway_extras.go:238::handleAPIGWGetSdk` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes` | ✓ `simulator-aws/apigateway_extras.go:241::handleAPIGWListSdkTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes/{id}` | ✓ `simulator-aws/apigateway_extras.go:242::handleAPIGWGetSdkType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/usage` | ✓ `simulator-aws/apigateway_extras.go:245::handleAPIGWGetUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_extras.go:246::handleAPIGWGetTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
