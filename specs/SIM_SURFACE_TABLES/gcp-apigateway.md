# Sim surface — gcp-apigateway

Surface registered in `simulator-gcp/apigateway.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/projects/{project}/locations/global/apis` | ✓ `simulator-gcp/apigateway.go:58::handleGCPAPIGWCreateApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/global/apis/{api}` | ✓ `simulator-gcp/apigateway.go:59::handleGCPAPIGWGetApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/global/apis` | ✓ `simulator-gcp/apigateway.go:60::handleGCPAPIGWListApis` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/global/apis/{api}` | ✓ `simulator-gcp/apigateway.go:61::handleGCPAPIGWPatchApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/global/apis/{api}` | ✓ `simulator-gcp/apigateway.go:62::handleGCPAPIGWDeleteApi` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/global/apis/{api}/configs` | ✓ `simulator-gcp/apigateway.go:65::handleGCPAPIGWCreateConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}` | ✓ `simulator-gcp/apigateway.go:66::handleGCPAPIGWGetConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/global/apis/{api}/configs` | ✓ `simulator-gcp/apigateway.go:67::handleGCPAPIGWListConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}` | ✓ `simulator-gcp/apigateway.go:68::handleGCPAPIGWPatchConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}` | ✓ `simulator-gcp/apigateway.go:69::handleGCPAPIGWDeleteConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/gateways` | ✓ `simulator-gcp/apigateway.go:72::handleGCPAPIGWCreateGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/gateways/{gw}` | ✓ `simulator-gcp/apigateway.go:73::handleGCPAPIGWGetGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/gateways` | ✓ `simulator-gcp/apigateway.go:74::handleGCPAPIGWListGateways` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/gateways/{gw}` | ✓ `simulator-gcp/apigateway.go:75::handleGCPAPIGWPatchGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/gateways/{gw}` | ✓ `simulator-gcp/apigateway.go:76::handleGCPAPIGWDeleteGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/gateways/{gwAction}` | ✓ `simulator-gcp/apigateway.go:84::handleGCPAPIGWIamAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
