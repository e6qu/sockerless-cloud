# Sim surface — aws-cloudmap

Surface registered in `simulator-aws/cloudmap.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action Route53AutoNaming_v20170314.CreatePrivateDnsNamespace` | ✓ `simulator-aws/cloudmap.go:300::handleCMCreatePrivateDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.CreatePublicDnsNamespace` | ✓ `simulator-aws/cloudmap.go:301::handleCMCreatePublicDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.CreateHttpNamespace` | ✓ `simulator-aws/cloudmap.go:302::handleCMCreateHttpNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetNamespace` | ✓ `simulator-aws/cloudmap.go:303::handleCMGetNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeleteNamespace` | ✓ `simulator-aws/cloudmap.go:304::handleCMDeleteNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateHttpNamespace` | ✓ `simulator-aws/cloudmap.go:305::handleCMUpdateHttpNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdatePrivateDnsNamespace` | ✓ `simulator-aws/cloudmap.go:306::handleCMUpdatePrivateDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdatePublicDnsNamespace` | ✓ `simulator-aws/cloudmap.go:307::handleCMUpdatePublicDnsNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.CreateService` | ✓ `simulator-aws/cloudmap.go:308::handleCMCreateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetService` | ✓ `simulator-aws/cloudmap.go:309::handleCMGetService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateService` | ✓ `simulator-aws/cloudmap.go:310::handleCMUpdateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetServiceAttributes` | ✓ `simulator-aws/cloudmap.go:311::handleCMGetServiceAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateServiceAttributes` | ✓ `simulator-aws/cloudmap.go:312::handleCMUpdateServiceAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeleteServiceAttributes` | ✓ `simulator-aws/cloudmap.go:313::handleCMDeleteServiceAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.RegisterInstance` | ✓ `simulator-aws/cloudmap.go:314::handleCMRegisterInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeregisterInstance` | ✓ `simulator-aws/cloudmap.go:315::handleCMDeregisterInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetInstance` | ✓ `simulator-aws/cloudmap.go:316::handleCMGetInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListInstances` | ✓ `simulator-aws/cloudmap.go:317::handleCMListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UpdateInstanceCustomHealthStatus` | ✓ `simulator-aws/cloudmap.go:318::handleCMUpdateInstanceCustomHealthStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetInstancesHealthStatus` | ✓ `simulator-aws/cloudmap.go:319::handleCMGetInstancesHealthStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DiscoverInstances` | ✓ `simulator-aws/cloudmap.go:320::handleCMDiscoverInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DiscoverInstancesRevision` | ✓ `simulator-aws/cloudmap.go:321::handleCMDiscoverInstancesRevision` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.GetOperation` | ✓ `simulator-aws/cloudmap.go:322::handleCMGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListOperations` | ✓ `simulator-aws/cloudmap.go:323::handleCMListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListNamespaces` | ✓ `simulator-aws/cloudmap.go:324::handleCMListNamespaces` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListServices` | ✓ `simulator-aws/cloudmap.go:325::handleCMListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.DeleteService` | ✓ `simulator-aws/cloudmap.go:326::handleCMDeleteService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.ListTagsForResource` | ✓ `simulator-aws/cloudmap.go:327::handleCMListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.TagResource` | ✓ `simulator-aws/cloudmap.go:328::handleCMTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Route53AutoNaming_v20170314.UntagResource` | ✓ `simulator-aws/cloudmap.go:329::handleCMUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
