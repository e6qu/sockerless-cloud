# Sim surface — aws-apigateway

Surface registered in `simulator-aws/apigateway.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /restapis` | ✓ `simulator-aws/apigateway.go:231::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis` | ✓ `simulator-aws/apigateway.go:232::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:233::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}` | ✓ `simulator-aws/apigateway.go:234::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{parentId}` | ✓ `simulator-aws/apigateway.go:235::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources` | ✓ `simulator-aws/apigateway.go:236::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:237::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway.go:238::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:239::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:240::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway.go:241::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:242::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:243::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway.go:244::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:245::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments` | ✓ `simulator-aws/apigateway.go:246::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:247::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway.go:248::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:249::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages` | ✓ `simulator-aws/apigateway.go:250::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:251::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway.go:252::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:259::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:260::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:261::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:262::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:263::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}` | ✓ `simulator-aws/apigateway.go:264::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /apikeys` | ✓ `simulator-aws/apigateway.go:268::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys` | ✓ `simulator-aws/apigateway.go:269::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:270::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:271::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /apikeys/{apiKey}` | ✓ `simulator-aws/apigateway.go:272::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans` | ✓ `simulator-aws/apigateway.go:276::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans` | ✓ `simulator-aws/apigateway.go:277::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:278::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:279::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}` | ✓ `simulator-aws/apigateway.go:280::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:281::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys` | ✓ `simulator-aws/apigateway.go:282::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:283::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /usageplans/{usagePlanId}/keys/{keyId}` | ✓ `simulator-aws/apigateway.go:284::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:287::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models` | ✓ `simulator-aws/apigateway.go:288::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:289::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway.go:290::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:291::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators` | ✓ `simulator-aws/apigateway.go:292::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway.go:293::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:294::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers` | ✓ `simulator-aws/apigateway.go:295::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:296::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway.go:297::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:103::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}` | ✓ `simulator-aws/apigateway_complete.go:104::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:105::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}` | ✓ `simulator-aws/apigateway_complete.go:106::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration` | ✓ `simulator-aws/apigateway_complete.go:107::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/models/{modelName}` | ✓ `simulator-aws/apigateway_complete.go:108::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_complete.go:109::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/stages/{stageName}` | ✓ `simulator-aws/apigateway_complete.go:110::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/deployments/{deploymentId}` | ✓ `simulator-aws/apigateway_complete.go:111::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}` | ✓ `simulator-aws/apigateway_complete.go:112::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /vpclinks/{vpcLinkId}` | ✓ `simulator-aws/apigateway_complete.go:113::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /usageplans/{usagePlanId}/keys/{keyId}/usage` | ✓ `simulator-aws/apigateway_complete.go:114::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:117::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_complete.go:118::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}` | ✓ `simulator-aws/apigateway_complete.go:127::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/data` | ✓ `simulator-aws/apigateway_complete.go:131::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/stages/{stageName}/cache/authorizers` | ✓ `simulator-aws/apigateway_complete.go:132::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}` | ✓ `simulator-aws/apigateway_complete.go:135::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/authorizers/{authorizerId}` | ✓ `simulator-aws/apigateway_complete.go:136::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:139::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:140::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnameaccessassociations/{domainNameAccessAssociationArn}` | ✓ `simulator-aws/apigateway_complete.go:141::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /rejectdomainnameaccessassociations` | ✓ `simulator-aws/apigateway_complete.go:142::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:193::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings` | ✓ `simulator-aws/apigateway_extras.go:194::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:195::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:196::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /domainnames/{domainName}/basepathmappings/{basePath}` | ✓ `simulator-aws/apigateway_extras.go:197::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:200::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates` | ✓ `simulator-aws/apigateway_extras.go:201::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:202::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:203::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /clientcertificates/{clientCertificateId}` | ✓ `simulator-aws/apigateway_extras.go:204::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:207::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:208::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/documentation/parts` | ✓ `simulator-aws/apigateway_extras.go:209::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:210::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:211::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/parts/{documentationPartId}` | ✓ `simulator-aws/apigateway_extras.go:212::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:215::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions` | ✓ `simulator-aws/apigateway_extras.go:216::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:217::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:218::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/documentation/versions/{documentationVersion}` | ✓ `simulator-aws/apigateway_extras.go:219::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:222::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses` | ✓ `simulator-aws/apigateway_extras.go:223::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:224::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:225::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /restapis/{restApiId}/gatewayresponses/{responseType}` | ✓ `simulator-aws/apigateway_extras.go:226::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/requestvalidators/{requestValidatorId}` | ✓ `simulator-aws/apigateway_extras.go:229::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/models/{modelName}/default_template` | ✓ `simulator-aws/apigateway_extras.go:230::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /account` | ✓ `simulator-aws/apigateway_extras.go:233::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /account` | ✓ `simulator-aws/apigateway_extras.go:234::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/exports/{exportType}` | ✓ `simulator-aws/apigateway_extras.go:237::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /restapis/{restApiId}/stages/{stageName}/sdks/{sdkType}` | ✓ `simulator-aws/apigateway_extras.go:238::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes` | ✓ `simulator-aws/apigateway_extras.go:241::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sdktypes/{id}` | ✓ `simulator-aws/apigateway_extras.go:242::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /usageplans/{usagePlanId}/usage` | ✓ `simulator-aws/apigateway_extras.go:245::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /tags/{resourceArn}` | ✓ `simulator-aws/apigateway_extras.go:246::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
