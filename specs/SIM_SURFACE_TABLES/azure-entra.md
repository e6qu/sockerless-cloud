# Sim surface — azure-entra

Surface registered in `simulator-azure/entra.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1.0/groups` | ✓ `simulator-azure/entra.go:256::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:284::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:294::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:304::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:325::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/members/{userId}/$ref` | ✓ `simulator-azure/entra.go:352::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/users` | ✓ `simulator-azure/entra.go:364::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:396::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PATCH /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:408::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:436::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/me/memberOf` | ✓ `simulator-azure/entra.go:454::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:455::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/applications` | ✓ `simulator-azure/entra.go:463::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/applications` | ✓ `simulator-azure/entra.go:486::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:498::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PATCH /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:507::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:535::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:546::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:562::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:591::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:624::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:644::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PATCH /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:653::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:675::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:683::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:701::func` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
The Entra surface is entirely standard Microsoft Graph: user/group/application/service-principal provisioning, membership, credential minting, and `/me` reads resolved from the bearer token's oid claim. There are no sockerless-invented seed routes and no process-global "active user": grants that carry no `login_hint` mint tokens for the directory's fixed built-in identity, and tests bind grants to specific users via `login_hint` (authorization code) or `username` (ROPC). App-registration client secrets minted via `POST /v1.0/applications/{appObjectId}/addPassword` are validated by the v2.0 `client_credentials` grant (SHA-256 stored verifier), which issues app-only tokens for the application's service principal. SDK tests: `simulator-azure/sdk-tests/entra_test.go`. CLI tests: `simulator-azure/cli-tests/entra_test.go`.
<!-- HAND-WRITTEN END -->
