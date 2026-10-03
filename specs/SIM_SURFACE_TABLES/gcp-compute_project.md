# Sim surface — gcp-compute_project

Surface registered in `simulator-gcp/compute_project.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /compute/v1/projects/{project}/setDefaultNetworkTier` | ✓ `simulator-gcp/compute_project.go:106::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/setCloudArmorTier` | ✓ `simulator-gcp/compute_project.go:108::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/setCommonInstanceMetadata` | ✓ `simulator-gcp/compute_project.go:114::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/setUsageExportBucket` | ✓ `simulator-gcp/compute_project.go:128::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/enableXpnHost` | ✓ `simulator-gcp/compute_project.go:161::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/disableXpnHost` | ✓ `simulator-gcp/compute_project.go:165::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/getXpnHost` | ✓ `simulator-gcp/compute_project.go:175::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/enableXpnResource` | ✓ `simulator-gcp/compute_project.go:240::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/disableXpnResource` | ✓ `simulator-gcp/compute_project.go:243::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/getXpnResources` | ✓ `simulator-gcp/compute_project.go:247::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/listXpnHosts` | ✓ `simulator-gcp/compute_project.go:262::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/moveDisk` | ✓ `simulator-gcp/compute_project.go:294::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/moveInstance` | ✓ `simulator-gcp/compute_project.go:297::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}` | ✓ `simulator-gcp/compute_project.go:81::inProject` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
