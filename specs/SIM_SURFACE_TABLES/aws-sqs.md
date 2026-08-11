# Sim surface — aws-sqs

Surface registered in `simulator-aws/sqs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action AmazonSQS.CreateQueue` | ✓ `simulator-aws/sqs.go:307::handleSQSCreateQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.DeleteQueue` | ✓ `simulator-aws/sqs.go:308::handleSQSDeleteQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.GetQueueUrl` | ✓ `simulator-aws/sqs.go:309::handleSQSGetQueueURL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ListQueues` | ✓ `simulator-aws/sqs.go:310::handleSQSListQueues` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.GetQueueAttributes` | ✓ `simulator-aws/sqs.go:311::handleSQSGetQueueAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.SetQueueAttributes` | ✓ `simulator-aws/sqs.go:312::handleSQSSetQueueAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.SendMessage` | ✓ `simulator-aws/sqs.go:313::handleSQSSendMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.SendMessageBatch` | ✓ `simulator-aws/sqs.go:314::handleSQSSendMessageBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ReceiveMessage` | ✓ `simulator-aws/sqs.go:315::handleSQSReceiveMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.DeleteMessage` | ✓ `simulator-aws/sqs.go:316::handleSQSDeleteMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.DeleteMessageBatch` | ✓ `simulator-aws/sqs.go:317::handleSQSDeleteMessageBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ChangeMessageVisibility` | ✓ `simulator-aws/sqs.go:318::handleSQSChangeMessageVisibility` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ChangeMessageVisibilityBatch` | ✓ `simulator-aws/sqs.go:319::handleSQSChangeMessageVisibilityBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.AddPermission` | ✓ `simulator-aws/sqs.go:320::handleSQSAddPermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.RemovePermission` | ✓ `simulator-aws/sqs.go:321::handleSQSRemovePermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.TagQueue` | ✓ `simulator-aws/sqs.go:322::handleSQSTagQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.UntagQueue` | ✓ `simulator-aws/sqs.go:323::handleSQSUntagQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ListQueueTags` | ✓ `simulator-aws/sqs.go:324::handleSQSListQueueTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.PurgeQueue` | ✓ `simulator-aws/sqs.go:325::handleSQSPurgeQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
