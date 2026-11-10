# Sim surface — aws-servicequotas

Surface registered in `simulator-aws/servicequotas.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action ServiceQuotasV20190624.AssociateServiceQuotaTemplate` | ✓ `simulator-aws/servicequotas.go:124::handleSQAssociateServiceQuotaTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.CreateSupportCase` | ✓ `simulator-aws/servicequotas.go:125::handleSQCreateSupportCase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.DeleteServiceQuotaIncreaseRequestFromTemplate` | ✓ `simulator-aws/servicequotas.go:126::handleSQDeleteServiceQuotaIncreaseRequestFromTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.DisassociateServiceQuotaTemplate` | ✓ `simulator-aws/servicequotas.go:127::handleSQDisassociateServiceQuotaTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetAWSDefaultServiceQuota` | ✓ `simulator-aws/servicequotas.go:128::handleSQGetAWSDefaultServiceQuota` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetAssociationForServiceQuotaTemplate` | ✓ `simulator-aws/servicequotas.go:129::handleSQGetAssociationForServiceQuotaTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetAutoManagementConfiguration` | ✓ `simulator-aws/servicequotas.go:130::handleSQGetAutoManagementConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetQuotaUtilizationReport` | ✓ `simulator-aws/servicequotas.go:131::handleSQGetQuotaUtilizationReport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetRequestedServiceQuotaChange` | ✓ `simulator-aws/servicequotas.go:132::handleSQGetRequestedServiceQuotaChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetServiceQuota` | ✓ `simulator-aws/servicequotas.go:133::handleSQGetServiceQuota` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.GetServiceQuotaIncreaseRequestFromTemplate` | ✓ `simulator-aws/servicequotas.go:134::handleSQGetServiceQuotaIncreaseRequestFromTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListAWSDefaultServiceQuotas` | ✓ `simulator-aws/servicequotas.go:135::handleSQListAWSDefaultServiceQuotas` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListRequestedServiceQuotaChangeHistory` | ✓ `simulator-aws/servicequotas.go:136::handleSQListRequestedServiceQuotaChangeHistory` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListRequestedServiceQuotaChangeHistoryByQuota` | ✓ `simulator-aws/servicequotas.go:137::handleSQListRequestedServiceQuotaChangeHistoryByQuota` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListServiceQuotaIncreaseRequestsInTemplate` | ✓ `simulator-aws/servicequotas.go:138::handleSQListServiceQuotaIncreaseRequestsInTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListServiceQuotas` | ✓ `simulator-aws/servicequotas.go:139::handleSQListServiceQuotas` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListServices` | ○ `simulator-aws/servicequotas.go:140::handleSQListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.ListTagsForResource` | ✓ `simulator-aws/servicequotas.go:141::handleSQListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.PutServiceQuotaIncreaseRequestIntoTemplate` | ✓ `simulator-aws/servicequotas.go:142::handleSQPutServiceQuotaIncreaseRequestIntoTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.RequestServiceQuotaIncrease` | ✓ `simulator-aws/servicequotas.go:143::handleSQRequestServiceQuotaIncrease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.StartAutoManagement` | ✓ `simulator-aws/servicequotas.go:144::handleSQStartAutoManagement` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.StartQuotaUtilizationReport` | ✓ `simulator-aws/servicequotas.go:145::handleSQStartQuotaUtilizationReport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.StopAutoManagement` | ✓ `simulator-aws/servicequotas.go:146::handleSQStopAutoManagement` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.TagResource` | ✓ `simulator-aws/servicequotas.go:147::handleSQTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.UntagResource` | ✓ `simulator-aws/servicequotas.go:148::handleSQUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ServiceQuotasV20190624.UpdateAutoManagement` | ✓ `simulator-aws/servicequotas.go:149::handleSQUpdateAutoManagement` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
