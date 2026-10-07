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
| `GET /v1.0/me` | ✓ `simulator-azure/entra.go:334::handleGraphMe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/me/memberOf` | ✓ `simulator-azure/entra.go:335::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:336::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/me/memberOf` | ✓ `simulator-azure/entra.go:337::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/me/transitiveMemberOf` | ✓ `simulator-azure/entra.go:338::handleGraphMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups` | ✓ `simulator-azure/entra.go:349::handleGraphCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups` | ✓ `simulator-azure/entra.go:350::handleGraphCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups` | ✓ `simulator-azure/entra.go:351::handleGraphListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups` | ✓ `simulator-azure/entra.go:352::handleGraphListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:353::handleGraphGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:354::handleGraphGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:355::handleGraphUpdateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:356::handleGraphUpdateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}` | ✓ `simulator-azure/entra.go:357::handleGraphDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}` | ✓ `simulator-azure/entra.go:358::handleGraphDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:360::handleGraphListGroupMembers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/members` | ✓ `simulator-azure/entra.go:361::handleGraphListGroupMembers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:362::handleGraphAddGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups/{groupId}/members/$ref` | ✓ `simulator-azure/entra.go:363::handleGraphAddGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/members/{memberId}/$ref` | ✓ `simulator-azure/entra.go:364::handleGraphRemoveGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}/members/{memberId}/$ref` | ✓ `simulator-azure/entra.go:365::handleGraphRemoveGroupMemberRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/owners` | ✓ `simulator-azure/entra.go:367::handleGraphListGroupOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/owners` | ✓ `simulator-azure/entra.go:368::handleGraphListGroupOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/groups/{groupId}/owners/$ref` | ✓ `simulator-azure/entra.go:369::handleGraphAddGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/groups/{groupId}/owners/$ref` | ✓ `simulator-azure/entra.go:370::handleGraphAddGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/groups/{groupId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:371::handleGraphRemoveGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/groups/{groupId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:372::handleGraphRemoveGroupOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/groups/{groupId}/memberOf` | ✓ `simulator-azure/entra.go:374::handleGraphGroupMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/groups/{groupId}/memberOf` | ✓ `simulator-azure/entra.go:375::handleGraphGroupMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/users` | ✓ `simulator-azure/entra.go:622::handleGraphCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/users` | ✓ `simulator-azure/entra.go:623::handleGraphCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users` | ✓ `simulator-azure/entra.go:624::handleGraphListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users` | ✓ `simulator-azure/entra.go:625::handleGraphListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:626::handleGraphGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}` | ✓ `simulator-azure/entra.go:627::handleGraphGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:628::handleGraphUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/users/{userId}` | ✓ `simulator-azure/entra.go:629::handleGraphUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/users/{userId}` | ✓ `simulator-azure/entra.go:630::handleGraphDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/users/{userId}` | ✓ `simulator-azure/entra.go:631::handleGraphDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/users/{userId}/memberOf` | ✓ `simulator-azure/entra.go:632::handleGraphUserMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/users/{userId}/memberOf` | ✓ `simulator-azure/entra.go:633::handleGraphUserMemberOf` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications` | ✓ `simulator-azure/entra.go:764::handleGraphCreateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications` | ✓ `simulator-azure/entra.go:765::handleGraphCreateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications` | ✓ `simulator-azure/entra.go:766::handleGraphListApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications` | ✓ `simulator-azure/entra.go:767::handleGraphListApplications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:768::handleGraphGetApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:769::handleGraphGetApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:770::handleGraphUpdateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:771::handleGraphUpdateApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:772::handleGraphDeleteApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/applications/{appObjectId}` | ✓ `simulator-azure/entra.go:773::handleGraphDeleteApplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:778::handleGraphApplicationAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/addPassword` | ✓ `simulator-azure/entra.go:779::handleGraphApplicationAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:780::handleGraphApplicationRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/removePassword` | ✓ `simulator-azure/entra.go:781::handleGraphApplicationRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/applications/{appObjectId}/owners` | ✓ `simulator-azure/entra.go:783::handleGraphListApplicationOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/applications/{appObjectId}/owners` | ✓ `simulator-azure/entra.go:784::handleGraphListApplicationOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/applications/{appObjectId}/owners/$ref` | ✓ `simulator-azure/entra.go:785::handleGraphAddApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/applications/{appObjectId}/owners/$ref` | ✓ `simulator-azure/entra.go:786::handleGraphAddApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/applications/{appObjectId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:787::handleGraphRemoveApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/applications/{appObjectId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:788::handleGraphRemoveApplicationOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:951::handleGraphCreateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals` | ✓ `simulator-azure/entra.go:952::handleGraphCreateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals` | ✓ `simulator-azure/entra.go:953::handleGraphListServicePrincipals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals` | ✓ `simulator-azure/entra.go:954::handleGraphListServicePrincipals` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:955::handleGraphGetServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:956::handleGraphGetServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:957::handleGraphUpdateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:958::handleGraphUpdateServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:959::handleGraphDeleteServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/servicePrincipals/{spId}` | ✓ `simulator-azure/entra.go:960::handleGraphDeleteServicePrincipal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:962::handleGraphServicePrincipalAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/addPassword` | ✓ `simulator-azure/entra.go:963::handleGraphServicePrincipalAddPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:964::handleGraphServicePrincipalRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/removePassword` | ✓ `simulator-azure/entra.go:965::handleGraphServicePrincipalRemovePassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1.0/servicePrincipals/{spId}/owners` | ✓ `simulator-azure/entra.go:967::handleGraphListServicePrincipalOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /beta/servicePrincipals/{spId}/owners` | ✓ `simulator-azure/entra.go:968::handleGraphListServicePrincipalOwners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1.0/servicePrincipals/{spId}/owners/$ref` | ✓ `simulator-azure/entra.go:969::handleGraphAddServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /beta/servicePrincipals/{spId}/owners/$ref` | ✓ `simulator-azure/entra.go:970::handleGraphAddServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1.0/servicePrincipals/{spId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:971::handleGraphRemoveServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /beta/servicePrincipals/{spId}/owners/{ownerId}/$ref` | ✓ `simulator-azure/entra.go:972::handleGraphRemoveServicePrincipalOwnerRef` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
