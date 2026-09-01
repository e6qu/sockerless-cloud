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
| `POST /v20180820/accessgrantsinstance/location` | ✓ `simulator-aws/s3control_access_grants.go:101::handleS3CreateAccessGrantsLocation` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/location/{locationId}` | ✓ `simulator-aws/s3control_access_grants.go:102::handleS3GetAccessGrantsLocation` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PUT /v20180820/accessgrantsinstance/location/{locationId}` | ✓ `simulator-aws/s3control_access_grants.go:103::handleS3UpdateAccessGrantsLocation` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/location/{locationId}` | ✓ `simulator-aws/s3control_access_grants.go:104::handleS3DeleteAccessGrantsLocation` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/locations` | ✓ `simulator-aws/s3control_access_grants.go:105::handleS3ListAccessGrantsLocations` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `POST /v20180820/accessgrantsinstance/grant` | ✓ `simulator-aws/s3control_access_grants.go:107::handleS3CreateAccessGrant` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/grant/{grantId}` | ✓ `simulator-aws/s3control_access_grants.go:108::handleS3GetAccessGrant` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/grant/{grantId}` | ✓ `simulator-aws/s3control_access_grants.go:109::handleS3DeleteAccessGrant` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/grants` | ✓ `simulator-aws/s3control_access_grants.go:110::handleS3ListAccessGrants` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/caller/grants` | ✓ `simulator-aws/s3control_access_grants.go:111::handleS3ListCallerAccessGrants` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/dataaccess` | ✓ `simulator-aws/s3control_access_grants.go:112::handleS3GetDataAccess` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `POST /v20180820/accessgrantsinstance` | ✓ `simulator-aws/s3control_access_grants.go:88::handleS3CreateAccessGrantsInstance` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance` | ✓ `simulator-aws/s3control_access_grants.go:89::handleS3GetAccessGrantsInstance` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accessgrantsinstance` | ✓ `simulator-aws/s3control_access_grants.go:90::handleS3DeleteAccessGrantsInstance` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstances` | ✓ `simulator-aws/s3control_access_grants.go:91::handleS3ListAccessGrantsInstances` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/prefix` | ✓ `simulator-aws/s3control_access_grants.go:92::handleS3GetAccessGrantsInstanceForPrefix` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `POST /v20180820/accessgrantsinstance/identitycenter` | ✓ `simulator-aws/s3control_access_grants.go:94::handleS3AssociateAccessGrantsIdentityCenter` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/identitycenter` | ✓ `simulator-aws/s3control_access_grants.go:95::handleS3DissociateAccessGrantsIdentityCenter` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PUT /v20180820/accessgrantsinstance/resourcepolicy` | ✓ `simulator-aws/s3control_access_grants.go:97::handleS3PutAccessGrantsInstanceResourcePolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accessgrantsinstance/resourcepolicy` | ✓ `simulator-aws/s3control_access_grants.go:98::handleS3GetAccessGrantsInstanceResourcePolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accessgrantsinstance/resourcepolicy` | ✓ `simulator-aws/s3control_access_grants.go:99::handleS3DeleteAccessGrantsInstanceResourcePolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
