# Sim surface — aws-s3control_access_grants

Surface registered in `simulator-aws/s3control_access_grants.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v20180820/accessgrantsinstance` | ✓ `simulator-aws/s3control_access_grants.go:100::handleS3CreateAccessGrantsInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance` | ✓ `simulator-aws/s3control_access_grants.go:101::handleS3GetAccessGrantsInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/accessgrantsinstance` | ✓ `simulator-aws/s3control_access_grants.go:102::handleS3DeleteAccessGrantsInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstances` | ✓ `simulator-aws/s3control_access_grants.go:103::handleS3ListAccessGrantsInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/prefix` | ✓ `simulator-aws/s3control_access_grants.go:104::handleS3GetAccessGrantsInstanceForPrefix` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v20180820/accessgrantsinstance/identitycenter` | ✓ `simulator-aws/s3control_access_grants.go:106::handleS3AssociateAccessGrantsIdentityCenter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/identitycenter` | ✓ `simulator-aws/s3control_access_grants.go:107::handleS3DissociateAccessGrantsIdentityCenter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v20180820/accessgrantsinstance/resourcepolicy` | ✓ `simulator-aws/s3control_access_grants.go:109::handleS3PutAccessGrantsInstanceResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/resourcepolicy` | ✓ `simulator-aws/s3control_access_grants.go:110::handleS3GetAccessGrantsInstanceResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/resourcepolicy` | ✓ `simulator-aws/s3control_access_grants.go:111::handleS3DeleteAccessGrantsInstanceResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v20180820/accessgrantsinstance/location` | ✓ `simulator-aws/s3control_access_grants.go:113::handleS3CreateAccessGrantsLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/location/{locationId}` | ✓ `simulator-aws/s3control_access_grants.go:114::handleS3GetAccessGrantsLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v20180820/accessgrantsinstance/location/{locationId}` | ✓ `simulator-aws/s3control_access_grants.go:115::handleS3UpdateAccessGrantsLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/location/{locationId}` | ✓ `simulator-aws/s3control_access_grants.go:116::handleS3DeleteAccessGrantsLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/locations` | ✓ `simulator-aws/s3control_access_grants.go:117::handleS3ListAccessGrantsLocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v20180820/accessgrantsinstance/grant` | ✓ `simulator-aws/s3control_access_grants.go:119::handleS3CreateAccessGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/grant/{grantId}` | ✓ `simulator-aws/s3control_access_grants.go:120::handleS3GetAccessGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/grant/{grantId}` | ✓ `simulator-aws/s3control_access_grants.go:121::handleS3DeleteAccessGrant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/grants` | ✓ `simulator-aws/s3control_access_grants.go:122::handleS3ListAccessGrants` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/caller/grants` | ✓ `simulator-aws/s3control_access_grants.go:123::handleS3ListCallerAccessGrants` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/accessgrantsinstance/dataaccess` | ✓ `simulator-aws/s3control_access_grants.go:124::handleS3GetDataAccess` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
