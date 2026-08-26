# Sim surface — gcp-cloudkms

Surface registered in `simulator-gcp/cloudkms.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects/{project}/locations/{location}/keyRings` | ✓ `simulator-gcp/cloudkms.go:285::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings` | ✓ `simulator-gcp/cloudkms.go:304::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}` | ✓ `simulator-gcp/cloudkms.go:328::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRingAction}` | ✓ `simulator-gcp/cloudkms.go:349::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys` | ✓ `simulator-gcp/cloudkms.go:369::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys` | ✓ `simulator-gcp/cloudkms.go:445::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}` | ✓ `simulator-gcp/cloudkms.go:469::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}` | ✓ `simulator-gcp/cloudkms.go:490::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKeyAction}` | ✓ `simulator-gcp/cloudkms.go:518::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions` | ✓ `simulator-gcp/cloudkms.go:545::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}` | ✓ `simulator-gcp/cloudkms.go:571::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions` | ✓ `simulator-gcp/cloudkms.go:596::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions:importTrustedKeyWrappedCryptoKeyVersion` | ✓ `simulator-gcp/cloudkms.go:631::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}` | ✓ `simulator-gcp/cloudkms.go:638::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersionAction}` | ✓ `simulator-gcp/cloudkms.go:667::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}` | ✓ `simulator-gcp/cloudkms.go:745::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}` | ✓ `simulator-gcp/cloudkms.go:760::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}/publicKey` | ✓ `simulator-gcp/cloudkms.go:777::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/{cryptoKeyVersionsCollectionAction}` | ✓ `simulator-gcp/cloudkms.go:786::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{locationAction}` | ✓ `simulator-gcp/cloudkms.go:810::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/{locationGetAction}` | ✓ `simulator-gcp/cloudkms.go:825::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs` | ✓ `simulator-gcp/cloudkms.go:838::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs` | ✓ `simulator-gcp/cloudkms.go:907::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs/{importJobAction}` | ✓ `simulator-gcp/cloudkms.go:929::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs/{importJobAction}` | ✓ `simulator-gcp/cloudkms.go:950::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/ekmConnections` | ✓ `simulator-gcp/cloudkms.go:973::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/ekmConnections` | ✓ `simulator-gcp/cloudkms.go:1002::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnectionAction}` | ✓ `simulator-gcp/cloudkms.go:1024::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnectionAction}` | ✓ `simulator-gcp/cloudkms.go:1054::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnection}` | ✓ `simulator-gcp/cloudkms.go:1072::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/ekmConfig` | ✓ `simulator-gcp/cloudkms.go:1100::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/ekmConfig` | ✓ `simulator-gcp/cloudkms.go:1110::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/ekmConfig/{ekmConfigAction}` | ✓ `simulator-gcp/cloudkms.go:1134::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/keyHandles` | ✓ `simulator-gcp/cloudkms.go:1156::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyHandles` | ✓ `simulator-gcp/cloudkms.go:1184::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/keyHandles/{keyHandle}` | ✓ `simulator-gcp/cloudkms.go:1204::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/autokeyConfig` | ✓ `simulator-gcp/cloudkms.go:1264::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/autokeyConfig` | ✓ `simulator-gcp/cloudkms.go:1267::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/folders/{folder}/autokeyConfig` | ✓ `simulator-gcp/cloudkms.go:1270::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/folders/{folder}/autokeyConfig` | ✓ `simulator-gcp/cloudkms.go:1273::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/folders/{folderAction}` | ✓ `simulator-gcp/cloudkms.go:1276::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/kajPolicyConfig` | ✓ `simulator-gcp/cloudkms.go:1286::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/kajPolicyConfig` | ✓ `simulator-gcp/cloudkms.go:1289::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/folders/{folder}/kajPolicyConfig` | ✓ `simulator-gcp/cloudkms.go:1292::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/folders/{folder}/kajPolicyConfig` | ✓ `simulator-gcp/cloudkms.go:1295::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{organization}/kajPolicyConfig` | ✓ `simulator-gcp/cloudkms.go:1298::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/organizations/{organization}/kajPolicyConfig` | ✓ `simulator-gcp/cloudkms.go:1301::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/retiredResources` | ✓ `simulator-gcp/cloudkms.go:1500::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/retiredResources/{retiredResource}` | ✓ `simulator-gcp/cloudkms.go:1526::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
