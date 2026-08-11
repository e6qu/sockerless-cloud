# Sim surface — aws-dynamodb

Surface registered in `simulator-aws/dynamodb.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action DynamoDB_20120810.CreateTable` | ✓ `simulator-aws/dynamodb.go:211::handleDDBCreateTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.DescribeTable` | ✓ `simulator-aws/dynamodb.go:212::handleDDBDescribeTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.UpdateTable` | ✓ `simulator-aws/dynamodb.go:213::handleDDBUpdateTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.DeleteTable` | ✓ `simulator-aws/dynamodb.go:214::handleDDBDeleteTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.ListTables` | ✓ `simulator-aws/dynamodb.go:215::handleDDBListTables` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.PutItem` | ✓ `simulator-aws/dynamodb.go:216::handleDDBPutItem` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.GetItem` | ✓ `simulator-aws/dynamodb.go:217::handleDDBGetItem` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.UpdateItem` | ✓ `simulator-aws/dynamodb.go:218::handleDDBUpdateItem` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.DeleteItem` | ✓ `simulator-aws/dynamodb.go:219::handleDDBDeleteItem` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.Query` | ✓ `simulator-aws/dynamodb.go:220::handleDDBQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.Scan` | ✓ `simulator-aws/dynamodb.go:221::handleDDBScan` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.BatchWriteItem` | ✓ `simulator-aws/dynamodb.go:222::handleDDBBatchWriteItem` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.BatchGetItem` | ✓ `simulator-aws/dynamodb.go:223::handleDDBBatchGetItem` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.TransactWriteItems` | ✓ `simulator-aws/dynamodb.go:224::handleDDBTransactWriteItems` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.TransactGetItems` | ✓ `simulator-aws/dynamodb.go:225::handleDDBTransactGetItems` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.DescribeContinuousBackups` | ✓ `simulator-aws/dynamodb.go:226::handleDDBDescribeContinuousBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.UpdateContinuousBackups` | ✓ `simulator-aws/dynamodb.go:227::handleDDBUpdateContinuousBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.DescribeTimeToLive` | ✓ `simulator-aws/dynamodb.go:228::handleDDBDescribeTimeToLive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.UpdateTimeToLive` | ✓ `simulator-aws/dynamodb.go:229::handleDDBUpdateTimeToLive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.ListTagsOfResource` | ✓ `simulator-aws/dynamodb.go:230::handleDDBListTagsOfResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.TagResource` | ✓ `simulator-aws/dynamodb.go:231::handleDDBTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DynamoDB_20120810.UntagResource` | ✓ `simulator-aws/dynamodb.go:232::handleDDBUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
