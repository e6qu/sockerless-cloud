# Sim surface — aws-cloudfront

Surface registered in `simulator-aws/cloudfront.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /2020-05-31/distribution` | ✓ `simulator-aws/cloudfront.go:533::handleCFCreateDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution` | ✓ `simulator-aws/cloudfront.go:534::handleCFListDistributions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{id}` | ✓ `simulator-aws/cloudfront.go:535::handleCFGetDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{id}/config` | ✓ `simulator-aws/cloudfront.go:536::handleCFGetDistributionConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/distribution/{id}/config` | ✓ `simulator-aws/cloudfront.go:537::handleCFUpdateDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/distribution/{id}` | ✓ `simulator-aws/cloudfront.go:538::handleCFDeleteDistribution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/tagging` | ✓ `simulator-aws/cloudfront.go:541::handleCFListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/tagging` | ✓ `simulator-aws/cloudfront.go:542::handleCFTagDispatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/origin-access-control` | ✓ `simulator-aws/cloudfront.go:558::handleCFCreateOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-access-control` | ✓ `simulator-aws/cloudfront.go:559::handleCFListOACs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-access-control/{id}` | ✓ `simulator-aws/cloudfront.go:560::handleCFGetOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-access-control/{id}/config` | ✓ `simulator-aws/cloudfront.go:561::handleCFGetOACConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/origin-access-control/{id}/config` | ✓ `simulator-aws/cloudfront.go:562::handleCFUpdateOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/origin-access-control/{id}` | ✓ `simulator-aws/cloudfront.go:563::handleCFDeleteOAC` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/function` | ✓ `simulator-aws/cloudfront_functions.go:145::handleCFCreateFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/function` | ✓ `simulator-aws/cloudfront_functions.go:146::handleCFListFunctions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/function/{name}/describe` | ✓ `simulator-aws/cloudfront_functions.go:147::handleCFDescribeFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/function/{name}` | ✓ `simulator-aws/cloudfront_functions.go:148::handleCFGetFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/function/{name}` | ✓ `simulator-aws/cloudfront_functions.go:149::handleCFUpdateFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/function/{name}` | ✓ `simulator-aws/cloudfront_functions.go:150::handleCFDeleteFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/function/{name}/publish` | ✓ `simulator-aws/cloudfront_functions.go:151::handleCFPublishFunction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/distribution/{distId}/invalidation` | ✓ `simulator-aws/cloudfront_functions.go:153::handleCFCreateInvalidation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{distId}/invalidation` | ✓ `simulator-aws/cloudfront_functions.go:154::handleCFListInvalidations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/distribution/{distId}/invalidation/{id}` | ✓ `simulator-aws/cloudfront_functions.go:155::handleCFGetInvalidation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/public-key/{id}` | ✓ `simulator-aws/cloudfront_keys.go:100::handleCFGetPublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/public-key/{id}/config` | ✓ `simulator-aws/cloudfront_keys.go:101::handleCFGetPublicKeyConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/public-key/{id}/config` | ✓ `simulator-aws/cloudfront_keys.go:102::handleCFUpdatePublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/public-key/{id}` | ✓ `simulator-aws/cloudfront_keys.go:103::handleCFDeletePublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/key-group` | ✓ `simulator-aws/cloudfront_keys.go:105::handleCFCreateKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/key-group` | ✓ `simulator-aws/cloudfront_keys.go:106::handleCFListKeyGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/key-group/{id}` | ✓ `simulator-aws/cloudfront_keys.go:107::handleCFGetKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/key-group/{id}/config` | ✓ `simulator-aws/cloudfront_keys.go:108::handleCFGetKeyGroupConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/key-group/{id}` | ✓ `simulator-aws/cloudfront_keys.go:109::handleCFUpdateKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/key-group/{id}` | ✓ `simulator-aws/cloudfront_keys.go:110::handleCFDeleteKeyGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/public-key` | ✓ `simulator-aws/cloudfront_keys.go:98::handleCFCreatePublicKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/public-key` | ✓ `simulator-aws/cloudfront_keys.go:99::handleCFListPublicKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/cache-policy` | ✓ `simulator-aws/cloudfront_policies.go:276::handleCFCreateCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/cache-policy` | ✓ `simulator-aws/cloudfront_policies.go:277::handleCFListCachePolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/cache-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:278::handleCFGetCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/cache-policy/{id}/config` | ✓ `simulator-aws/cloudfront_policies.go:279::handleCFGetCachePolicyConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/cache-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:280::handleCFUpdateCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/cache-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:281::handleCFDeleteCachePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/origin-request-policy` | ✓ `simulator-aws/cloudfront_policies.go:283::handleCFCreateORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-request-policy` | ✓ `simulator-aws/cloudfront_policies.go:284::handleCFListORPs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-request-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:285::handleCFGetORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/origin-request-policy/{id}/config` | ✓ `simulator-aws/cloudfront_policies.go:286::handleCFGetORPConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/origin-request-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:287::handleCFUpdateORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/origin-request-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:288::handleCFDeleteORP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2020-05-31/response-headers-policy` | ✓ `simulator-aws/cloudfront_policies.go:290::handleCFCreateRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/response-headers-policy` | ✓ `simulator-aws/cloudfront_policies.go:291::handleCFListRHPs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/response-headers-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:292::handleCFGetRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2020-05-31/response-headers-policy/{id}/config` | ✓ `simulator-aws/cloudfront_policies.go:293::handleCFGetRHPConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2020-05-31/response-headers-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:294::handleCFUpdateRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2020-05-31/response-headers-policy/{id}` | ✓ `simulator-aws/cloudfront_policies.go:295::handleCFDeleteRHP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
