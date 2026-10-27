# Sim surface — aws-secretsmanager

Surface registered in `simulator-aws/secretsmanager.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action secretsmanager.CreateSecret` | ✓ `simulator-aws/secretsmanager.go:166::handleSMCreateSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.GetSecretValue` | ✓ `simulator-aws/secretsmanager.go:167::handleSMGetSecretValue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.DescribeSecret` | ✓ `simulator-aws/secretsmanager.go:168::handleSMDescribeSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.UpdateSecret` | ✓ `simulator-aws/secretsmanager.go:169::handleSMUpdateSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.PutSecretValue` | ✓ `simulator-aws/secretsmanager.go:170::handleSMPutSecretValue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.DeleteSecret` | ✓ `simulator-aws/secretsmanager.go:171::handleSMDeleteSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.ListSecrets` | ✓ `simulator-aws/secretsmanager.go:172::handleSMListSecrets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.ListSecretVersionIds` | ✓ `simulator-aws/secretsmanager.go:173::handleSMListSecretVersionIds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.TagResource` | ✓ `simulator-aws/secretsmanager.go:174::handleSMTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.UntagResource` | ✓ `simulator-aws/secretsmanager.go:175::handleSMUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.GetResourcePolicy` | ✓ `simulator-aws/secretsmanager.go:176::handleSMGetResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.GetRandomPassword` | ○ `simulator-aws/secretsmanager.go:177::handleSMGetRandomPassword` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.PutResourcePolicy` | ✓ `simulator-aws/secretsmanager.go:178::handleSMPutResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.DeleteResourcePolicy` | ✓ `simulator-aws/secretsmanager.go:179::handleSMDeleteResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.ValidateResourcePolicy` | ○ `simulator-aws/secretsmanager.go:180::handleSMValidateResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.RestoreSecret` | ✓ `simulator-aws/secretsmanager.go:181::handleSMRestoreSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.RotateSecret` | ✓ `simulator-aws/secretsmanager.go:182::handleSMRotateSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.CancelRotateSecret` | ✓ `simulator-aws/secretsmanager.go:183::handleSMCancelRotateSecret` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.BatchGetSecretValue` | ✓ `simulator-aws/secretsmanager.go:184::handleSMBatchGetSecretValue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.UpdateSecretVersionStage` | ✓ `simulator-aws/secretsmanager.go:185::handleSMUpdateSecretVersionStage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.ReplicateSecretToRegions` | ✓ `simulator-aws/secretsmanager.go:186::handleSMReplicateSecretToRegions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.RemoveRegionsFromReplication` | ✓ `simulator-aws/secretsmanager.go:187::handleSMRemoveRegionsFromReplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action secretsmanager.StopReplicationToReplica` | ✓ `simulator-aws/secretsmanager.go:188::handleSMStopReplicationToReplica` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
