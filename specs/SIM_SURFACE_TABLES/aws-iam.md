# Sim surface — aws-iam

Surface registered in `simulator-aws/iam.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action ListPolicies` | ✓ `simulator-aws/iam.go:100::handleIAMListPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetPolicyVersion` | ✓ `simulator-aws/iam.go:101::handleIAMGetPolicyVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateInstanceProfile` | ✓ `simulator-aws/iam.go:106::handleIAMCreateInstanceProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetInstanceProfile` | ✓ `simulator-aws/iam.go:107::handleIAMGetInstanceProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteInstanceProfile` | ✓ `simulator-aws/iam.go:108::handleIAMDeleteInstanceProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListInstanceProfiles` | ✓ `simulator-aws/iam.go:109::handleIAMListInstanceProfiles` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddRoleToInstanceProfile` | ✓ `simulator-aws/iam.go:110::handleIAMAddRoleToInstanceProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveRoleFromInstanceProfile` | ✓ `simulator-aws/iam.go:111::handleIAMRemoveRoleFromInstanceProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateRole` | ✓ `simulator-aws/iam.go:79::handleIAMCreateRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetRole` | ✓ `simulator-aws/iam.go:80::handleIAMGetRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteRole` | ✓ `simulator-aws/iam.go:81::handleIAMDeleteRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UpdateRole` | ✓ `simulator-aws/iam.go:82::handleIAMUpdateRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TagRole` | ✓ `simulator-aws/iam.go:83::handleIAMTagRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UntagRole` | ✓ `simulator-aws/iam.go:84::handleIAMUntagRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UpdateAssumeRolePolicy` | ✓ `simulator-aws/iam.go:85::handleIAMUpdateAssumeRolePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutRolePolicy` | ✓ `simulator-aws/iam.go:86::handleIAMPutRolePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetRolePolicy` | ✓ `simulator-aws/iam.go:87::handleIAMGetRolePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteRolePolicy` | ✓ `simulator-aws/iam.go:88::handleIAMDeleteRolePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AttachRolePolicy` | ✓ `simulator-aws/iam.go:89::handleIAMAttachRolePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DetachRolePolicy` | ✓ `simulator-aws/iam.go:90::handleIAMDetachRolePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAttachedRolePolicies` | ✓ `simulator-aws/iam.go:91::handleIAMListAttachedRolePolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListRolePolicies` | ✓ `simulator-aws/iam.go:92::handleIAMListRolePolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListInstanceProfilesForRole` | ✓ `simulator-aws/iam.go:93::handleIAMListInstanceProfilesForRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreatePolicy` | ✓ `simulator-aws/iam.go:97::handleIAMCreatePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetPolicy` | ✓ `simulator-aws/iam.go:98::handleIAMGetPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeletePolicy` | ✓ `simulator-aws/iam.go:99::handleIAMDeletePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetAccountProperties` | ✓ `simulator-aws/iam_account_properties.go:35::handleIAMGetAccountProperties` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutAccountProperties` | ✓ `simulator-aws/iam_account_properties.go:36::handleIAMPutAccountProperties` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetRoleTemplateVersion` | ○ `simulator-aws/iam_account_properties.go:37::handleIAMGetRoleTemplateVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AcquireRole` | ○ `simulator-aws/iam_account_properties.go:38::handleIAMAcquireRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutUserPermissionsBoundary` | ✓ `simulator-aws/iam_groups.go:64::handleIAMPutUserBoundary` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUserPermissionsBoundary` | ✓ `simulator-aws/iam_groups.go:65::handleIAMDeleteUserBoundary` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateGroup` | ✓ `simulator-aws/iam_groups.go:67::handleIAMCreateGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetGroup` | ✓ `simulator-aws/iam_groups.go:68::handleIAMGetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteGroup` | ✓ `simulator-aws/iam_groups.go:69::handleIAMDeleteGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListGroups` | ✓ `simulator-aws/iam_groups.go:70::handleIAMListGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddUserToGroup` | ✓ `simulator-aws/iam_groups.go:71::handleIAMAddUserToGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveUserFromGroup` | ✓ `simulator-aws/iam_groups.go:72::handleIAMRemoveUserFromGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListGroupsForUser` | ✓ `simulator-aws/iam_groups.go:73::handleIAMListGroupsForUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutGroupPolicy` | ✓ `simulator-aws/iam_groups.go:74::handleIAMPutGroupPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetGroupPolicy` | ✓ `simulator-aws/iam_groups.go:75::handleIAMGetGroupPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteGroupPolicy` | ✓ `simulator-aws/iam_groups.go:76::handleIAMDeleteGroupPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListGroupPolicies` | ✓ `simulator-aws/iam_groups.go:77::handleIAMListGroupPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AttachGroupPolicy` | ✓ `simulator-aws/iam_groups.go:78::handleIAMAttachGroupPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DetachGroupPolicy` | ✓ `simulator-aws/iam_groups.go:79::handleIAMDetachGroupPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAttachedGroupPolicies` | ✓ `simulator-aws/iam_groups.go:80::handleIAMListAttachedGroupPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListUsers` | ✓ `simulator-aws/iam_groups.go:81::handleIAMListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListPolicyVersions` | ✓ `simulator-aws/iam_lists.go:28::handleIAMListPolicyVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListRoles` | ✓ `simulator-aws/iam_lists.go:29::handleIAMListRoles` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListRoleTags` | ✓ `simulator-aws/iam_lists.go:30::handleIAMListRoleTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListPolicyTags` | ✓ `simulator-aws/iam_lists.go:31::handleIAMListPolicyTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SimulateCustomPolicy` | ○ `simulator-aws/iam_policy_sim.go:780::handleIAMSimulateCustomPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SimulatePrincipalPolicy` | ✓ `simulator-aws/iam_policy_sim.go:781::handleIAMSimulatePrincipalPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServiceLinkedRole` | ✓ `simulator-aws/iam_slr_oidc.go:61::handleIAMCreateServiceLinkedRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServiceLinkedRole` | ✓ `simulator-aws/iam_slr_oidc.go:62::handleIAMDeleteServiceLinkedRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetServiceLinkedRoleDeletionStatus` | ✓ `simulator-aws/iam_slr_oidc.go:63::handleIAMGetSLRDeletionStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:65::handleIAMCreateOIDCProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:66::handleIAMGetOIDCProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TagOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:67::handleIAMTagOIDCProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UntagOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:68::handleIAMUntagOIDCProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UpdateOpenIDConnectProviderThumbprint` | ✓ `simulator-aws/iam_slr_oidc.go:69::handleIAMUpdateOIDCThumbprint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddClientIDToOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:70::handleIAMAddOIDCClientID` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveClientIDFromOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:71::handleIAMRemoveOIDCClientID` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteOpenIDConnectProvider` | ✓ `simulator-aws/iam_slr_oidc.go:72::handleIAMDeleteOIDCProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListOpenIDConnectProviders` | ✓ `simulator-aws/iam_slr_oidc.go:73::handleIAMListOIDCProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUser` | ✓ `simulator-aws/iam_users.go:63::handleIAMCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetUser` | ✓ `simulator-aws/iam_users.go:64::handleIAMGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUser` | ✓ `simulator-aws/iam_users.go:65::handleIAMDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateAccessKey` | ✓ `simulator-aws/iam_users.go:66::handleIAMCreateAccessKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteAccessKey` | ✓ `simulator-aws/iam_users.go:67::handleIAMDeleteAccessKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAccessKeys` | ✓ `simulator-aws/iam_users.go:68::handleIAMListAccessKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutUserPolicy` | ✓ `simulator-aws/iam_users.go:69::handleIAMPutUserPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetUserPolicy` | ✓ `simulator-aws/iam_users.go:70::handleIAMGetUserPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUserPolicy` | ✓ `simulator-aws/iam_users.go:71::handleIAMDeleteUserPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListUserPolicies` | ✓ `simulator-aws/iam_users.go:72::handleIAMListUserPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AttachUserPolicy` | ✓ `simulator-aws/iam_users.go:73::handleIAMAttachUserPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DetachUserPolicy` | ✓ `simulator-aws/iam_users.go:74::handleIAMDetachUserPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAttachedUserPolicies` | ✓ `simulator-aws/iam_users.go:75::handleIAMListAttachedUserPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
