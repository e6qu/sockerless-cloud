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
| `POST /restapis` | ✓ `simulator-aws/apigateway.go:214::handleAPIGWCreateRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis` | ✓ `simulator-aws/apigateway.go:215::handleAPIGWListRestApis` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:216::handleAPIGWGetRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:217::handleAPIGWDeleteRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{parentId}` | ✓ `simulator-aws/apigateway.go:218::handleAPIGWCreateResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources` | ✓ `simulator-aws/apigateway.go:219::handleAPIGWListResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:220::handleAPIGWGetResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:221::handleAPIGWDeleteResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:222::handleAPIGWPutMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:223::handleAPIGWGetMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:224::handleAPIGWDeleteMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:225::handleAPIGWPutIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:226::handleAPIGWGetIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:227::handleAPIGWDeleteIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:228::handleAPIGWCreateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:229::handleAPIGWListDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:230::handleAPIGWGetDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:231::handleAPIGWDeleteDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:232::handleAPIGWCreateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:233::handleAPIGWListStages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:234::handleAPIGWGetStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:235::handleAPIGWDeleteStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:239::handleAPIGWPutMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:240::handleAPIGWGetMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:241::handleAPIGWDeleteMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:242::handleAPIGWPutIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:243::handleAPIGWGetIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:244::handleAPIGWDeleteIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apikeys` | ✓ `simulator-aws/apigateway.go:247::handleAPIGWCreateApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys` | ✓ `simulator-aws/apigateway.go:248::handleAPIGWListApiKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:249::handleAPIGWGetApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:250::handleAPIGWUpdateApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:251::handleAPIGWDeleteApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans` | ✓ `simulator-aws/apigateway.go:254::handleAPIGWCreateUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans` | ✓ `simulator-aws/apigateway.go:255::handleAPIGWListUsagePlans` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:256::handleAPIGWGetUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:257::handleAPIGWUpdateUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:258::handleAPIGWDeleteUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:259::handleAPIGWCreateUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:260::handleAPIGWListUsagePlanKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:261::handleAPIGWGetUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:262::handleAPIGWDeleteUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:264::handleAPIGWCreateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:265::handleAPIGWListModels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:266::handleAPIGWGetModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:267::handleAPIGWDeleteModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:268::handleAPIGWCreateRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:269::handleAPIGWListRequestValidators` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway.go:270::handleAPIGWDeleteRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:271::handleAPIGWCreateAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:272::handleAPIGWListAuthorizers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:273::handleAPIGWGetAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:274::handleAPIGWDeleteAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:104::handleAPIGWPutRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/data` | ✓ `simulator-aws/apigateway_complete.go:107::handleAPIGWFlushStageCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/authorizers` | ✓ `simulator-aws/apigateway_complete.go:108::handleAPIGWFlushStageAuthorizersCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:110::handleAPIGWTestInvokeMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway_complete.go:111::handleAPIGWTestInvokeAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:114::handleAPIGWCreateDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:115::handleAPIGWGetDomainNameAccessAssociations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnameaccessassociations/{domainNameAccessAssociationArn}` | ✓ `simulator-aws/apigateway_complete.go:116::handleAPIGWDeleteDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /rejectdomainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:117::handleAPIGWRejectDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:82::handleAPIGWUpdateRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway_complete.go:83::handleAPIGWUpdateResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:84::handleAPIGWUpdateMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway_complete.go:85::handleAPIGWUpdateMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway_complete.go:86::handleAPIGWUpdateIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway_complete.go:87::handleAPIGWUpdateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_complete.go:88::handleAPIGWUpdateRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway_complete.go:89::handleAPIGWUpdateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway_complete.go:90::handleAPIGWUpdateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}` | ✓ `simulator-aws/apigateway_complete.go:91::handleAPIGWUpdateDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigateway_complete.go:92::handleAPIGWUpdateVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}/keys/{keyId}/usage` | ✓ `simulator-aws/apigateway_complete.go:93::handleAPIGWUpdateUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:96::handleAPIGWTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:97::handleAPIGWUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:171::handleAPIGWCreateBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:172::handleAPIGWListBasePathMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:173::handleAPIGWGetBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:174::handleAPIGWUpdateBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:175::handleAPIGWDeleteBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:177::handleAPIGWGenerateClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:178::handleAPIGWListClientCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:179::handleAPIGWGetClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:180::handleAPIGWUpdateClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:181::handleAPIGWDeleteClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:183::handleAPIGWCreateDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:184::handleAPIGWListDocumentationParts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:185::handleAPIGWImportDocumentationParts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:186::handleAPIGWGetDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:187::handleAPIGWUpdateDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:188::handleAPIGWDeleteDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:190::handleAPIGWCreateDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:191::handleAPIGWListDocumentationVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:192::handleAPIGWGetDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:193::handleAPIGWUpdateDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:194::handleAPIGWDeleteDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:196::handleAPIGWPutGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses` | ✓ `simulator-aws/apigateway_extras.go:197::handleAPIGWListGatewayResponses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:198::handleAPIGWGetGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:199::handleAPIGWUpdateGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:200::handleAPIGWDeleteGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_extras.go:202::handleAPIGWGetRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}/default_template` | ✓ `simulator-aws/apigateway_extras.go:203::handleAPIGWGetModelTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /account` | ✓ `simulator-aws/apigateway_extras.go:205::handleAPIGWGetAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /account` | ✓ `simulator-aws/apigateway_extras.go:206::handleAPIGWUpdateAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/exports/{exportType}` | ✓ `simulator-aws/apigateway_extras.go:208::handleAPIGWGetExport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/sdks/{sdkType}` | ✓ `simulator-aws/apigateway_extras.go:209::handleAPIGWGetSdk` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes` | ✓ `simulator-aws/apigateway_extras.go:211::handleAPIGWListSdkTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes/{id}` | ✓ `simulator-aws/apigateway_extras.go:212::handleAPIGWGetSdkType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/usage` | ✓ `simulator-aws/apigateway_extras.go:214::handleAPIGWGetUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_extras.go:215::handleAPIGWGetTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
