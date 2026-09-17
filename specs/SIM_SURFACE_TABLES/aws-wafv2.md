# Sim surface — aws-wafv2

Surface registered in `simulator-aws/wafv2.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AWSWAF_20190729.CreateWebACL` | ✓ `simulator-aws/wafv2.go:261::handleWAFCreateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetWebACL` | ✓ `simulator-aws/wafv2.go:262::handleWAFGetWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateWebACL` | ✓ `simulator-aws/wafv2.go:263::handleWAFUpdateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteWebACL` | ✓ `simulator-aws/wafv2.go:264::handleWAFDeleteWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListWebACLs` | ✓ `simulator-aws/wafv2.go:265::handleWAFListWebACLs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.AssociateWebACL` | ✓ `simulator-aws/wafv2.go:267::handleWAFAssociateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DisassociateWebACL` | ✓ `simulator-aws/wafv2.go:268::handleWAFDisassociateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetWebACLForResource` | ✓ `simulator-aws/wafv2.go:269::handleWAFGetWebACLForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListResourcesForWebACL` | ✓ `simulator-aws/wafv2.go:270::handleWAFListResourcesForWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateIPSet` | ✓ `simulator-aws/wafv2.go:272::handleWAFCreateIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetIPSet` | ✓ `simulator-aws/wafv2.go:273::handleWAFGetIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateIPSet` | ✓ `simulator-aws/wafv2.go:274::handleWAFUpdateIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteIPSet` | ✓ `simulator-aws/wafv2.go:275::handleWAFDeleteIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListIPSets` | ✓ `simulator-aws/wafv2.go:276::handleWAFListIPSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateRuleGroup` | ✓ `simulator-aws/wafv2.go:278::handleWAFCreateRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRuleGroup` | ✓ `simulator-aws/wafv2.go:279::handleWAFGetRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateRuleGroup` | ✓ `simulator-aws/wafv2.go:280::handleWAFUpdateRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteRuleGroup` | ✓ `simulator-aws/wafv2.go:281::handleWAFDeleteRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListRuleGroups` | ✓ `simulator-aws/wafv2.go:282::handleWAFListRuleGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateRegexPatternSet` | ✓ `simulator-aws/wafv2.go:284::handleWAFCreateRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRegexPatternSet` | ✓ `simulator-aws/wafv2.go:285::handleWAFGetRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateRegexPatternSet` | ✓ `simulator-aws/wafv2.go:286::handleWAFUpdateRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteRegexPatternSet` | ✓ `simulator-aws/wafv2.go:287::handleWAFDeleteRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListRegexPatternSets` | ✓ `simulator-aws/wafv2.go:288::handleWAFListRegexSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.TagResource` | ✓ `simulator-aws/wafv2.go:290::handleWAFTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UntagResource` | ✓ `simulator-aws/wafv2.go:291::handleWAFUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListTagsForResource` | ✓ `simulator-aws/wafv2.go:292::handleWAFListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.PutLoggingConfiguration` | ✓ `simulator-aws/wafv2.go:294::handleWAFPutLoggingConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetLoggingConfiguration` | ✓ `simulator-aws/wafv2.go:295::handleWAFGetLoggingConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteLoggingConfiguration` | ✓ `simulator-aws/wafv2.go:296::handleWAFDeleteLoggingConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListLoggingConfigurations` | ✓ `simulator-aws/wafv2.go:297::handleWAFListLoggingConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetSampledRequests` | ✓ `simulator-aws/wafv2.go:299::handleWAFGetSampledRequests` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRevenueStatistics` | ○ `simulator-aws/wafv2.go:300::handleWAFGetRevenueStatistics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRevenueStatisticsSummary` | ○ `simulator-aws/wafv2.go:301::handleWAFGetRevenueStatisticsSummary` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRevenueStatisticsTimeSeries` | ○ `simulator-aws/wafv2.go:302::handleWAFGetRevenueStatisticsTimeSeries` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListSettlementRecords` | ○ `simulator-aws/wafv2.go:303::handleWAFListSettlementRecords` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateAPIKey` | ✓ `simulator-aws/wafv2.go:305::handleWAFCreateAPIKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteAPIKey` | ✓ `simulator-aws/wafv2.go:306::handleWAFDeleteAPIKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListAPIKeys` | ✓ `simulator-aws/wafv2.go:307::handleWAFListAPIKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetDecryptedAPIKey` | ✓ `simulator-aws/wafv2.go:308::handleWAFGetDecryptedAPIKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CheckCapacity` | ○ `simulator-aws/wafv2.go:310::handleWAFCheckCapacity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DescribeManagedRuleGroup` | ○ `simulator-aws/wafv2.go:312::handleWAFDescribeManagedRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DescribeAllManagedProducts` | ○ `simulator-aws/wafv2.go:313::handleWAFDescribeAllManagedProducts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DescribeManagedProductsByVendor` | ○ `simulator-aws/wafv2.go:314::handleWAFDescribeManagedProductsByVendor` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListAvailableManagedRuleGroups` | ○ `simulator-aws/wafv2.go:315::handleWAFListAvailableManagedRuleGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListAvailableManagedRuleGroupVersions` | ○ `simulator-aws/wafv2.go:316::handleWAFListAvailableManagedRuleGroupVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetManagedRuleSet` | ✓ `simulator-aws/wafv2.go:318::handleWAFGetManagedRuleSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListManagedRuleSets` | ✓ `simulator-aws/wafv2.go:319::handleWAFListManagedRuleSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.PutManagedRuleSetVersions` | ✓ `simulator-aws/wafv2.go:320::handleWAFPutManagedRuleSetVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateManagedRuleSetVersionExpiryDate` | ✓ `simulator-aws/wafv2.go:321::handleWAFUpdateManagedRuleSetVersionExpiryDate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.PutPermissionPolicy` | ✓ `simulator-aws/wafv2.go:323::handleWAFPutPermissionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetPermissionPolicy` | ✓ `simulator-aws/wafv2.go:324::handleWAFGetPermissionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeletePermissionPolicy` | ✓ `simulator-aws/wafv2.go:325::handleWAFDeletePermissionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GenerateMobileSdkReleaseUrl` | ○ `simulator-aws/wafv2.go:327::handleWAFGenerateMobileSdkReleaseUrl` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetMobileSdkRelease` | ○ `simulator-aws/wafv2.go:328::handleWAFGetMobileSdkRelease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListMobileSdkReleases` | ○ `simulator-aws/wafv2.go:329::handleWAFListMobileSdkReleases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteFirewallManagerRuleGroups` | ✓ `simulator-aws/wafv2.go:331::handleWAFDeleteFirewallManagerRuleGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRateBasedStatementManagedKeys` | ✓ `simulator-aws/wafv2.go:333::handleWAFGetRateBasedStatementManagedKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetTopPathStatisticsByTraffic` | ✓ `simulator-aws/wafv2.go:334::handleWAFGetTopPathStatisticsByTraffic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Amplify associations are data-plane active: the associated WebACL's default
action and IP-set rules inspect actual hosted requests, terminal BLOCK actions
return HTTP 403, and enabled WebACL/rule visibility configurations retain the
request method, URI, headers, client address, action, response code, and
timestamp. `GetSampledRequests` filters that observed traffic by WebACL,
metric, scope, and the requested (at most three-hour) time window, reports the
real population size, and applies `MaxItems`. Official AWS SDK tests prove
blocking, sampling, disassociation, and the restored hosted response.
<!-- HAND-WRITTEN END -->
