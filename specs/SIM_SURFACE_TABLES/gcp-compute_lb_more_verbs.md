# Sim surface — gcp-compute_lb_more_verbs

Surface registered in `simulator-gcp/compute_lb_more_verbs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /compute/v1/projects/{project}/targetHttpsProxies/{name}/setUrlMap` | ? `simulator-gcp/compute_lb_more_verbs.go:105::unscoped` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/targetHttpsProxies/{name}/setSslCertificates` | ? `simulator-gcp/compute_lb_more_verbs.go:107::unscoped` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/forwardingRules/{name}` | ? `simulator-gcp/compute_lb_more_verbs.go:132::rule` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/targetHttpProxies/{name}` | ? `simulator-gcp/compute_lb_more_verbs.go:134::httpProxy` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/backendServices/{name}/addSignedUrlKey` | ? `simulator-gcp/compute_lb_more_verbs.go:214::true` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/backendServices/{name}/deleteSignedUrlKey` | ? `simulator-gcp/compute_lb_more_verbs.go:216::false` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/backendServices/{name}/getEffectiveSecurityPolicies` | ✓ `simulator-gcp/compute_lb_more_verbs.go:224::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/backendServices/{name}/setSecurityPolicy` | ? `simulator-gcp/compute_lb_more_verbs.go:63::backend` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/backendServices/{name}/setEdgeSecurityPolicy` | ? `simulator-gcp/compute_lb_more_verbs.go:65::backend` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/forwardingRules/{name}/setTarget` | ? `simulator-gcp/compute_lb_more_verbs.go:69::rule` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/forwardingRules/{name}/setLabels` | ? `simulator-gcp/compute_lb_more_verbs.go:71::rule` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/targetHttpProxies/{name}/setUrlMap` | ? `simulator-gcp/compute_lb_more_verbs.go:77::httpProxy` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
