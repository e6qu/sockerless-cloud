# Sim surface — gcp-compute_reservation_verbs

Surface registered in `simulator-gcp/compute_reservation_verbs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}` | ○ `simulator-gcp/compute_reservation_verbs.go:103::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/performMaintenance` | ○ `simulator-gcp/compute_reservation_verbs.go:117::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/getIamPolicy` | ✓ `simulator-gcp/compute_reservation_verbs.go:134::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/setIamPolicy` | ✓ `simulator-gcp/compute_reservation_verbs.go:137::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/testIamPermissions` | ✓ `simulator-gcp/compute_reservation_verbs.go:140::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks` | ○ `simulator-gcp/compute_reservation_verbs.go:161::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks/{subBlock}` | ○ `simulator-gcp/compute_reservation_verbs.go:188::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks/{subBlock}/getIamPolicy` | ✓ `simulator-gcp/compute_reservation_verbs.go:208::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks/{subBlock}/setIamPolicy` | ✓ `simulator-gcp/compute_reservation_verbs.go:211::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks/{subBlock}/testIamPermissions` | ✓ `simulator-gcp/compute_reservation_verbs.go:214::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks/{subBlock}/reservationSlots` | ○ `simulator-gcp/compute_reservation_verbs.go:245::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks/{block}/reservationSubBlocks/{subBlock}/reservationSlots/{slot}` | ○ `simulator-gcp/compute_reservation_verbs.go:258::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/resize` | ✓ `simulator-gcp/compute_reservation_verbs.go:48::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/performMaintenance` | ✓ `simulator-gcp/compute_reservation_verbs.go:73::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /compute/v1/projects/{project}/zones/{zone}/reservations/{name}/reservationBlocks` | ○ `simulator-gcp/compute_reservation_verbs.go:90::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
