# Sim surface — aws-autoscaling

Surface registered in `simulator-aws/autoscaling.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action CreateLaunchConfiguration` | ✓ `simulator-aws/autoscaling.go:141::handleASCreateLaunchConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeLaunchConfigurations` | ✓ `simulator-aws/autoscaling.go:142::handleASDescribeLaunchConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteLaunchConfiguration` | ✓ `simulator-aws/autoscaling.go:143::handleASDeleteLaunchConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateAutoScalingGroup` | ✓ `simulator-aws/autoscaling.go:144::handleASCreateAutoScalingGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAutoScalingGroups` | ✓ `simulator-aws/autoscaling.go:145::handleASDescribeAutoScalingGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UpdateAutoScalingGroup` | ✓ `simulator-aws/autoscaling.go:146::handleASUpdateAutoScalingGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetDesiredCapacity` | ✓ `simulator-aws/autoscaling.go:147::handleASSetDesiredCapacity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeScalingActivities` | ✓ `simulator-aws/autoscaling.go:148::handleASDescribeScalingActivities` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateOrUpdateTags` | ✓ `simulator-aws/autoscaling.go:149::handleASCreateOrUpdateTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteTags` | ✓ `simulator-aws/autoscaling.go:150::handleASDeleteTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTags` | ✓ `simulator-aws/autoscaling.go:151::handleASDescribeTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteAutoScalingGroup` | ✓ `simulator-aws/autoscaling.go:152::handleASDeleteAutoScalingGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutScalingPolicy` | ✓ `simulator-aws/autoscaling.go:153::handleASPutScalingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribePolicies` | ✓ `simulator-aws/autoscaling.go:154::handleASDescribePolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeletePolicy` | ✓ `simulator-aws/autoscaling.go:155::handleASDeletePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ExecutePolicy` | ✓ `simulator-aws/autoscaling.go:156::handleASExecutePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutScheduledUpdateGroupAction` | ✓ `simulator-aws/autoscaling.go:157::handleASPutScheduledUpdateGroupAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeScheduledActions` | ✓ `simulator-aws/autoscaling.go:158::handleASDescribeScheduledActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteScheduledAction` | ✓ `simulator-aws/autoscaling.go:159::handleASDeleteScheduledAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutLifecycleHook` | ✓ `simulator-aws/autoscaling.go:160::handleASPutLifecycleHook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeLifecycleHooks` | ✓ `simulator-aws/autoscaling.go:161::handleASDescribeLifecycleHooks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteLifecycleHook` | ✓ `simulator-aws/autoscaling.go:162::handleASDeleteLifecycleHook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAutoScalingInstances` | ✓ `simulator-aws/autoscaling.go:163::handleASDescribeAutoScalingInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetInstanceHealth` | ✓ `simulator-aws/autoscaling.go:164::handleASSetInstanceHealth` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TerminateInstanceInAutoScalingGroup` | ✓ `simulator-aws/autoscaling.go:165::handleASTerminateInstanceInAutoScalingGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
