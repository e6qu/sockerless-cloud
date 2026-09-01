# Sim surface — azure-subscription

Surface registered in `simulator-azure/subscription.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /subscriptions/{subscriptionId}/locations` | ○ `simulator-azure/subscription.go:106::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /tenants` | ○ `simulator-azure/subscription.go:112::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /providers/Microsoft.Resources/checkResourceName` | ○ `simulator-azure/subscription.go:126::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.Resources/checkZonePeers/` | ○ `simulator-azure/subscription.go:148::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}` | ✓ `simulator-azure/subscription.go:58::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers` | ○ `simulator-azure/subscription.go:64::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions` | ✓ `simulator-azure/subscription.go:86::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/cancel` | ✓ `simulator-azure/subscription_alias.go:110::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/enable` | ✓ `simulator-azure/subscription_alias.go:117::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/rename` | ✓ `simulator-azure/subscription_alias.go:124::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /providers/Microsoft.Subscription/aliases/{aliasName}` | ✓ `simulator-azure/subscription_alias.go:68::handleSubscriptionAliasCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.Subscription/aliases/{aliasName}` | ✓ `simulator-azure/subscription_alias.go:71::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /providers/Microsoft.Subscription/aliases/{aliasName}` | ✓ `simulator-azure/subscription_alias.go:88::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.Subscription/aliases` | ✓ `simulator-azure/subscription_alias.go:98::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->

The simulator serves every operation the vendored
`subscription-arm-subscriptions-2021-10-01` Swagger declares (15 of 15; the
count is locked by `azureMethodFloor` in `simulator-azure/azure_coverage_test.go`).

The `tf-test` cells above read `n/a` for the ownership, policy and
operation-catalog rows because the AzureRM provider wraps none of them:
`azurerm_subscription` covers the alias creation and the cancel/rename/enable
actions, and there is no provider resource for the ownership handover, the
tenant policy, the billing-account policy, or the provider operation catalog.

<!-- HAND-WRITTEN END -->
