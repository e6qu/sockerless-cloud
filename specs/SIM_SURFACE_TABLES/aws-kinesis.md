# Sim surface — aws-kinesis

Surface registered in `simulator-aws/kinesis.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action Kinesis_20131202.CreateStream` | ✓ `simulator-aws/kinesis.go:131::handleKinesisCreateStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DeleteStream` | ✓ `simulator-aws/kinesis.go:132::handleKinesisDeleteStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DescribeStream` | ✓ `simulator-aws/kinesis.go:133::handleKinesisDescribeStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DescribeStreamSummary` | ✓ `simulator-aws/kinesis.go:134::handleKinesisDescribeStreamSummary` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.ListStreams` | ✓ `simulator-aws/kinesis.go:135::handleKinesisListStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.ListShards` | ✓ `simulator-aws/kinesis.go:136::handleKinesisListShards` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.PutRecord` | ✓ `simulator-aws/kinesis.go:137::handleKinesisPutRecord` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.PutRecords` | ✓ `simulator-aws/kinesis.go:138::handleKinesisPutRecords` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.GetShardIterator` | ✓ `simulator-aws/kinesis.go:139::handleKinesisGetShardIterator` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.GetRecords` | ✓ `simulator-aws/kinesis.go:140::handleKinesisGetRecords` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.AddTagsToStream` | ✓ `simulator-aws/kinesis.go:141::handleKinesisAddTagsToStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.RemoveTagsFromStream` | ✓ `simulator-aws/kinesis.go:142::handleKinesisRemoveTagsFromStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.ListTagsForStream` | ✓ `simulator-aws/kinesis.go:143::handleKinesisListTagsForStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.IncreaseStreamRetentionPeriod` | ✓ `simulator-aws/kinesis.go:144::handleKinesisIncreaseStreamRetentionPeriod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DecreaseStreamRetentionPeriod` | ✓ `simulator-aws/kinesis.go:145::handleKinesisDecreaseStreamRetentionPeriod` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.EnableEnhancedMonitoring` | ✓ `simulator-aws/kinesis.go:146::handleKinesisEnableEnhancedMonitoring` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DisableEnhancedMonitoring` | ✓ `simulator-aws/kinesis.go:147::handleKinesisDisableEnhancedMonitoring` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.StartStreamEncryption` | ✓ `simulator-aws/kinesis.go:148::handleKinesisStartStreamEncryption` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.StopStreamEncryption` | ✓ `simulator-aws/kinesis.go:149::handleKinesisStopStreamEncryption` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateShardCount` | ✓ `simulator-aws/kinesis.go:150::handleKinesisUpdateShardCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DescribeLimits` | ✓ `simulator-aws/kinesis.go:151::handleKinesisDescribeLimits` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.RegisterStreamConsumer` | ✓ `simulator-aws/kinesis.go:152::handleKinesisRegisterStreamConsumer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DeregisterStreamConsumer` | ✓ `simulator-aws/kinesis.go:153::handleKinesisDeregisterStreamConsumer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DescribeStreamConsumer` | ✓ `simulator-aws/kinesis.go:154::handleKinesisDescribeStreamConsumer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.ListStreamConsumers` | ✓ `simulator-aws/kinesis.go:155::handleKinesisListStreamConsumers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.PutResourcePolicy` | ✓ `simulator-aws/kinesis.go:156::handleKinesisPutResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.GetResourcePolicy` | ✓ `simulator-aws/kinesis.go:157::handleKinesisGetResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DeleteResourcePolicy` | ✓ `simulator-aws/kinesis.go:158::handleKinesisDeleteResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.MergeShards` | ✓ `simulator-aws/kinesis.go:159::handleKinesisMergeShards` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.SplitShard` | ✓ `simulator-aws/kinesis.go:160::handleKinesisSplitShard` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.TagResource` | ✓ `simulator-aws/kinesis.go:161::handleKinesisTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UntagResource` | ✓ `simulator-aws/kinesis.go:162::handleKinesisUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.ListTagsForResource` | ✓ `simulator-aws/kinesis.go:163::handleKinesisListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateStreamMode` | ✓ `simulator-aws/kinesis.go:164::handleKinesisUpdateStreamMode` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DescribeAccountSettings` | ✓ `simulator-aws/kinesis.go:165::handleKinesisDescribeAccountSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateAccountSettings` | ✓ `simulator-aws/kinesis.go:166::handleKinesisUpdateAccountSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateMaxRecordSize` | ✓ `simulator-aws/kinesis.go:167::handleKinesisUpdateMaxRecordSize` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateStreamWarmThroughput` | ✓ `simulator-aws/kinesis.go:168::handleKinesisUpdateStreamWarmThroughput` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateStreamRecordDistributionStrategy` | ✓ `simulator-aws/kinesis.go:169::handleKinesisUpdateStreamRecordDistributionStrategy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.CreateChannel` | ✓ `simulator-aws/kinesis_channels.go:49::handleKinesisCreateChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DescribeChannel` | ✓ `simulator-aws/kinesis_channels.go:50::handleKinesisDescribeChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.UpdateChannel` | ✓ `simulator-aws/kinesis_channels.go:51::handleKinesisUpdateChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.DeleteChannel` | ✓ `simulator-aws/kinesis_channels.go:52::handleKinesisDeleteChannel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.ListChannels` | ✓ `simulator-aws/kinesis_channels.go:53::handleKinesisListChannels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Kinesis_20131202.SubscribeToShard` | ✓ `simulator-aws/kinesis_streaming.go:21::handleKinesisSubscribeToShard` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
