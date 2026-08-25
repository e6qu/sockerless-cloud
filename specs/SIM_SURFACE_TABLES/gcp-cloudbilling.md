# Sim surface — gcp-cloudbilling

Surface registered in `simulator-gcp/cloudbilling.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /v1/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:105::handleBillingListAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:106::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts/{accountAction}` | ✓ `simulator-gcp/cloudbilling.go:113::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/billingAccounts/{accountAction}` | ✓ `simulator-gcp/cloudbilling.go:129::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/billingAccounts/{account}` | ✓ `simulator-gcp/cloudbilling.go:156::handleBillingPatchAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts/{account}/subAccounts` | ✓ `simulator-gcp/cloudbilling.go:157::handleBillingListSubAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/billingAccounts/{account}/subAccounts` | ✓ `simulator-gcp/cloudbilling.go:158::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/billingAccounts/{account}/projects` | ✓ `simulator-gcp/cloudbilling.go:166::handleBillingListAccountProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{organization}/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:172::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/organizations/{organization}/billingAccounts` | ✓ `simulator-gcp/cloudbilling.go:182::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/organizations/{organization}/billingAccounts/{accountAction}` | ✓ `simulator-gcp/cloudbilling.go:185::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/billingInfo` | ✓ `simulator-gcp/cloudbilling.go:201::handleBillingUpdateProjectInfo` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/services` | ✓ `simulator-gcp/cloudbilling.go:204::handleBillingListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/services/{service}/skus` | ✓ `simulator-gcp/cloudbilling.go:205::handleBillingListSkus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
