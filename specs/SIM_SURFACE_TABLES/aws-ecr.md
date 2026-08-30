# Sim surface — aws-ecr

Surface registered in `simulator-aws/ecr.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action AmazonEC2ContainerRegistry_V20150921.CreateRepository` | ✓ `simulator-aws/ecr.go:127::handleECRCreateRepository` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeRepositories` | ✓ `simulator-aws/ecr.go:128::handleECRDescribeRepositories` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteRepository` | ✓ `simulator-aws/ecr.go:129::handleECRDeleteRepository` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken` | ✓ `simulator-aws/ecr.go:130::handleECRGetAuthorizationToken` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.BatchGetImage` | ✓ `simulator-aws/ecr.go:131::handleECRBatchGetImage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.ListImages` | ✓ `simulator-aws/ecr.go:132::handleECRListImages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeImages` | ✓ `simulator-aws/ecr.go:133::handleECRDescribeImages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutImage` | ✓ `simulator-aws/ecr.go:134::handleECRPutImage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.BatchDeleteImage` | ✓ `simulator-aws/ecr.go:135::handleECRBatchDeleteImage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.BatchCheckLayerAvailability` | ✓ `simulator-aws/ecr.go:136::handleECRBatchCheckLayerAvailability` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutLifecyclePolicy` | ✓ `simulator-aws/ecr.go:137::handleECRPutLifecyclePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetLifecyclePolicy` | ✓ `simulator-aws/ecr.go:138::handleECRGetLifecyclePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteLifecyclePolicy` | ✓ `simulator-aws/ecr.go:139::handleECRDeleteLifecyclePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.ListTagsForResource` | ✓ `simulator-aws/ecr.go:140::handleECRListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.TagResource` | ✓ `simulator-aws/ecr.go:141::handleECRTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.UntagResource` | ✓ `simulator-aws/ecr.go:142::handleECRUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.CreatePullThroughCacheRule` | ✓ `simulator-aws/ecr.go:150::handleECRCreatePullThroughCacheRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribePullThroughCacheRules` | ✓ `simulator-aws/ecr.go:151::handleECRDescribePullThroughCacheRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeletePullThroughCacheRule` | ✓ `simulator-aws/ecr.go:152::handleECRDeletePullThroughCacheRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.SetRepositoryPolicy` | ✓ `simulator-aws/ecr_layers.go:43::handleECRSetRepositoryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetRepositoryPolicy` | ✓ `simulator-aws/ecr_layers.go:44::handleECRGetRepositoryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteRepositoryPolicy` | ✓ `simulator-aws/ecr_layers.go:45::handleECRDeleteRepositoryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.InitiateLayerUpload` | ✓ `simulator-aws/ecr_layers.go:46::handleECRInitiateLayerUpload` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.UploadLayerPart` | ✓ `simulator-aws/ecr_layers.go:47::handleECRUploadLayerPart` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.CompleteLayerUpload` | ✓ `simulator-aws/ecr_layers.go:48::handleECRCompleteLayerUpload` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetDownloadUrlForLayer` | ✓ `simulator-aws/ecr_layers.go:49::handleECRGetDownloadUrlForLayer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutImageTagMutability` | ✓ `simulator-aws/ecr_registry.go:71::handleECRPutImageTagMutability` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutImageScanningConfiguration` | ✓ `simulator-aws/ecr_registry.go:72::handleECRPutImageScanningConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.StartImageScan` | ✓ `simulator-aws/ecr_registry.go:73::handleECRStartImageScan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeImageScanFindings` | ✓ `simulator-aws/ecr_registry.go:74::handleECRDescribeImageScanFindings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.StartLifecyclePolicyPreview` | ✓ `simulator-aws/ecr_registry.go:75::handleECRStartLifecyclePolicyPreview` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetLifecyclePolicyPreview` | ✓ `simulator-aws/ecr_registry.go:76::handleECRGetLifecyclePolicyPreview` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeRegistry` | ✓ `simulator-aws/ecr_registry.go:77::handleECRDescribeRegistry` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutRegistryPolicy` | ✓ `simulator-aws/ecr_registry.go:78::handleECRPutRegistryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.GetRegistryPolicy` | ✓ `simulator-aws/ecr_registry.go:79::handleECRGetRegistryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DeleteRegistryPolicy` | ✓ `simulator-aws/ecr_registry.go:80::handleECRDeleteRegistryPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.PutReplicationConfiguration` | ✓ `simulator-aws/ecr_registry.go:81::handleECRPutReplicationConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerRegistry_V20150921.DescribeImageReplicationStatus` | ✓ `simulator-aws/ecr_registry.go:82::handleECRDescribeImageReplicationStatus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
