# Sim surface — aws-wafv2

Surface registered in `simulator-aws/wafv2.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action AWSWAF_20190729.CreateWebACL` | ✓ `simulator-aws/wafv2.go:266::handleWAFCreateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetWebACL` | ✓ `simulator-aws/wafv2.go:267::handleWAFGetWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateWebACL` | ✓ `simulator-aws/wafv2.go:268::handleWAFUpdateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteWebACL` | ✓ `simulator-aws/wafv2.go:269::handleWAFDeleteWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListWebACLs` | ✓ `simulator-aws/wafv2.go:270::handleWAFListWebACLs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.AssociateWebACL` | ✓ `simulator-aws/wafv2.go:272::handleWAFAssociateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DisassociateWebACL` | ✓ `simulator-aws/wafv2.go:273::handleWAFDisassociateWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetWebACLForResource` | ✓ `simulator-aws/wafv2.go:274::handleWAFGetWebACLForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListResourcesForWebACL` | ✓ `simulator-aws/wafv2.go:275::handleWAFListResourcesForWebACL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateIPSet` | ✓ `simulator-aws/wafv2.go:277::handleWAFCreateIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetIPSet` | ✓ `simulator-aws/wafv2.go:278::handleWAFGetIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateIPSet` | ✓ `simulator-aws/wafv2.go:279::handleWAFUpdateIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteIPSet` | ✓ `simulator-aws/wafv2.go:280::handleWAFDeleteIPSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListIPSets` | ✓ `simulator-aws/wafv2.go:281::handleWAFListIPSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateRuleGroup` | ✓ `simulator-aws/wafv2.go:283::handleWAFCreateRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRuleGroup` | ✓ `simulator-aws/wafv2.go:284::handleWAFGetRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateRuleGroup` | ✓ `simulator-aws/wafv2.go:285::handleWAFUpdateRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteRuleGroup` | ✓ `simulator-aws/wafv2.go:286::handleWAFDeleteRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListRuleGroups` | ✓ `simulator-aws/wafv2.go:287::handleWAFListRuleGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateRegexPatternSet` | ✓ `simulator-aws/wafv2.go:289::handleWAFCreateRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRegexPatternSet` | ✓ `simulator-aws/wafv2.go:290::handleWAFGetRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateRegexPatternSet` | ✓ `simulator-aws/wafv2.go:291::handleWAFUpdateRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteRegexPatternSet` | ✓ `simulator-aws/wafv2.go:292::handleWAFDeleteRegexSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListRegexPatternSets` | ✓ `simulator-aws/wafv2.go:293::handleWAFListRegexSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.TagResource` | ✓ `simulator-aws/wafv2.go:295::handleWAFTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UntagResource` | ✓ `simulator-aws/wafv2.go:296::handleWAFUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListTagsForResource` | ✓ `simulator-aws/wafv2.go:297::handleWAFListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.PutLoggingConfiguration` | ✓ `simulator-aws/wafv2.go:299::handleWAFPutLoggingConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetLoggingConfiguration` | ✓ `simulator-aws/wafv2.go:300::handleWAFGetLoggingConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteLoggingConfiguration` | ✓ `simulator-aws/wafv2.go:301::handleWAFDeleteLoggingConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListLoggingConfigurations` | ✓ `simulator-aws/wafv2.go:302::handleWAFListLoggingConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetSampledRequests` | ✓ `simulator-aws/wafv2.go:304::handleWAFGetSampledRequests` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRevenueStatistics` | ✓ `simulator-aws/wafv2.go:305::handleWAFGetRevenueStatistics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRevenueStatisticsSummary` | ✓ `simulator-aws/wafv2.go:306::handleWAFGetRevenueStatisticsSummary` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRevenueStatisticsTimeSeries` | ✓ `simulator-aws/wafv2.go:307::handleWAFGetRevenueStatisticsTimeSeries` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListSettlementRecords` | ✓ `simulator-aws/wafv2.go:308::handleWAFListSettlementRecords` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CreateAPIKey` | ✓ `simulator-aws/wafv2.go:310::handleWAFCreateAPIKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteAPIKey` | ✓ `simulator-aws/wafv2.go:311::handleWAFDeleteAPIKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListAPIKeys` | ✓ `simulator-aws/wafv2.go:312::handleWAFListAPIKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetDecryptedAPIKey` | ✓ `simulator-aws/wafv2.go:313::handleWAFGetDecryptedAPIKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.CheckCapacity` | ✓ `simulator-aws/wafv2.go:315::handleWAFCheckCapacity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DescribeManagedRuleGroup` | ✓ `simulator-aws/wafv2.go:317::handleWAFDescribeManagedRuleGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DescribeAllManagedProducts` | ✓ `simulator-aws/wafv2.go:318::handleWAFDescribeAllManagedProducts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DescribeManagedProductsByVendor` | ✓ `simulator-aws/wafv2.go:319::handleWAFDescribeManagedProductsByVendor` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListAvailableManagedRuleGroups` | ✓ `simulator-aws/wafv2.go:320::handleWAFListAvailableManagedRuleGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListAvailableManagedRuleGroupVersions` | ✓ `simulator-aws/wafv2.go:321::handleWAFListAvailableManagedRuleGroupVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetManagedRuleSet` | ✓ `simulator-aws/wafv2.go:323::handleWAFGetManagedRuleSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListManagedRuleSets` | ✓ `simulator-aws/wafv2.go:324::handleWAFListManagedRuleSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.PutManagedRuleSetVersions` | ✓ `simulator-aws/wafv2.go:325::handleWAFPutManagedRuleSetVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.UpdateManagedRuleSetVersionExpiryDate` | ✓ `simulator-aws/wafv2.go:326::handleWAFUpdateManagedRuleSetVersionExpiryDate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.PutPermissionPolicy` | ✓ `simulator-aws/wafv2.go:328::handleWAFPutPermissionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetPermissionPolicy` | ✓ `simulator-aws/wafv2.go:329::handleWAFGetPermissionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeletePermissionPolicy` | ✓ `simulator-aws/wafv2.go:330::handleWAFDeletePermissionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GenerateMobileSdkReleaseUrl` | ✓ `simulator-aws/wafv2.go:332::handleWAFGenerateMobileSdkReleaseUrl` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetMobileSdkRelease` | ✓ `simulator-aws/wafv2.go:333::handleWAFGetMobileSdkRelease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.ListMobileSdkReleases` | ✓ `simulator-aws/wafv2.go:334::handleWAFListMobileSdkReleases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.DeleteFirewallManagerRuleGroups` | ✓ `simulator-aws/wafv2.go:336::handleWAFDeleteFirewallManagerRuleGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetRateBasedStatementManagedKeys` | ✓ `simulator-aws/wafv2.go:338::handleWAFGetRateBasedStatementManagedKeys` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSWAF_20190729.GetTopPathStatisticsByTraffic` | ✓ `simulator-aws/wafv2.go:339::handleWAFGetTopPathStatisticsByTraffic` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

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
