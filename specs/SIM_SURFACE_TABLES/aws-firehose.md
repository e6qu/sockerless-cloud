# Sim surface — aws-firehose

Surface registered in `simulator-aws/firehose.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
