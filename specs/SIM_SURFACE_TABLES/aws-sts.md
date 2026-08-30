# Sim surface — aws-sts

Surface registered in `simulator-aws/sts.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action GetCallerIdentity` | ✓ `simulator-aws/sts.go:48::handleGetCallerIdentity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AssumeRole` | ✓ `simulator-aws/sts.go:49::handleSTSAssumeRole` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AssumeRoleWithWebIdentity` | ✓ `simulator-aws/sts.go:50::handleSTSAssumeRoleWithWebIdentity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetSessionToken` | ✓ `simulator-aws/sts.go:51::handleSTSGetSessionToken` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetFederationToken` | ✓ `simulator-aws/sts.go:52::handleSTSGetFederationToken` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AssumeRoleWithSAML` | ✓ `simulator-aws/sts.go:53::handleSTSAssumeRoleWithSAML` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetWebIdentityToken` | ○ `simulator-aws/sts.go:54::handleSTSGetWebIdentityToken` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetDelegatedAccessToken` | ✓ `simulator-aws/sts.go:55::handleSTSGetDelegatedAccessToken` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AssumeRoot` | ✓ `simulator-aws/sts.go:56::handleSTSAssumeRoot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecodeAuthorizationMessage` | ○ `simulator-aws/sts.go:57::handleSTSDecodeAuthorizationMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetAccessKeyInfo` | ○ `simulator-aws/sts.go:58::handleSTSGetAccessKeyInfo` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
