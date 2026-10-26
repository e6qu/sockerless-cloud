# Sim surface — aws-ecr

Surface registered in `simulator-aws/ecr.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AmazonEC2ContainerRegistry_V20150921.ListImages` | ✓ `simulator-aws/ecr.go:100::handleECRListImages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeImages` | ✓ `simulator-aws/ecr.go:101::handleECRDescribeImages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutImage` | ✓ `simulator-aws/ecr.go:102::handleECRPutImage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.BatchDeleteImage` | ✓ `simulator-aws/ecr.go:103::handleECRBatchDeleteImage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.BatchCheckLayerAvailability` | ✓ `simulator-aws/ecr.go:104::handleECRBatchCheckLayerAvailability` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutLifecyclePolicy` | ✓ `simulator-aws/ecr.go:105::handleECRPutLifecyclePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetLifecyclePolicy` | ✓ `simulator-aws/ecr.go:106::handleECRGetLifecyclePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteLifecyclePolicy` | ✓ `simulator-aws/ecr.go:107::handleECRDeleteLifecyclePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.ListTagsForResource` | ✓ `simulator-aws/ecr.go:108::handleECRListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.TagResource` | ✓ `simulator-aws/ecr.go:109::handleECRTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.UntagResource` | ✓ `simulator-aws/ecr.go:110::handleECRUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.CreatePullThroughCacheRule` | ✓ `simulator-aws/ecr.go:112::handleECRCreatePullThroughCacheRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribePullThroughCacheRules` | ✓ `simulator-aws/ecr.go:113::handleECRDescribePullThroughCacheRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeletePullThroughCacheRule` | ✓ `simulator-aws/ecr.go:114::handleECRDeletePullThroughCacheRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.CreateRepository` | ✓ `simulator-aws/ecr.go:95::handleECRCreateRepository` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeRepositories` | ✓ `simulator-aws/ecr.go:96::handleECRDescribeRepositories` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteRepository` | ✓ `simulator-aws/ecr.go:97::handleECRDeleteRepository` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken` | ✓ `simulator-aws/ecr.go:98::handleECRGetAuthorizationToken` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.BatchGetImage` | ✓ `simulator-aws/ecr.go:99::handleECRBatchGetImage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.SetRepositoryPolicy` | ✓ `simulator-aws/ecr_layers.go:38::handleECRSetRepositoryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetRepositoryPolicy` | ✓ `simulator-aws/ecr_layers.go:39::handleECRGetRepositoryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteRepositoryPolicy` | ✓ `simulator-aws/ecr_layers.go:40::handleECRDeleteRepositoryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.InitiateLayerUpload` | ✓ `simulator-aws/ecr_layers.go:41::handleECRInitiateLayerUpload` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.UploadLayerPart` | ✓ `simulator-aws/ecr_layers.go:42::handleECRUploadLayerPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.CompleteLayerUpload` | ✓ `simulator-aws/ecr_layers.go:43::handleECRCompleteLayerUpload` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetDownloadUrlForLayer` | ✓ `simulator-aws/ecr_layers.go:44::handleECRGetDownloadUrlForLayer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutImageTagMutability` | ✓ `simulator-aws/ecr_registry.go:55::handleECRPutImageTagMutability` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutImageScanningConfiguration` | ✓ `simulator-aws/ecr_registry.go:56::handleECRPutImageScanningConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.StartImageScan` | ✓ `simulator-aws/ecr_registry.go:57::handleECRStartImageScan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeImageScanFindings` | ✓ `simulator-aws/ecr_registry.go:58::handleECRDescribeImageScanFindings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.StartLifecyclePolicyPreview` | ✓ `simulator-aws/ecr_registry.go:59::handleECRStartLifecyclePolicyPreview` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetLifecyclePolicyPreview` | ✓ `simulator-aws/ecr_registry.go:60::handleECRGetLifecyclePolicyPreview` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeRegistry` | ✓ `simulator-aws/ecr_registry.go:61::handleECRDescribeRegistry` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutRegistryPolicy` | ✓ `simulator-aws/ecr_registry.go:62::handleECRPutRegistryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetRegistryPolicy` | ✓ `simulator-aws/ecr_registry.go:63::handleECRGetRegistryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteRegistryPolicy` | ✓ `simulator-aws/ecr_registry.go:64::handleECRDeleteRegistryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutReplicationConfiguration` | ✓ `simulator-aws/ecr_registry.go:65::handleECRPutReplicationConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeImageReplicationStatus` | ✓ `simulator-aws/ecr_registry.go:66::handleECRDescribeImageReplicationStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
