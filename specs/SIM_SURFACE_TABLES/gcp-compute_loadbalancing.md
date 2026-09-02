# Sim surface — gcp-compute_loadbalancing

Surface registered in `simulator-gcp/compute_loadbalancing.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /compute/v1/projects/{project}/global/backendServices` | ✓ `simulator-gcp/compute_loadbalancing.go:103::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/backendServices/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:134::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/backendServices` | ✓ `simulator-gcp/compute_loadbalancing.go:137::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/backendServices/listUsable` | ✓ `simulator-gcp/compute_loadbalancing.go:143::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/backendServices/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:146::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/backendServices/{name}/getHealth` | ✓ `simulator-gcp/compute_loadbalancing.go:183::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/backendServices/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:204::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/urlMaps` | ✓ `simulator-gcp/compute_loadbalancing.go:208::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/urlMaps/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:230::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/urlMaps` | ✓ `simulator-gcp/compute_loadbalancing.go:233::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/urlMaps/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:236::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/targetHttpProxies` | ✓ `simulator-gcp/compute_loadbalancing.go:240::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/targetHttpProxies/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:261::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/targetHttpProxies` | ✓ `simulator-gcp/compute_loadbalancing.go:264::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/targetHttpProxies/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:267::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/forwardingRules` | ✓ `simulator-gcp/compute_loadbalancing.go:271::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/forwardingRules/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:309::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/forwardingRules` | ✓ `simulator-gcp/compute_loadbalancing.go:312::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/forwardingRules/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:315::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action /{path...}` | ✓ `simulator-gcp/compute_loadbalancing.go:429::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/healthChecks` | ✓ `simulator-gcp/compute_loadbalancing.go:50::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/healthChecks/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:93::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/healthChecks` | ✓ `simulator-gcp/compute_loadbalancing.go:96::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /compute/v1/projects/{project}/global/healthChecks/{name}` | ✓ `simulator-gcp/compute_loadbalancing.go:99::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Issue #263 closed the GCP managed load-balancer gap for the global external HTTP load-balancing control-plane chain. The implemented Compute slice covers global health checks, backend services, URL maps, target HTTP proxies, and global forwarding rules with AIP-style global operations. Coverage uses the official Compute Go SDK in `simulator-gcp/sdk-tests/compute_test.go`, `gcloud compute` lifecycle coverage in `simulator-gcp/cli-tests/compute_loadbalancing_test.go`, and Terraform `google_compute_health_check`, `google_compute_backend_service`, `google_compute_url_map`, `google_compute_target_http_proxy`, and `google_compute_global_forwarding_rule` resources in `simulator-gcp/terraform-tests/main.tf`.
<!-- HAND-WRITTEN END -->
