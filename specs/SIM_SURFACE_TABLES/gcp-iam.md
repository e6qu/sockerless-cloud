# Sim surface — gcp-iam

Surface registered in `simulator-gcp/iam.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/permissions:queryTestablePermissions` | ○ `simulator-gcp/iam.go:1008::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/iamPolicies:lintPolicy` | ○ `simulator-gcp/iam.go:1036::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/iamPolicies:queryAuditableServices` | ○ `simulator-gcp/iam.go:1062::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/roles:queryGrantableRoles` | ✓ `simulator-gcp/iam.go:1089::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/roles` | ○ `simulator-gcp/iam.go:1170::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/roles/{role...}` | ○ `simulator-gcp/iam.go:1181::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/iam` | ✓ `simulator-gcp/iam.go:1194::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/iam` | ✓ `simulator-gcp/iam.go:1211::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3:fetchResourceSemantics` | ○ `simulator-gcp/iam.go:1471::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}` | ✓ `simulator-gcp/iam.go:1488::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects:search` | ✓ `simulator-gcp/iam.go:1513::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects` | ✓ `simulator-gcp/iam.go:1527::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/projects` | ✓ `simulator-gcp/iam.go:1551::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects/{project}` | ✓ `simulator-gcp/iam.go:1584::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/projects/{project}` | ✓ `simulator-gcp/iam.go:1592::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/projects/{project}` | ✓ `simulator-gcp/iam.go:1616::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/projects/{projectAction}` | ✓ `simulator-gcp/iam.go:1639::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders:search` | ✓ `simulator-gcp/iam.go:1688::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders` | ✓ `simulator-gcp/iam.go:1701::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/folders` | ✓ `simulator-gcp/iam.go:1715::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders/{folder}` | ✓ `simulator-gcp/iam.go:1735::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/folders/{folder}` | ✓ `simulator-gcp/iam.go:1743::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/folders/{folder}` | ✓ `simulator-gcp/iam.go:1763::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/folders/{folderAction}` | ✓ `simulator-gcp/iam.go:1775::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/organizations:search` | ✓ `simulator-gcp/iam.go:1816::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/organizations/{org}` | ✓ `simulator-gcp/iam.go:1831::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/organizations/{orgAction}` | ✓ `simulator-gcp/iam.go:1839::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/liens` | ✓ `simulator-gcp/iam.go:1849::crmCreateLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/liens` | ✓ `simulator-gcp/iam.go:1850::crmListLiens` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/liens/{lien}` | ✓ `simulator-gcp/iam.go:1851::crmGetLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/liens/{lien}` | ✓ `simulator-gcp/iam.go:1852::crmDeleteLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys/namespaced` | ✓ `simulator-gcp/iam.go:1858::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys` | ✓ `simulator-gcp/iam.go:1868::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagKeys` | ✓ `simulator-gcp/iam.go:1882::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys/{key}` | ✓ `simulator-gcp/iam.go:1903::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/tagKeys/{key}` | ✓ `simulator-gcp/iam.go:1911::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagKeys/{key}` | ✓ `simulator-gcp/iam.go:1932::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagKeys/{keyAction}` | ✓ `simulator-gcp/iam.go:1942::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/namespaced` | ✓ `simulator-gcp/iam.go:1949::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues` | ✓ `simulator-gcp/iam.go:1959::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues` | ✓ `simulator-gcp/iam.go:1973::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/{val}` | ✓ `simulator-gcp/iam.go:1992::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/tagValues/{val}` | ✓ `simulator-gcp/iam.go:2000::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagValues/{val}` | ✓ `simulator-gcp/iam.go:2021::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/{val}/tagHolds` | ✓ `simulator-gcp/iam.go:2032::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues/{val}/tagHolds` | ✓ `simulator-gcp/iam.go:2046::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagValues/{val}/tagHolds/{hold}` | ✓ `simulator-gcp/iam.go:2062::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues/{valAction}` | ✓ `simulator-gcp/iam.go:2071::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagBindings` | ✓ `simulator-gcp/iam.go:2078::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagBindings` | ✓ `simulator-gcp/iam.go:2093::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagBindings/{binding...}` | ✓ `simulator-gcp/iam.go:2107::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/effectiveTags` | ✓ `simulator-gcp/iam.go:2149::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders/{folder}/capabilities/{capability}` | ✓ `simulator-gcp/iam.go:2162::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/folders/{folder}/capabilities/{capability}` | ✓ `simulator-gcp/iam.go:2173::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/locations/{location}/tagBindingCollections/{collection}` | ✓ `simulator-gcp/iam.go:2198::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/locations/{location}/tagBindingCollections/{collection}` | ✓ `simulator-gcp/iam.go:2214::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/locations/{location}/effectiveTagBindingCollections/{collection}` | ✓ `simulator-gcp/iam.go:2237::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{parent}/roles` | ✓ `simulator-gcp/iam.go:2426::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{parent}/roles` | ✓ `simulator-gcp/iam.go:2426::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{parent}/roles/{role}` | ✓ `simulator-gcp/iam.go:2448::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{parent}/roles/{role}` | ✓ `simulator-gcp/iam.go:2448::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{parent}/roles` | ✓ `simulator-gcp/iam.go:2459::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{parent}/roles` | ✓ `simulator-gcp/iam.go:2459::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{parent}/roles/{roleAction}` | ✓ `simulator-gcp/iam.go:2500::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{parent}/roles/{roleAction}` | ✓ `simulator-gcp/iam.go:2500::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/organizations/{parent}/roles/{role}` | ✓ `simulator-gcp/iam.go:2520::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{parent}/roles/{role}` | ✓ `simulator-gcp/iam.go:2520::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/organizations/{parent}/roles/{role}` | ✓ `simulator-gcp/iam.go:2562::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{parent}/roles/{role}` | ✓ `simulator-gcp/iam.go:2562::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts` | ✓ `simulator-gcp/iam.go:311::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:358::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/namespaces/{namespace}/managedIdentities/{mi}/workloadSources/{ws}/operations/{op}` | ✓ `simulator-gcp/iam.go:3643::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/locations/{location}/workforcePools/{pool}/subjects/{subject}` | ✓ `simulator-gcp/iam.go:3724::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:373::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/locations/{location}/workforcePools/{pool}/subjects/{subjectAction}` | ✓ `simulator-gcp/iam.go:3736::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/locations/{location}/workforcePools/{pool}/subjects/{subject}/operations/{op}` | ✓ `simulator-gcp/iam.go:3768::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients` | ✓ `simulator-gcp/iam.go:3788::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients/{client}` | ✓ `simulator-gcp/iam.go:3805::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/oauthClients` | ✓ `simulator-gcp/iam.go:3815::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/oauthClients/{clientAction}` | ✓ `simulator-gcp/iam.go:3845::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/oauthClients/{client}` | ✓ `simulator-gcp/iam.go:3865::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/oauthClients/{client}` | ✓ `simulator-gcp/iam.go:3882::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials` | ✓ `simulator-gcp/iam.go:3897::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials/{cred}` | ✓ `simulator-gcp/iam.go:3912::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials` | ✓ `simulator-gcp/iam.go:3920::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials/{cred}` | ✓ `simulator-gcp/iam.go:3945::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials/{cred}` | ✓ `simulator-gcp/iam.go:3960::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:414::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam.go:441::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys` | ✓ `simulator-gcp/iam.go:480::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys:upload` | ✓ `simulator-gcp/iam.go:538::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys/{keyAction}` | ✓ `simulator-gcp/iam.go:635::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}` | ✓ `simulator-gcp/iam.go:666::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/keys` | ✓ `simulator-gcp/iam.go:711::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}` | ✓ `simulator-gcp/iam.go:731::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{emailAction}` | ✓ `simulator-gcp/iam.go:767::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts` | ✓ `simulator-gcp/iam.go:928::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/allowedLocations` | ○ `simulator-gcp/iam.go:951::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/allowedLocations` | ○ `simulator-gcp/iam.go:958::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/locations/{location}/workforcePools/{pool}/allowedLocations` | ○ `simulator-gcp/iam.go:965::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/{resource...}` | ✓ `simulator-gcp/iam.go:981::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /.well-known/openid-configuration` | ○ `simulator-gcp/token_signing.go:223::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /.well-known/jwks.json` | ○ `simulator-gcp/token_signing.go:229::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
PR #392 added service account key CRUD (`POST/GET(single)/GET(list)/DELETE /v1/projects/{p}/serviceAccounts/{email}/keys`). The create handler generates a real RSA-2048 private key and returns it base64-encoded as a JSON credential file in `privateKeyData` (absent on get/list, matching real GCP spec). gcloud uses `project="-"` as a wildcard; the handler resolves the project by parsing the email (`{acct}@{project}.iam.gserviceaccount.com`). Tested by `simulator-gcp/sdk-tests/iam_test.go` (`TestIAM_ServiceAccountKeysCRUD`) and `simulator-gcp/cli-tests/client_surface_audit_test.go` (`TestCLI_IAMServiceAccountKeys`). Terraform does not create SA keys directly; `google_service_account_key` is not in the test stack.

`POST /v1/projects/{project}/serviceAccounts` (create) rejects a duplicate accountId within the same project with 409 `ALREADY_EXISTS` — `"Service account {accountId} already exists within project projects/{project}."` — matching real Cloud IAM instead of silently overwriting the existing account. Tested by `simulator-gcp/sdk-tests/iam_test.go` (`TestIAM_CreateServiceAccountDuplicateConflict`) and `simulator-gcp/cli-tests/iam_test.go` (`TestIAMServiceAccountCreateDuplicateCLI`). No dedicated terraform case: `google_service_account` create is idempotent by Terraform's own state tracking — a normal `apply` never issues a second raw create for a resource already in state, so the duplicate-create conflict has no terraform-provider code path to exercise.
<!-- HAND-WRITTEN END -->
