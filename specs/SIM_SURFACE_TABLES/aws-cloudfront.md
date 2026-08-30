# Sim surface — aws-cloudfront

Surface registered in `simulator-aws/cloudfront.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /2020-05-31/distribution` | ✓ `simulator-aws/cloudfront.go:567::handleCFCreateDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution` | ✓ `simulator-aws/cloudfront.go:568::handleCFListDistributions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{id}` | ✓ `simulator-aws/cloudfront.go:569::handleCFGetDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{id}/config` | ✓ `simulator-aws/cloudfront.go:570::handleCFGetDistributionConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/distribution/{id}/config` | ✓ `simulator-aws/cloudfront.go:571::handleCFUpdateDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/distribution/{id}` | ✓ `simulator-aws/cloudfront.go:572::handleCFDeleteDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/tagging` | ✓ `simulator-aws/cloudfront.go:575::handleCFListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/tagging` | ✓ `simulator-aws/cloudfront.go:576::handleCFTagDispatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/origin-access-control` | ✓ `simulator-aws/cloudfront.go:604::handleCFCreateOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-access-control` | ✓ `simulator-aws/cloudfront.go:605::handleCFListOACs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-access-control/{id}` | ✓ `simulator-aws/cloudfront.go:606::handleCFGetOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-access-control/{id}/config` | ✓ `simulator-aws/cloudfront.go:607::handleCFGetOACConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/origin-access-control/{id}/config` | ✓ `simulator-aws/cloudfront.go:608::handleCFUpdateOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/origin-access-control/{id}` | ✓ `simulator-aws/cloudfront.go:609::handleCFDeleteOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/function` | ✓ `simulator-aws/cloudfront_functions.go:156::handleCFCreateFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/function` | ✓ `simulator-aws/cloudfront_functions.go:157::handleCFListFunctions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/function/{name}/describe` | ✓ `simulator-aws/cloudfront_functions.go:158::handleCFDescribeFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/function/{name}` | ✓ `simulator-aws/cloudfront_functions.go:159::handleCFGetFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/function/{name}` | ✓ `simulator-aws/cloudfront_functions.go:160::handleCFUpdateFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/function/{name}` | ✓ `simulator-aws/cloudfront_functions.go:161::handleCFDeleteFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/function/{name}/publish` | ✓ `simulator-aws/cloudfront_functions.go:162::handleCFPublishFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/distribution/{distId}/invalidation` | ✓ `simulator-aws/cloudfront_functions.go:165::handleCFCreateInvalidation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{distId}/invalidation` | ✓ `simulator-aws/cloudfront_functions.go:166::handleCFListInvalidations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{distId}/invalidation/{id}` | ✓ `simulator-aws/cloudfront_functions.go:167::handleCFGetInvalidation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/public-key` | ✓ `simulator-aws/cloudfront_keys.go:101::handleCFCreatePublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/public-key` | ✓ `simulator-aws/cloudfront_keys.go:102::handleCFListPublicKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/public-key/{id}` | ✓ `simulator-aws/cloudfront_keys.go:103::handleCFGetPublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/public-key/{id}/config` | ✓ `simulator-aws/cloudfront_keys.go:104::handleCFGetPublicKeyConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/public-key/{id}/config` | ✓ `simulator-aws/cloudfront_keys.go:105::handleCFUpdatePublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/public-key/{id}` | ✓ `simulator-aws/cloudfront_keys.go:106::handleCFDeletePublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/key-group` | ✓ `simulator-aws/cloudfront_keys.go:109::handleCFCreateKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/key-group` | ✓ `simulator-aws/cloudfront_keys.go:110::handleCFListKeyGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/key-group/{id}` | ✓ `simulator-aws/cloudfront_keys.go:111::handleCFGetKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/key-group/{id}/config` | ✓ `simulator-aws/cloudfront_keys.go:112::handleCFGetKeyGroupConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/key-group/{id}` | ✓ `simulator-aws/cloudfront_keys.go:113::handleCFUpdateKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/key-group/{id}` | ✓ `simulator-aws/cloudfront_keys.go:114::handleCFDeleteKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/cache-policy` | ✓ `simulator-aws/cloudfront_policies.go:284::handleCFCreateCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/cache-policy` | ✓ `simulator-aws/cloudfront_policies.go:285::handleCFListCachePolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/cache-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:286::handleCFGetCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/cache-policy/{id}/config` | ✓ `simulator-aws/cloudfront_policies.go:287::handleCFGetCachePolicyConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/cache-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:288::handleCFUpdateCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/cache-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:289::handleCFDeleteCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/origin-request-policy` | ✓ `simulator-aws/cloudfront_policies.go:292::handleCFCreateORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-request-policy` | ✓ `simulator-aws/cloudfront_policies.go:293::handleCFListORPs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-request-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:294::handleCFGetORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-request-policy/{id}/config` | ✓ `simulator-aws/cloudfront_policies.go:295::handleCFGetORPConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/origin-request-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:296::handleCFUpdateORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/origin-request-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:297::handleCFDeleteORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/response-headers-policy` | ✓ `simulator-aws/cloudfront_policies.go:300::handleCFCreateRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/response-headers-policy` | ✓ `simulator-aws/cloudfront_policies.go:301::handleCFListRHPs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/response-headers-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:302::handleCFGetRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/response-headers-policy/{id}/config` | ✓ `simulator-aws/cloudfront_policies.go:303::handleCFGetRHPConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/response-headers-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:304::handleCFUpdateRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/response-headers-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:305::handleCFDeleteRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
