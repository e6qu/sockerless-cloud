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
| `GET /v1.0/me/memberOf` | ✓ `simulator-azure/entra.go:320::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:321::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/me/memberOf` | ✓ `simulator-azure/entra.go:322::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:323::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups` | ✓ `simulator-azure/entra.go:336::handleGraphCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups` | ✓ `simulator-azure/entra.go:337::handleGraphCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups` | ✓ `simulator-azure/entra.go:338::handleGraphListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups` | ✓ `simulator-azure/entra.go:339::handleGraphListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:340::handleGraphGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:341::handleGraphGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:342::handleGraphUpdateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:343::handleGraphUpdateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:344::handleGraphDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:345::handleGraphDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:347::handleGraphListGroupMembers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:348::handleGraphListGroupMembers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:349::handleGraphAddGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:350::handleGraphAddGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/members/{memberId}/$ref` | ✓ `simulator-azure/entra.go:351::handleGraphRemoveGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}/members/{memberId}/$ref` | ✓ `simulator-azure/entra.go:352::handleGraphRemoveGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/owners` | ✓ `simulator-azure/entra.go:354::handleGraphListGroupOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/owners` | ✓ `simulator-azure/entra.go:355::handleGraphListGroupOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/owners/$ref` | ✓ `simulator-azure/entra.go:356::handleGraphAddGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups/{groupId}/owners/$ref` | ✓ `simulator-azure/entra.go:357::handleGraphAddGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:358::handleGraphRemoveGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:359::handleGraphRemoveGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/memberOf` | ✓ `simulator-azure/entra.go:361::handleGraphGroupMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/memberOf` | ✓ `simulator-azure/entra.go:362::handleGraphGroupMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/users` | ✓ `simulator-azure/entra.go:611::handleGraphCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/users` | ✓ `simulator-azure/entra.go:612::handleGraphCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users` | ✓ `simulator-azure/entra.go:613::handleGraphListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users` | ✓ `simulator-azure/entra.go:614::handleGraphListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:615::handleGraphGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}` | ✓ `simulator-azure/entra.go:616::handleGraphGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:617::handleGraphUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/users/{userId}` | ✓ `simulator-azure/entra.go:618::handleGraphUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:619::handleGraphDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/users/{userId}` | ✓ `simulator-azure/entra.go:620::handleGraphDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}/memberOf` | ✓ `simulator-azure/entra.go:621::handleGraphUserMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}/memberOf` | ✓ `simulator-azure/entra.go:622::handleGraphUserMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications` | ✓ `simulator-azure/entra.go:755::handleGraphCreateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications` | ✓ `simulator-azure/entra.go:756::handleGraphCreateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications` | ✓ `simulator-azure/entra.go:757::handleGraphListApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications` | ✓ `simulator-azure/entra.go:758::handleGraphListApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:759::handleGraphGetApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:760::handleGraphGetApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:761::handleGraphUpdateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:762::handleGraphUpdateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:763::handleGraphDeleteApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:764::handleGraphDeleteApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:769::handleGraphApplicationAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:770::handleGraphApplicationAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:771::handleGraphApplicationRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:772::handleGraphApplicationRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}/owners` | ✓ `simulator-azure/entra.go:774::handleGraphListApplicationOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications/{appObjectId}/owners` | ✓ `simulator-azure/entra.go:775::handleGraphListApplicationOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/owners/$ref` | ✓ `simulator-azure/entra.go:776::handleGraphAddApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/owners/$ref` | ✓ `simulator-azure/entra.go:777::handleGraphAddApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:778::handleGraphRemoveApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/applications/{appObjectId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:779::handleGraphRemoveApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:944::handleGraphCreateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals` | ✓ `simulator-azure/entra.go:945::handleGraphCreateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:946::handleGraphListServicePrincipals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals` | ✓ `simulator-azure/entra.go:947::handleGraphListServicePrincipals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:948::handleGraphGetServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:949::handleGraphGetServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:950::handleGraphUpdateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:951::handleGraphUpdateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:952::handleGraphDeleteServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:953::handleGraphDeleteServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:955::handleGraphServicePrincipalAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:956::handleGraphServicePrincipalAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:957::handleGraphServicePrincipalRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:958::handleGraphServicePrincipalRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}/owners` | ✓ `simulator-azure/entra.go:960::handleGraphListServicePrincipalOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals/{spId}/owners` | ✓ `simulator-azure/entra.go:961::handleGraphListServicePrincipalOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/owners/$ref` | ✓ `simulator-azure/entra.go:962::handleGraphAddServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/owners/$ref` | ✓ `simulator-azure/entra.go:963::handleGraphAddServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:964::handleGraphRemoveServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/servicePrincipals/{spId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:965::handleGraphRemoveServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/directoryObjects/{objectId}` | ✓ `simulator-azure/entra_directory.go:462::handleGraphGetDirectoryObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/directoryObjects/{objectId}` | ✓ `simulator-azure/entra_directory.go:463::handleGraphGetDirectoryObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}/manager` | ✓ `simulator-azure/entra_directory.go:465::handleGraphGetManager` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}/manager` | ✓ `simulator-azure/entra_directory.go:466::handleGraphGetManager` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1.0/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:467::handleGraphSetManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /beta/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:468::handleGraphSetManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:469::handleGraphRemoveManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:470::handleGraphRemoveManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
The Entra surface is entirely standard Microsoft Graph: user/group/application/service-principal provisioning, membership, credential minting, and `/me` reads resolved from the bearer token's oid claim. There are no sockerless-invented seed routes and no process-global "active user": grants that carry no `login_hint` mint tokens for the directory's fixed built-in identity, and tests bind grants to specific users via `login_hint` (authorization code) or `username` (ROPC). App-registration client secrets minted via `POST /v1.0/applications/{appObjectId}/addPassword` are validated by the v2.0 `client_credentials` grant (SHA-256 stored verifier), which issues app-only tokens for the application's service principal. SDK tests: `simulator-azure/sdk-tests/entra_test.go`. CLI tests: `simulator-azure/cli-tests/entra_test.go`.
<!-- HAND-WRITTEN END -->
