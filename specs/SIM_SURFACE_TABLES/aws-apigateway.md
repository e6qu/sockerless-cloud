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
| `POST /restapis` | ✓ `simulator-aws/apigateway.go:207::handleAPIGWCreateRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis` | ✓ `simulator-aws/apigateway.go:208::handleAPIGWListRestApis` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:209::handleAPIGWGetRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:210::handleAPIGWDeleteRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{parentId}` | ✓ `simulator-aws/apigateway.go:211::handleAPIGWCreateResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources` | ✓ `simulator-aws/apigateway.go:212::handleAPIGWListResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:213::handleAPIGWGetResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:214::handleAPIGWDeleteResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:215::handleAPIGWPutMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:216::handleAPIGWGetMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:217::handleAPIGWDeleteMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:218::handleAPIGWPutIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:219::handleAPIGWGetIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:220::handleAPIGWDeleteIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:221::handleAPIGWCreateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:222::handleAPIGWListDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:223::handleAPIGWGetDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:224::handleAPIGWDeleteDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:225::handleAPIGWCreateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:226::handleAPIGWListStages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:227::handleAPIGWGetStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:228::handleAPIGWDeleteStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:232::handleAPIGWPutMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:233::handleAPIGWGetMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:234::handleAPIGWDeleteMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:235::handleAPIGWPutIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:236::handleAPIGWGetIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:237::handleAPIGWDeleteIntegrationResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apikeys` | ✓ `simulator-aws/apigateway.go:240::handleAPIGWCreateApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys` | ✓ `simulator-aws/apigateway.go:241::handleAPIGWListApiKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:242::handleAPIGWGetApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:243::handleAPIGWUpdateApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:244::handleAPIGWDeleteApiKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans` | ✓ `simulator-aws/apigateway.go:247::handleAPIGWCreateUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans` | ✓ `simulator-aws/apigateway.go:248::handleAPIGWListUsagePlans` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:249::handleAPIGWGetUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:250::handleAPIGWUpdateUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:251::handleAPIGWDeleteUsagePlan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:252::handleAPIGWCreateUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:253::handleAPIGWListUsagePlanKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:254::handleAPIGWGetUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:255::handleAPIGWDeleteUsagePlanKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:257::handleAPIGWCreateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:258::handleAPIGWListModels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:259::handleAPIGWGetModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:260::handleAPIGWDeleteModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:261::handleAPIGWCreateRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:262::handleAPIGWListRequestValidators` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway.go:263::handleAPIGWDeleteRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:264::handleAPIGWCreateAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:265::handleAPIGWListAuthorizers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:266::handleAPIGWGetAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:267::handleAPIGWDeleteAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:100::handleAPIGWTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:101::handleAPIGWUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:108::handleAPIGWPutRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/data` | ✓ `simulator-aws/apigateway_complete.go:111::handleAPIGWFlushStageCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/authorizers` | ✓ `simulator-aws/apigateway_complete.go:112::handleAPIGWFlushStageAuthorizersCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:114::handleAPIGWTestInvokeMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway_complete.go:115::handleAPIGWTestInvokeAuthorizer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:118::handleAPIGWCreateDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:119::handleAPIGWGetDomainNameAccessAssociations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnameaccessassociations/{domainNameAccessAssociationArn}` | ✓ `simulator-aws/apigateway_complete.go:120::handleAPIGWDeleteDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /rejectdomainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:121::handleAPIGWRejectDomainNameAccessAssociation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:86::handleAPIGWUpdateRestApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway_complete.go:87::handleAPIGWUpdateResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:88::handleAPIGWUpdateMethod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway_complete.go:89::handleAPIGWUpdateMethodResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway_complete.go:90::handleAPIGWUpdateIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway_complete.go:91::handleAPIGWUpdateModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_complete.go:92::handleAPIGWUpdateRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway_complete.go:93::handleAPIGWUpdateStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway_complete.go:94::handleAPIGWUpdateDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}` | ✓ `simulator-aws/apigateway_complete.go:95::handleAPIGWUpdateDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigateway_complete.go:96::handleAPIGWUpdateVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}/keys/{keyId}/usage` | ✓ `simulator-aws/apigateway_complete.go:97::handleAPIGWUpdateUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnames` | ✓ `simulator-aws/apigateway_domains.go:66::handleAPIGWCreateDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames` | ✓ `simulator-aws/apigateway_domains.go:67::handleAPIGWGetDomainNames` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}` | ✓ `simulator-aws/apigateway_domains.go:68::handleAPIGWGetDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnames/{domainName}` | ✓ `simulator-aws/apigateway_domains.go:69::handleAPIGWDeleteDomainName` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /vpclinks` | ✓ `simulator-aws/apigateway_domains.go:70::handleAPIGWCreateVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /vpclinks` | ✓ `simulator-aws/apigateway_domains.go:71::handleAPIGWGetVpcLinks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigateway_domains.go:72::handleAPIGWGetVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigateway_domains.go:73::handleAPIGWDeleteVpcLink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:170::handleAPIGWCreateBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:171::handleAPIGWListBasePathMappings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:172::handleAPIGWGetBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:173::handleAPIGWUpdateBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:174::handleAPIGWDeleteBasePathMapping` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:176::handleAPIGWGenerateClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:177::handleAPIGWListClientCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:178::handleAPIGWGetClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:179::handleAPIGWUpdateClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:180::handleAPIGWDeleteClientCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:182::handleAPIGWCreateDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:183::handleAPIGWListDocumentationParts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:184::handleAPIGWImportDocumentationParts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:185::handleAPIGWGetDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:186::handleAPIGWUpdateDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:187::handleAPIGWDeleteDocumentationPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:189::handleAPIGWCreateDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:190::handleAPIGWListDocumentationVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:191::handleAPIGWGetDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:192::handleAPIGWUpdateDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:193::handleAPIGWDeleteDocumentationVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:195::handleAPIGWPutGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses` | ✓ `simulator-aws/apigateway_extras.go:196::handleAPIGWListGatewayResponses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:197::handleAPIGWGetGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:198::handleAPIGWUpdateGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:199::handleAPIGWDeleteGatewayResponse` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_extras.go:201::handleAPIGWGetRequestValidator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}/default_template` | ✓ `simulator-aws/apigateway_extras.go:202::handleAPIGWGetModelTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /account` | ✓ `simulator-aws/apigateway_extras.go:204::handleAPIGWGetAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /account` | ✓ `simulator-aws/apigateway_extras.go:205::handleAPIGWUpdateAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/exports/{exportType}` | ✓ `simulator-aws/apigateway_extras.go:207::handleAPIGWGetExport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/sdks/{sdkType}` | ✓ `simulator-aws/apigateway_extras.go:208::handleAPIGWGetSdk` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes` | ✓ `simulator-aws/apigateway_extras.go:210::handleAPIGWListSdkTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes/{id}` | ✓ `simulator-aws/apigateway_extras.go:211::handleAPIGWGetSdkType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/usage` | ✓ `simulator-aws/apigateway_extras.go:213::handleAPIGWGetUsage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_extras.go:214::handleAPIGWGetTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
