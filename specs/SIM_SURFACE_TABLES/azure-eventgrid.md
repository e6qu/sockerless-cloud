# Sim surface — azure-eventgrid

Surface registered in `simulator-azure/eventgrid.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/topics` | ✓ `simulator-azure/eventgrid.go:65::handleEventGridListTopicsBySubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/domains` | ✓ `simulator-azure/eventgrid.go:78::handleEventGridListDomainsBySubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/systemTopics` | ✓ `simulator-azure/eventgrid.go:92::handleEventGridListSystemTopicsBySubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerTopics` | ✓ `simulator-azure/eventgrid.go:105::handleEventGridListPartnerTopicsBySubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /api/events` | ✓ `simulator-azure/eventgrid.go:127::handleEventGridPublishEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.EventGrid/operations` | ✓ `simulator-azure/eventgrid_more.go:71::handleEventGridListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.EventGrid/topicTypes` | ✓ `simulator-azure/eventgrid_more.go:72::handleEventGridListTopicTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}` | ✓ `simulator-azure/eventgrid_more.go:73::handleEventGridGetTopicType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventTypes` | ✓ `simulator-azure/eventgrid_more.go:74::handleEventGridListTopicTypeEventTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:108::handleEventGridListSubsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:109::handleEventGridListSubsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:110::handleEventGridListSubsRegional` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:111::handleEventGridListSubsRegional` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:112::handleEventGridListSubsForTopicType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:113::handleEventGridListSubsForTopicType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:114::handleEventGridListSubsForTopicType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions` | ✓ `simulator-azure/eventgrid_more.go:115::handleEventGridListSubsForTopicType` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerRegistrations` | ✓ `simulator-azure/eventgrid_partner.go:41::handleEventGridListPartnerRegistrationsBySub` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerNamespaces` | ✓ `simulator-azure/eventgrid_partner.go:49::handleEventGridListPartnerNamespacesBySub` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerConfigurations` | ✓ `simulator-azure/eventgrid_partner.go:67::handleEventGridListPartnerConfigurationsBySub` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.EventGrid/verifiedPartners` | ✓ `simulator-azure/eventgrid_partner.go:72::handleEventGridListVerifiedPartners` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.EventGrid/verifiedPartners/{verifiedPartnerName}` | ✓ `simulator-azure/eventgrid_partner.go:73::handleEventGridGetVerifiedPartner` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
