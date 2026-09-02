# Sim surface — azure-entra

Surface registered in `simulator-azure/entra.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /v1.0/me/memberOf` | ✓ `simulator-azure/entra.go:320::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:321::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/me/memberOf` | ✓ `simulator-azure/entra.go:322::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:323::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups` | ✓ `simulator-azure/entra.go:334::handleGraphCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups` | ✓ `simulator-azure/entra.go:335::handleGraphCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups` | ✓ `simulator-azure/entra.go:336::handleGraphListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups` | ✓ `simulator-azure/entra.go:337::handleGraphListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:338::handleGraphGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:339::handleGraphGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:340::handleGraphUpdateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:341::handleGraphUpdateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:342::handleGraphDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:343::handleGraphDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:345::handleGraphListGroupMembers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:346::handleGraphListGroupMembers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:347::handleGraphAddGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:348::handleGraphAddGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/members/{memberId}/$ref` | ✓ `simulator-azure/entra.go:349::handleGraphRemoveGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}/members/{memberId}/$ref` | ✓ `simulator-azure/entra.go:350::handleGraphRemoveGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/owners` | ✓ `simulator-azure/entra.go:352::handleGraphListGroupOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/owners` | ✓ `simulator-azure/entra.go:353::handleGraphListGroupOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/owners/$ref` | ✓ `simulator-azure/entra.go:354::handleGraphAddGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups/{groupId}/owners/$ref` | ✓ `simulator-azure/entra.go:355::handleGraphAddGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:356::handleGraphRemoveGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:357::handleGraphRemoveGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/memberOf` | ✓ `simulator-azure/entra.go:359::handleGraphGroupMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/memberOf` | ✓ `simulator-azure/entra.go:360::handleGraphGroupMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/users` | ✓ `simulator-azure/entra.go:607::handleGraphCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/users` | ✓ `simulator-azure/entra.go:608::handleGraphCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users` | ✓ `simulator-azure/entra.go:609::handleGraphListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users` | ✓ `simulator-azure/entra.go:610::handleGraphListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:611::handleGraphGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}` | ✓ `simulator-azure/entra.go:612::handleGraphGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:613::handleGraphUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/users/{userId}` | ✓ `simulator-azure/entra.go:614::handleGraphUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:615::handleGraphDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/users/{userId}` | ✓ `simulator-azure/entra.go:616::handleGraphDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}/memberOf` | ✓ `simulator-azure/entra.go:617::handleGraphUserMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}/memberOf` | ✓ `simulator-azure/entra.go:618::handleGraphUserMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications` | ✓ `simulator-azure/entra.go:749::handleGraphCreateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications` | ✓ `simulator-azure/entra.go:750::handleGraphCreateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications` | ✓ `simulator-azure/entra.go:751::handleGraphListApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications` | ✓ `simulator-azure/entra.go:752::handleGraphListApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:753::handleGraphGetApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:754::handleGraphGetApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:755::handleGraphUpdateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:756::handleGraphUpdateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:757::handleGraphDeleteApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:758::handleGraphDeleteApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:763::handleGraphApplicationAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:764::handleGraphApplicationAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:765::handleGraphApplicationRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:766::handleGraphApplicationRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}/owners` | ✓ `simulator-azure/entra.go:768::handleGraphListApplicationOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications/{appObjectId}/owners` | ✓ `simulator-azure/entra.go:769::handleGraphListApplicationOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/owners/$ref` | ✓ `simulator-azure/entra.go:770::handleGraphAddApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/owners/$ref` | ✓ `simulator-azure/entra.go:771::handleGraphAddApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:772::handleGraphRemoveApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/applications/{appObjectId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:773::handleGraphRemoveApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:936::handleGraphCreateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals` | ✓ `simulator-azure/entra.go:937::handleGraphCreateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:938::handleGraphListServicePrincipals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals` | ✓ `simulator-azure/entra.go:939::handleGraphListServicePrincipals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:940::handleGraphGetServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:941::handleGraphGetServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:942::handleGraphUpdateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:943::handleGraphUpdateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:944::handleGraphDeleteServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:945::handleGraphDeleteServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:947::handleGraphServicePrincipalAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:948::handleGraphServicePrincipalAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:949::handleGraphServicePrincipalRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:950::handleGraphServicePrincipalRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}/owners` | ✓ `simulator-azure/entra.go:952::handleGraphListServicePrincipalOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals/{spId}/owners` | ✓ `simulator-azure/entra.go:953::handleGraphListServicePrincipalOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/owners/$ref` | ✓ `simulator-azure/entra.go:954::handleGraphAddServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/owners/$ref` | ✓ `simulator-azure/entra.go:955::handleGraphAddServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:956::handleGraphRemoveServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/servicePrincipals/{spId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:957::handleGraphRemoveServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/directoryObjects/{objectId}` | ✓ `simulator-azure/entra_directory.go:456::handleGraphGetDirectoryObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/directoryObjects/{objectId}` | ✓ `simulator-azure/entra_directory.go:457::handleGraphGetDirectoryObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}/manager` | ✓ `simulator-azure/entra_directory.go:459::handleGraphGetManager` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}/manager` | ✓ `simulator-azure/entra_directory.go:460::handleGraphGetManager` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1.0/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:461::handleGraphSetManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /beta/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:462::handleGraphSetManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:463::handleGraphRemoveManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/users/{userId}/manager/$ref` | ✓ `simulator-azure/entra_directory.go:464::handleGraphRemoveManagerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
The Entra surface is entirely standard Microsoft Graph: user/group/application/service-principal provisioning, membership, credential minting, and `/me` reads resolved from the bearer token's oid claim. There are no sockerless-invented seed routes and no process-global "active user": grants that carry no `login_hint` mint tokens for the directory's fixed built-in identity, and tests bind grants to specific users via `login_hint` (authorization code) or `username` (ROPC). App-registration client secrets minted via `POST /v1.0/applications/{appObjectId}/addPassword` are validated by the v2.0 `client_credentials` grant (SHA-256 stored verifier), which issues app-only tokens for the application's service principal. SDK tests: `simulator-azure/sdk-tests/entra_test.go`. CLI tests: `simulator-azure/cli-tests/entra_test.go`.
<!-- HAND-WRITTEN END -->
