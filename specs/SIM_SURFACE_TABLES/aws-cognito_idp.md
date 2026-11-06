# Sim surface — aws-cognito_idp

Surface registered in `simulator-aws/cognito_idp.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AWSCognitoIdentityProviderService.CreateUserPool` | ✓ `simulator-aws/cognito_idp.go:133::handleCognitoCreateUserPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.DescribeUserPool` | ✓ `simulator-aws/cognito_idp.go:134::handleCognitoDescribeUserPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.UpdateUserPool` | ✓ `simulator-aws/cognito_idp.go:135::handleCognitoUpdateUserPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.DeleteUserPool` | ✓ `simulator-aws/cognito_idp.go:136::handleCognitoDeleteUserPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.ListUserPools` | ✓ `simulator-aws/cognito_idp.go:137::handleCognitoListUserPools` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.GetUserPoolMfaConfig` | ✓ `simulator-aws/cognito_idp.go:138::handleCognitoGetUserPoolMfaConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.SetUserPoolMfaConfig` | ✓ `simulator-aws/cognito_idp.go:139::handleCognitoSetUserPoolMfaConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.CreateUserPoolClient` | ✓ `simulator-aws/cognito_idp.go:140::handleCognitoCreateUserPoolClient` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.DescribeUserPoolClient` | ✓ `simulator-aws/cognito_idp.go:141::handleCognitoDescribeUserPoolClient` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.UpdateUserPoolClient` | ✓ `simulator-aws/cognito_idp.go:142::handleCognitoUpdateUserPoolClient` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.DeleteUserPoolClient` | ✓ `simulator-aws/cognito_idp.go:143::handleCognitoDeleteUserPoolClient` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.ListUserPoolClients` | ✓ `simulator-aws/cognito_idp.go:144::handleCognitoListUserPoolClients` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.CreateUserPoolDomain` | ✓ `simulator-aws/cognito_idp.go:145::handleCognitoCreateUserPoolDomain` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.DescribeUserPoolDomain` | ✓ `simulator-aws/cognito_idp.go:146::handleCognitoDescribeUserPoolDomain` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.DeleteUserPoolDomain` | ✓ `simulator-aws/cognito_idp.go:147::handleCognitoDeleteUserPoolDomain` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.AdminCreateUser` | ✓ `simulator-aws/cognito_idp.go:148::handleCognitoAdminCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.AdminGetUser` | ✓ `simulator-aws/cognito_idp.go:149::handleCognitoAdminGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.AdminDeleteUser` | ✓ `simulator-aws/cognito_idp.go:150::handleCognitoAdminDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.AdminSetUserPassword` | ✓ `simulator-aws/cognito_idp.go:151::handleCognitoAdminSetUserPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.AdminEnableUser` | ✓ `simulator-aws/cognito_idp.go:152::handleCognitoAdminEnableUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.AdminDisableUser` | ✓ `simulator-aws/cognito_idp.go:153::handleCognitoAdminDisableUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.ListUsers` | ✓ `simulator-aws/cognito_idp.go:154::handleCognitoListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.TagResource` | ✓ `simulator-aws/cognito_idp.go:155::handleCognitoTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.UntagResource` | ✓ `simulator-aws/cognito_idp.go:156::handleCognitoUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSCognitoIdentityProviderService.ListTagsForResource` | ✓ `simulator-aws/cognito_idp.go:157::handleCognitoListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
