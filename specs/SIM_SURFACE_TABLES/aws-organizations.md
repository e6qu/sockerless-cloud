# Sim surface — aws-organizations

Surface registered in `simulator-aws/organizations.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AWSOrganizationsV20161128.CreateOrganization` | ✓ `simulator-aws/organizations.go:190::handleOrgCreateOrganization` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DeleteOrganization` | ✓ `simulator-aws/organizations.go:191::handleOrgDeleteOrganization` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeOrganization` | ✓ `simulator-aws/organizations.go:192::handleOrgDescribeOrganization` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.EnableAllFeatures` | ✓ `simulator-aws/organizations.go:193::handleOrgEnableAllFeatures` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.CreateAccount` | ✓ `simulator-aws/organizations.go:196::handleOrgCreateAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeAccount` | ✓ `simulator-aws/organizations.go:197::handleOrgDescribeAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeCreateAccountStatus` | ✓ `simulator-aws/organizations.go:198::handleOrgDescribeCreateAccountStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListCreateAccountStatus` | ✓ `simulator-aws/organizations.go:199::handleOrgListCreateAccountStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListAccounts` | ✓ `simulator-aws/organizations.go:200::handleOrgListAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListAccountsForParent` | ✓ `simulator-aws/organizations.go:201::handleOrgListAccountsForParent` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.MoveAccount` | ✓ `simulator-aws/organizations.go:202::handleOrgMoveAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.RemoveAccountFromOrganization` | ✓ `simulator-aws/organizations.go:203::handleOrgRemoveAccountFromOrganization` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.CloseAccount` | ✓ `simulator-aws/organizations.go:204::handleOrgCloseAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.CreateOrganizationalUnit` | ✓ `simulator-aws/organizations.go:207::handleOrgCreateOU` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DeleteOrganizationalUnit` | ✓ `simulator-aws/organizations.go:208::handleOrgDeleteOU` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeOrganizationalUnit` | ✓ `simulator-aws/organizations.go:209::handleOrgDescribeOU` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.UpdateOrganizationalUnit` | ✓ `simulator-aws/organizations.go:210::handleOrgUpdateOU` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListOrganizationalUnitsForParent` | ✓ `simulator-aws/organizations.go:211::handleOrgListOUsForParent` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListRoots` | ✓ `simulator-aws/organizations.go:214::handleOrgListRoots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListChildren` | ✓ `simulator-aws/organizations.go:215::handleOrgListChildren` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListParents` | ✓ `simulator-aws/organizations.go:216::handleOrgListParents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.CreatePolicy` | ✓ `simulator-aws/organizations.go:219::handleOrgCreatePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DeletePolicy` | ✓ `simulator-aws/organizations.go:220::handleOrgDeletePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribePolicy` | ✓ `simulator-aws/organizations.go:221::handleOrgDescribePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.UpdatePolicy` | ✓ `simulator-aws/organizations.go:222::handleOrgUpdatePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.AttachPolicy` | ✓ `simulator-aws/organizations.go:223::handleOrgAttachPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DetachPolicy` | ✓ `simulator-aws/organizations.go:224::handleOrgDetachPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListPolicies` | ✓ `simulator-aws/organizations.go:225::handleOrgListPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListPoliciesForTarget` | ✓ `simulator-aws/organizations.go:226::handleOrgListPoliciesForTarget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListTargetsForPolicy` | ✓ `simulator-aws/organizations.go:227::handleOrgListTargetsForPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.EnablePolicyType` | ✓ `simulator-aws/organizations.go:228::handleOrgEnablePolicyType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DisablePolicyType` | ✓ `simulator-aws/organizations.go:229::handleOrgDisablePolicyType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeEffectivePolicy` | ✓ `simulator-aws/organizations.go:230::handleOrgDescribeEffectivePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.InviteAccountToOrganization` | ✓ `simulator-aws/organizations.go:233::handleOrgInviteAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.AcceptHandshake` | ✓ `simulator-aws/organizations.go:234::handleOrgAcceptHandshake` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DeclineHandshake` | ✓ `simulator-aws/organizations.go:235::handleOrgDeclineHandshake` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.CancelHandshake` | ✓ `simulator-aws/organizations.go:236::handleOrgCancelHandshake` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeHandshake` | ✓ `simulator-aws/organizations.go:237::handleOrgDescribeHandshake` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListHandshakesForAccount` | ✓ `simulator-aws/organizations.go:238::handleOrgListHandshakesForAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListHandshakesForOrganization` | ✓ `simulator-aws/organizations.go:239::handleOrgListHandshakesForOrganization` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.RegisterDelegatedAdministrator` | ✓ `simulator-aws/organizations.go:242::handleOrgRegisterDelegatedAdmin` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DeregisterDelegatedAdministrator` | ✓ `simulator-aws/organizations.go:243::handleOrgDeregisterDelegatedAdmin` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListDelegatedAdministrators` | ✓ `simulator-aws/organizations.go:244::handleOrgListDelegatedAdmins` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListDelegatedServicesForAccount` | ✓ `simulator-aws/organizations.go:245::handleOrgListDelegatedServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.EnableAWSServiceAccess` | ✓ `simulator-aws/organizations.go:246::handleOrgEnableServiceAccess` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DisableAWSServiceAccess` | ✓ `simulator-aws/organizations.go:247::handleOrgDisableServiceAccess` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListAWSServiceAccessForOrganization` | ✓ `simulator-aws/organizations.go:248::handleOrgListServiceAccess` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.PutResourcePolicy` | ✓ `simulator-aws/organizations.go:251::handleOrgPutResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DeleteResourcePolicy` | ✓ `simulator-aws/organizations.go:252::handleOrgDeleteResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.DescribeResourcePolicy` | ✓ `simulator-aws/organizations.go:253::handleOrgDescribeResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.TagResource` | ✓ `simulator-aws/organizations.go:256::handleOrgTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.UntagResource` | ✓ `simulator-aws/organizations.go:257::handleOrgUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSOrganizationsV20161128.ListTagsForResource` | ✓ `simulator-aws/organizations.go:258::handleOrgListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
