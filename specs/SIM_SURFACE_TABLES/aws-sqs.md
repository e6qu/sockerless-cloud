# Sim surface — aws-sqs

Surface registered in `simulator-aws/sqs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AmazonSQS.CreateQueue` | ✓ `simulator-aws/sqs.go:335::handleSQSCreateQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.DeleteQueue` | ✓ `simulator-aws/sqs.go:336::handleSQSDeleteQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.GetQueueUrl` | ✓ `simulator-aws/sqs.go:337::handleSQSGetQueueURL` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ListQueues` | ✓ `simulator-aws/sqs.go:338::handleSQSListQueues` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.GetQueueAttributes` | ✓ `simulator-aws/sqs.go:339::handleSQSGetQueueAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.SetQueueAttributes` | ✓ `simulator-aws/sqs.go:340::handleSQSSetQueueAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.SendMessage` | ✓ `simulator-aws/sqs.go:341::handleSQSSendMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.SendMessageBatch` | ✓ `simulator-aws/sqs.go:342::handleSQSSendMessageBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ReceiveMessage` | ✓ `simulator-aws/sqs.go:343::handleSQSReceiveMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.DeleteMessage` | ✓ `simulator-aws/sqs.go:344::handleSQSDeleteMessage` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.DeleteMessageBatch` | ✓ `simulator-aws/sqs.go:345::handleSQSDeleteMessageBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ChangeMessageVisibility` | ✓ `simulator-aws/sqs.go:346::handleSQSChangeMessageVisibility` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ChangeMessageVisibilityBatch` | ✓ `simulator-aws/sqs.go:347::handleSQSChangeMessageVisibilityBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.AddPermission` | ✓ `simulator-aws/sqs.go:348::handleSQSAddPermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.RemovePermission` | ✓ `simulator-aws/sqs.go:349::handleSQSRemovePermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.TagQueue` | ✓ `simulator-aws/sqs.go:350::handleSQSTagQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.UntagQueue` | ✓ `simulator-aws/sqs.go:351::handleSQSUntagQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.ListQueueTags` | ✓ `simulator-aws/sqs.go:352::handleSQSListQueueTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSQS.PurgeQueue` | ✓ `simulator-aws/sqs.go:353::handleSQSPurgeQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
