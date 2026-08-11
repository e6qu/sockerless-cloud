# Sim surface — aws-cloudmap

Surface registered in `simulator-aws/cloudmap.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action Route53AutoNaming_v20170314.CreatePrivateDnsNamespace` | ✓ `simulator-aws/cloudmap.go:298::handleCMCreatePrivateDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.CreatePublicDnsNamespace` | ✓ `simulator-aws/cloudmap.go:299::handleCMCreatePublicDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.CreateHttpNamespace` | ✓ `simulator-aws/cloudmap.go:300::handleCMCreateHttpNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetNamespace` | ✓ `simulator-aws/cloudmap.go:301::handleCMGetNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeleteNamespace` | ✓ `simulator-aws/cloudmap.go:302::handleCMDeleteNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateHttpNamespace` | ✓ `simulator-aws/cloudmap.go:303::handleCMUpdateHttpNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdatePrivateDnsNamespace` | ✓ `simulator-aws/cloudmap.go:304::handleCMUpdatePrivateDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdatePublicDnsNamespace` | ✓ `simulator-aws/cloudmap.go:305::handleCMUpdatePublicDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.CreateService` | ✓ `simulator-aws/cloudmap.go:306::handleCMCreateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetService` | ✓ `simulator-aws/cloudmap.go:307::handleCMGetService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateService` | ✓ `simulator-aws/cloudmap.go:308::handleCMUpdateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetServiceAttributes` | ✓ `simulator-aws/cloudmap.go:309::handleCMGetServiceAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateServiceAttributes` | ✓ `simulator-aws/cloudmap.go:310::handleCMUpdateServiceAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeleteServiceAttributes` | ✓ `simulator-aws/cloudmap.go:311::handleCMDeleteServiceAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.RegisterInstance` | ✓ `simulator-aws/cloudmap.go:312::handleCMRegisterInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeregisterInstance` | ✓ `simulator-aws/cloudmap.go:313::handleCMDeregisterInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetInstance` | ✓ `simulator-aws/cloudmap.go:314::handleCMGetInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListInstances` | ✓ `simulator-aws/cloudmap.go:315::handleCMListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateInstanceCustomHealthStatus` | ✓ `simulator-aws/cloudmap.go:316::handleCMUpdateInstanceCustomHealthStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetInstancesHealthStatus` | ✓ `simulator-aws/cloudmap.go:317::handleCMGetInstancesHealthStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DiscoverInstances` | ✓ `simulator-aws/cloudmap.go:318::handleCMDiscoverInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DiscoverInstancesRevision` | ✓ `simulator-aws/cloudmap.go:319::handleCMDiscoverInstancesRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetOperation` | ✓ `simulator-aws/cloudmap.go:320::handleCMGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListOperations` | ✓ `simulator-aws/cloudmap.go:321::handleCMListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListNamespaces` | ✓ `simulator-aws/cloudmap.go:322::handleCMListNamespaces` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListServices` | ✓ `simulator-aws/cloudmap.go:323::handleCMListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeleteService` | ✓ `simulator-aws/cloudmap.go:324::handleCMDeleteService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListTagsForResource` | ✓ `simulator-aws/cloudmap.go:325::handleCMListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.TagResource` | ✓ `simulator-aws/cloudmap.go:326::handleCMTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UntagResource` | ✓ `simulator-aws/cloudmap.go:327::handleCMUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
