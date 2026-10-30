# Sim surface — gcp-iam

Surface registered in `simulator-gcp/cloudresourcemanager_v3.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /v3:fetchResourceSemantics` | ○ `simulator-gcp/cloudresourcemanager_v3.go:249::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:266::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects:search` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:291::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:306::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/projects` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:331::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/projects/{project}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:366::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/projects/{project}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:375::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/projects/{project}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:400::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/projects/{projectAction}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:424::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders:search` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:476::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:490::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/folders` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:494::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders/{folder}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:515::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/folders/{folder}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:524::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/folders/{folder}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:545::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/folders/{folderAction}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:558::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/organizations:search` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:599::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/organizations/{org}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:615::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/organizations/{orgAction}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:624::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/liens` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:634::crmCreateLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/liens` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:636::crmListLiens` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/liens/{lien}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:638::crmGetLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/liens/{lien}` | ✓ `simulator-gcp/cloudresourcemanager_v3.go:640::crmDeleteLien` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:100::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:104::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/{val}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:130::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/tagValues/{val}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:139::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagValues/{val}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:149::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys/namespaced` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:15::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/{val}/tagHolds` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:161::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues/{val}/tagHolds` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:176::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagValues/{val}/tagHolds/{hold}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:193::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagValues/{valAction}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:203::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagBindings` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:210::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagBindings` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:226::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagBindings/{binding...}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:230::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/effectiveTags` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:236::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/folders/{folder}/capabilities/{capability}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:254::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:26::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/folders/{folder}/capabilities/{capability}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:266::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/locations/{location}/tagBindingCollections/{collection}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:292::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagKeys` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:30::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/locations/{location}/tagBindingCollections/{collection}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:309::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/locations/{location}/effectiveTagBindingCollections/{collection}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:333::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagKeys/{key}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:52::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v3/tagKeys/{key}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:61::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v3/tagKeys/{key}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:71::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v3/tagKeys/{keyAction}` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:82::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v3/tagValues/namespaced` | ✓ `simulator-gcp/cloudresourcemanager_v3_tags.go:89::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/{resource...}` | ✓ `simulator-gcp/iam.go:200::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/iam` | ✓ `simulator-gcp/iam.go:240::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/iam` | ✓ `simulator-gcp/iam.go:257::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/namespaces/{namespace}/managedIdentities/{mi}/workloadSources/{ws}/operations/{op}` | ✓ `simulator-gcp/iam_pools.go:387::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/locations/{location}/workforcePools/{pool}/subjects/{subject}` | ✓ `simulator-gcp/iam_pools.go:468::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/locations/{location}/workforcePools/{pool}/subjects/{subjectAction}` | ✓ `simulator-gcp/iam_pools.go:480::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/locations/{location}/workforcePools/{pool}/subjects/{subject}/operations/{op}` | ✓ `simulator-gcp/iam_pools.go:512::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients` | ✓ `simulator-gcp/iam_pools.go:532::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients/{client}` | ✓ `simulator-gcp/iam_pools.go:549::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/oauthClients` | ✓ `simulator-gcp/iam_pools.go:559::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/oauthClients/{clientAction}` | ✓ `simulator-gcp/iam_pools.go:589::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/oauthClients/{client}` | ✓ `simulator-gcp/iam_pools.go:609::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/oauthClients/{client}` | ✓ `simulator-gcp/iam_pools.go:626::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials` | ✓ `simulator-gcp/iam_pools.go:641::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials/{cred}` | ✓ `simulator-gcp/iam_pools.go:656::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials` | ✓ `simulator-gcp/iam_pools.go:664::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials/{cred}` | ✓ `simulator-gcp/iam_pools.go:689::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/oauthClients/{client}/credentials/{cred}` | ✓ `simulator-gcp/iam_pools.go:704::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/iamPolicies:queryAuditableServices` | ○ `simulator-gcp/iam_roles.go:103::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/roles:queryGrantableRoles` | ✓ `simulator-gcp/iam_roles.go:130::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/roles` | ○ `simulator-gcp/iam_roles.go:189::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/allowedLocations` | ○ `simulator-gcp/iam_roles.go:19::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/roles/{role...}` | ○ `simulator-gcp/iam_roles.go:200::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{parent}/roles` | ✓ `simulator-gcp/iam_roles.go:227::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{parent}/roles` | ✓ `simulator-gcp/iam_roles.go:227::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{parent}/roles/{role}` | ✓ `simulator-gcp/iam_roles.go:248::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{parent}/roles/{role}` | ✓ `simulator-gcp/iam_roles.go:248::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{parent}/roles` | ✓ `simulator-gcp/iam_roles.go:259::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{parent}/roles` | ✓ `simulator-gcp/iam_roles.go:259::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/allowedLocations` | ○ `simulator-gcp/iam_roles.go:27::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{parent}/roles/{roleAction}` | ✓ `simulator-gcp/iam_roles.go:300::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{parent}/roles/{roleAction}` | ✓ `simulator-gcp/iam_roles.go:300::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/organizations/{parent}/roles/{role}` | ✓ `simulator-gcp/iam_roles.go:320::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{parent}/roles/{role}` | ✓ `simulator-gcp/iam_roles.go:320::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/locations/{location}/workforcePools/{pool}/allowedLocations` | ○ `simulator-gcp/iam_roles.go:35::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/organizations/{parent}/roles/{role}` | ✓ `simulator-gcp/iam_roles.go:362::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{parent}/roles/{role}` | ✓ `simulator-gcp/iam_roles.go:362::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/permissions:queryTestablePermissions` | ○ `simulator-gcp/iam_roles.go:49::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/iamPolicies:lintPolicy` | ○ `simulator-gcp/iam_roles.go:77::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys/{keyAction}` | ✓ `simulator-gcp/iam_service_account_keys.go:183::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}` | ✓ `simulator-gcp/iam_service_account_keys.go:214::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}/keys` | ✓ `simulator-gcp/iam_service_account_keys.go:259::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}` | ✓ `simulator-gcp/iam_service_account_keys.go:279::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys` | ✓ `simulator-gcp/iam_service_account_keys.go:28::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{email}/keys:upload` | ✓ `simulator-gcp/iam_service_account_keys.go:86::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam_service_accounts.go:125::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam_service_accounts.go:152::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts/{emailAction}` | ✓ `simulator-gcp/iam_service_accounts.go:202::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/serviceAccounts` | ✓ `simulator-gcp/iam_service_accounts.go:22::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts` | ✓ `simulator-gcp/iam_service_accounts.go:363::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam_service_accounts.go:69::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/serviceAccounts/{email}` | ✓ `simulator-gcp/iam_service_accounts.go:84::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /.well-known/openid-configuration` | ○ `simulator-gcp/token_signing.go:240::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /.well-known/jwks.json` | ○ `simulator-gcp/token_signing.go:246::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
PR #392 added service account key CRUD (`POST/GET(single)/GET(list)/DELETE /v1/projects/{p}/serviceAccounts/{email}/keys`). The create handler generates a real RSA-2048 private key and returns it base64-encoded as a JSON credential file in `privateKeyData` (absent on get/list, matching real GCP spec). gcloud uses `project="-"` as a wildcard; the handler resolves the project by parsing the email (`{acct}@{project}.iam.gserviceaccount.com`). Tested by `simulator-gcp/sdk-tests/iam_test.go` (`TestIAM_ServiceAccountKeysCRUD`) and `simulator-gcp/cli-tests/client_surface_audit_test.go` (`TestCLI_IAMServiceAccountKeys`). Terraform does not create SA keys directly; `google_service_account_key` is not in the test stack.

`POST /v1/projects/{project}/serviceAccounts` (create) rejects a duplicate accountId within the same project with 409 `ALREADY_EXISTS` — `"Service account {accountId} already exists within project projects/{project}."` — matching real Cloud IAM instead of silently overwriting the existing account. Tested by `simulator-gcp/sdk-tests/iam_test.go` (`TestIAM_CreateServiceAccountDuplicateConflict`) and `simulator-gcp/cli-tests/iam_test.go` (`TestIAMServiceAccountCreateDuplicateCLI`). No dedicated terraform case: `google_service_account` create is idempotent by Terraform's own state tracking — a normal `apply` never issues a second raw create for a resource already in state, so the duplicate-create conflict has no terraform-provider code path to exercise.
<!-- HAND-WRITTEN END -->
