# Sim surface — aws-firehose

Surface registered in `simulator-aws/firehose.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action Firehose_20150804.CreateDeliveryStream` | ✓ `simulator-aws/firehose.go:151::handleFirehoseCreateDeliveryStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.DeleteDeliveryStream` | ✓ `simulator-aws/firehose.go:152::handleFirehoseDeleteDeliveryStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.DescribeDeliveryStream` | ✓ `simulator-aws/firehose.go:153::handleFirehoseDescribeDeliveryStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.ListDeliveryStreams` | ✓ `simulator-aws/firehose.go:154::handleFirehoseListDeliveryStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.ListTagsForDeliveryStream` | ✓ `simulator-aws/firehose.go:155::handleFirehoseListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.PutRecord` | ✓ `simulator-aws/firehose.go:156::handleFirehosePutRecord` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.PutRecordBatch` | ✓ `simulator-aws/firehose.go:157::handleFirehosePutRecordBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.StartDeliveryStreamEncryption` | ✓ `simulator-aws/firehose.go:158::handleFirehoseStartEncryption` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.StopDeliveryStreamEncryption` | ✓ `simulator-aws/firehose.go:159::handleFirehoseStopEncryption` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.TagDeliveryStream` | ✓ `simulator-aws/firehose.go:160::handleFirehoseTagDeliveryStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.UntagDeliveryStream` | ✓ `simulator-aws/firehose.go:161::handleFirehoseUntagDeliveryStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Firehose_20150804.UpdateDestination` | ✓ `simulator-aws/firehose.go:162::handleFirehoseUpdateDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
