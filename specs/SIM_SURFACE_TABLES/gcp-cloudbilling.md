# Sim surface — gcp-cloudbilling

Surface registered in `simulator-gcp/cloudbilling.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/billingAccounts/{accountAction}` | ✓ `simulator-gcp/cloudbilling.go:111::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/billingAccounts/{account}` | ✓ `simulator-gcp/cloudbilling.go:138::handleBillingPatchAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts/{account}/subAccounts` | ✓ `simulator-gcp/cloudbilling.go:139::handleBillingListSubAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/billingAccounts/{account}/subAccounts` | ✓ `simulator-gcp/cloudbilling.go:140::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts/{account}/projects` | ✓ `simulator-gcp/cloudbilling.go:148::handleBillingListAccountProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{organization}/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:153::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{organization}/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:163::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{organization}/billingAccounts/{accountAction}` | ✓ `simulator-gcp/cloudbilling.go:166::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/billingInfo` | ✓ `simulator-gcp/cloudbilling.go:182::handleBillingUpdateProjectInfo` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/services` | ○ `simulator-gcp/cloudbilling.go:184::handleBillingListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/services/{service}/skus` | ○ `simulator-gcp/cloudbilling.go:185::handleBillingListSkus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/tasks/{tasksAction}` | 501 `simulator-gcp/cloudbilling.go:209::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/tasks/{tasksAction}` | 501 `simulator-gcp/cloudbilling.go:221::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/tasks/{tasksId}/pushNotificationConfigs` | 501 `simulator-gcp/cloudbilling.go:229::taskWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/tasks/{tasksId}/pushNotificationConfigs` | 501 `simulator-gcp/cloudbilling.go:230::taskWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/tasks/{tasksId}/pushNotificationConfigs/{pushNotificationConfigsId}` | 501 `simulator-gcp/cloudbilling.go:231::taskWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/tasks/{tasksId}/pushNotificationConfigs/{pushNotificationConfigsId}` | 501 `simulator-gcp/cloudbilling.go:232::taskWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/card` | 501 `simulator-gcp/cloudbilling.go:233::cardWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/message:send` | 501 `simulator-gcp/cloudbilling.go:234::messageWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/message:stream` | 501 `simulator-gcp/cloudbilling.go:235::messageWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/tasks/{rest...}` | 501 `simulator-gcp/cloudbilling.go:240::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/tasks/{rest...}` | 501 `simulator-gcp/cloudbilling.go:255::taskWhy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:88::handleBillingListAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:89::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts/{accountAction}` | ✓ `simulator-gcp/cloudbilling.go:95::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
