# Sim surface — gcp-compute_catalogs

Surface registered in `simulator-gcp/compute_catalogs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /compute/v1/projects/{project}/global/interconnectLocations` | ? `simulator-gcp/compute_catalogs.go:38::locations` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/interconnectLocations/{interconnectLocation}` | ? `simulator-gcp/compute_catalogs.go:39::locations` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/interconnectRemoteLocations` | ? `simulator-gcp/compute_catalogs.go:41::remote` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/interconnectRemoteLocations/{interconnectRemoteLocation}` | ? `simulator-gcp/compute_catalogs.go:42::remote` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/reliabilityRisks` | ? `simulator-gcp/compute_catalogs.go:46::risks` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/reliabilityRisks/{reliabilityRisk}` | ? `simulator-gcp/compute_catalogs.go:47::risks` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/licenseCodes/{licenseCode}` | ? `simulator-gcp/compute_catalogs.go:52::licences` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/licenseCodes/{resource}/getIamPolicy` | ? `simulator-gcp/compute_catalogs.go:53::licences` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/licenseCodes/{resource}/setIamPolicy` | ? `simulator-gcp/compute_catalogs.go:54::licences` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/global/licenseCodes/{resource}/testIamPermissions` | ? `simulator-gcp/compute_catalogs.go:55::licences` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/previewFeatures` | ? `simulator-gcp/compute_catalogs.go:60::previews` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/previewFeatures/{previewFeature}` | ? `simulator-gcp/compute_catalogs.go:61::previews` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PATCH /compute/v1/projects/{project}/global/previewFeatures/{previewFeature}` | ? `simulator-gcp/compute_catalogs.go:62::previews` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/interconnects/{interconnect}/getDiagnostics` | ? `simulator-gcp/compute_catalogs.go:67::catalog` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/global/interconnects/{interconnect}/getMacsecConfig` | ? `simulator-gcp/compute_catalogs.go:69::catalog` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
