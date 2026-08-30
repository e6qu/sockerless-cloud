# Sim surface — gcp-iam

Surface registered in `simulator-gcp/iam.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/projects/{project}/serviceAccounts` | ✓ `simulator-gcp/iam.go:192::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:239::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:258::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:303::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:333::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys` | ✓ `simulator-gcp/iam.go:353::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys:upload` | ✓ `simulator-gcp/iam.go:415::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys/{keyAction}` | ✓ `simulator-gcp/iam.go:516::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}` | ✓ `simulator-gcp/iam.go:551::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/keys` | ✓ `simulator-gcp/iam.go:600::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}` | ✓ `simulator-gcp/iam.go:624::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{emailAction}` | ✓ `simulator-gcp/iam.go:664::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts` | ✓ `simulator-gcp/iam.go:833::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/allowedLocations` | ○ `simulator-gcp/iam.go:856::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/allowedLocations` | ○ `simulator-gcp/iam.go:863::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/locations/{location}/workforcePools/{pool}/allowedLocations` | ○ `simulator-gcp/iam.go:870::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/{resource...}` | ✓ `simulator-gcp/iam.go:886::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/permissions:queryTestablePermissions` | ○ `simulator-gcp/iam.go:910::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/iamPolicies:lintPolicy` | ○ `simulator-gcp/iam.go:938::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/iamPolicies:queryAuditableServices` | ○ `simulator-gcp/iam.go:964::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/roles:queryGrantableRoles` | ✓ `simulator-gcp/iam.go:991::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/roles` | ○ `simulator-gcp/iam.go:1089::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/roles/{role...}` | ○ `simulator-gcp/iam.go:1100::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/iam` | ✓ `simulator-gcp/iam.go:1113::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/iam` | ✓ `simulator-gcp/iam.go:1134::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3:fetchResourceSemantics` | ○ `simulator-gcp/iam.go:1362::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}` | ✓ `simulator-gcp/iam.go:1379::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects:search` | ✓ `simulator-gcp/iam.go:1397::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects` | ✓ `simulator-gcp/iam.go:1411::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/projects` | ✓ `simulator-gcp/iam.go:1435::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects/{project}` | ✓ `simulator-gcp/iam.go:1463::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/projects/{project}` | ✓ `simulator-gcp/iam.go:1471::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/projects/{project}` | ✓ `simulator-gcp/iam.go:1495::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/projects/{projectAction}` | ✓ `simulator-gcp/iam.go:1518::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders:search` | ✓ `simulator-gcp/iam.go:1567::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders` | ✓ `simulator-gcp/iam.go:1580::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/folders` | ✓ `simulator-gcp/iam.go:1594::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders/{folder}` | ✓ `simulator-gcp/iam.go:1612::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/folders/{folder}` | ✓ `simulator-gcp/iam.go:1620::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/folders/{folder}` | ✓ `simulator-gcp/iam.go:1640::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/folders/{folderAction}` | ✓ `simulator-gcp/iam.go:1652::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/organizations:search` | ✓ `simulator-gcp/iam.go:1686::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/organizations/{org}` | ✓ `simulator-gcp/iam.go:1701::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/organizations/{orgAction}` | ✓ `simulator-gcp/iam.go:1709::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/liens` | ✓ `simulator-gcp/iam.go:1719::crmCreateLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/liens` | ✓ `simulator-gcp/iam.go:1720::crmListLiens` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/liens/{lien}` | ✓ `simulator-gcp/iam.go:1721::crmGetLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/liens/{lien}` | ✓ `simulator-gcp/iam.go:1722::crmDeleteLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys/namespaced` | ✓ `simulator-gcp/iam.go:1728::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys` | ✓ `simulator-gcp/iam.go:1738::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagKeys` | ✓ `simulator-gcp/iam.go:1752::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys/{key}` | ✓ `simulator-gcp/iam.go:1773::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/tagKeys/{key}` | ✓ `simulator-gcp/iam.go:1781::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagKeys/{key}` | ✓ `simulator-gcp/iam.go:1802::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagKeys/{keyAction}` | ✓ `simulator-gcp/iam.go:1812::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/namespaced` | ✓ `simulator-gcp/iam.go:1819::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues` | ✓ `simulator-gcp/iam.go:1829::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues` | ✓ `simulator-gcp/iam.go:1843::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/{val}` | ✓ `simulator-gcp/iam.go:1862::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/tagValues/{val}` | ✓ `simulator-gcp/iam.go:1870::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagValues/{val}` | ✓ `simulator-gcp/iam.go:1891::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/{val}/tagHolds` | ✓ `simulator-gcp/iam.go:1902::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues/{val}/tagHolds` | ✓ `simulator-gcp/iam.go:1916::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagValues/{val}/tagHolds/{hold}` | ✓ `simulator-gcp/iam.go:1932::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues/{valAction}` | ✓ `simulator-gcp/iam.go:1941::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagBindings` | ✓ `simulator-gcp/iam.go:1948::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagBindings` | ✓ `simulator-gcp/iam.go:1963::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagBindings/{binding...}` | ✓ `simulator-gcp/iam.go:1977::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/effectiveTags` | ○ `simulator-gcp/iam.go:1983::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders/{folder}/capabilities/{capability}` | ○ `simulator-gcp/iam.go:1990::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/folders/{folder}/capabilities/{capability}` | ✓ `simulator-gcp/iam.go:1996::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/locations/{location}/tagBindingCollections/{collection}` | ○ `simulator-gcp/iam.go:2011::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/locations/{location}/tagBindingCollections/{collection}` | ✓ `simulator-gcp/iam.go:2019::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/locations/{location}/effectiveTagBindingCollections/{collection}` | ○ `simulator-gcp/iam.go:2033::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /.well-known/openid-configuration` | ○ `simulator-gcp/token_signing.go:328::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /.well-known/jwks.json` | ○ `simulator-gcp/token_signing.go:339::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
PR #392 added service account key CRUD (`POST/GET(single)/GET(list)/DELETE /v1/projects/{p}/serviceAccounts/{email}/keys`). The create handler generates a real RSA-2048 private key and returns it base64-encoded as a JSON credential file in `privateKeyData` (absent on get/list, matching real GCP spec). gcloud uses `project="-"` as a wildcard; the handler resolves the project by parsing the email (`{acct}@{project}.iam.gserviceaccount.com`). Tested by `simulator-gcp/sdk-tests/iam_test.go` (`TestIAM_ServiceAccountKeysCRUD`) and `simulator-gcp/cli-tests/client_surface_audit_test.go` (`TestCLI_IAMServiceAccountKeys`). Terraform does not create SA keys directly; `google_service_account_key` is not in the test stack.

`POST /v1/projects/{project}/serviceAccounts` (create) rejects a duplicate accountId within the same project with 409 `ALREADY_EXISTS` — `"Service account {accountId} already exists within project projects/{project}."` — matching real Cloud IAM instead of silently overwriting the existing account. Tested by `simulator-gcp/sdk-tests/iam_test.go` (`TestIAM_CreateServiceAccountDuplicateConflict`) and `simulator-gcp/cli-tests/iam_test.go` (`TestIAMServiceAccountCreateDuplicateCLI`). No dedicated terraform case: `google_service_account` create is idempotent by Terraform's own state tracking — a normal `apply` never issues a second raw create for a resource already in state, so the duplicate-create conflict has no terraform-provider code path to exercise.
<!-- HAND-WRITTEN END -->
