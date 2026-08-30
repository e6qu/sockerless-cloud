# Sim surface — aws-budgets

Surface registered in `simulator-aws/budgets.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AWSBudgetServiceGateway.CreateBudget` | ✓ `simulator-aws/budgets.go:79::handleBudgetsCreateBudget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudget` | ✓ `simulator-aws/budgets.go:80::handleBudgetsDescribeBudget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DeleteBudget` | ✓ `simulator-aws/budgets.go:81::handleBudgetsDeleteBudget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.UpdateBudget` | ✓ `simulator-aws/budgets.go:82::handleBudgetsUpdateBudget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgets` | ✓ `simulator-aws/budgets.go:83::handleBudgetsDescribeBudgets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.CreateNotification` | ✓ `simulator-aws/budgets.go:84::handleBudgetsCreateNotification` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeNotificationsForBudget` | ✓ `simulator-aws/budgets.go:85::handleBudgetsDescribeNotificationsForBudget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DeleteNotification` | ✓ `simulator-aws/budgets.go:86::handleBudgetsDeleteNotification` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.CreateSubscriber` | ✓ `simulator-aws/budgets.go:87::handleBudgetsCreateSubscriber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeSubscribersForNotification` | ✓ `simulator-aws/budgets.go:88::handleBudgetsDescribeSubscribersForNotification` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DeleteSubscriber` | ✓ `simulator-aws/budgets.go:89::handleBudgetsDeleteSubscriber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.ListTagsForResource` | ✓ `simulator-aws/budgets.go:90::handleBudgetsListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.TagResource` | ✓ `simulator-aws/budgets.go:91::handleBudgetsTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.UntagResource` | ✓ `simulator-aws/budgets.go:92::handleBudgetsUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.CreateBudgetAction` | ✓ `simulator-aws/budgets_actions.go:69::handleBudgetsCreateBudgetAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgetAction` | ✓ `simulator-aws/budgets_actions.go:70::handleBudgetsDescribeBudgetAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgetActionsForBudget` | ✓ `simulator-aws/budgets_actions.go:71::handleBudgetsDescribeBudgetActionsForBudget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgetActionsForAccount` | ✓ `simulator-aws/budgets_actions.go:72::handleBudgetsDescribeBudgetActionsForAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgetActionHistories` | ✓ `simulator-aws/budgets_actions.go:73::handleBudgetsDescribeBudgetActionHistories` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.UpdateBudgetAction` | ✓ `simulator-aws/budgets_actions.go:74::handleBudgetsUpdateBudgetAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DeleteBudgetAction` | ✓ `simulator-aws/budgets_actions.go:75::handleBudgetsDeleteBudgetAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.ExecuteBudgetAction` | ✓ `simulator-aws/budgets_actions.go:76::handleBudgetsExecuteBudgetAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.UpdateNotification` | ✓ `simulator-aws/budgets_actions.go:472::handleBudgetsUpdateNotification` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.UpdateSubscriber` | ✓ `simulator-aws/budgets_actions.go:473::handleBudgetsUpdateSubscriber` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgetNotificationsForAccount` | ✓ `simulator-aws/budgets_actions.go:474::handleBudgetsDescribeBudgetNotificationsForAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSBudgetServiceGateway.DescribeBudgetPerformanceHistory` | ✓ `simulator-aws/budgets_actions.go:475::handleBudgetsDescribeBudgetPerformanceHistory` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
