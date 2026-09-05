package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ddbRequiredMembers lists, per DynamoDB operation, the top-level input members
// the AWS API marks @required in its Smithy model. Real DynamoDB rejects a
// request that omits one with a ValidationException *before* any business logic
// runs (so a missing TableName is a validation error, not a phantom
// ResourceNotFoundException). The registry is enforced against the vendored
// Smithy model by TestDDBRequiredMembersMatchSpec — if AWS marks a new member
// required, or this table drifts, that test fails.
var ddbRequiredMembers = map[string][]string{
	"CreateTable":               {"TableName"},
	"DescribeTable":             {"TableName"},
	"UpdateTable":               {"TableName"},
	"SearchVectors":             {"IndexName", "SearchVector", "TableName", "TopK"},
	"DeleteTable":               {"TableName"},
	"PutItem":                   {"TableName", "Item"},
	"GetItem":                   {"TableName", "Key"},
	"UpdateItem":                {"TableName", "Key"},
	"DeleteItem":                {"TableName", "Key"},
	"Query":                     {"TableName"},
	"Scan":                      {"TableName"},
	"BatchWriteItem":            {"RequestItems"},
	"BatchGetItem":              {"RequestItems"},
	"TransactWriteItems":        {"TransactItems"},
	"TransactGetItems":          {"TransactItems"},
	"DescribeContinuousBackups": {"TableName"},
	"UpdateContinuousBackups":   {"TableName", "PointInTimeRecoverySpecification"},
	"DescribeTimeToLive":        {"TableName"},
	"UpdateTimeToLive":          {"TableName", "TimeToLiveSpecification"},
	"ListTagsOfResource":        {"ResourceArn"},
	"TagResource":               {"ResourceArn", "Tags"},
	"UntagResource":             {"ResourceArn", "TagKeys"},
	"ExecuteStatement":          {"Statement"},
	"BatchExecuteStatement":     {"Statements"},
	"ExecuteTransaction":        {"TransactStatements"},
	// Backups.
	"CreateBackup":              {"BackupName", "TableName"},
	"DescribeBackup":            {"BackupArn"},
	"DeleteBackup":              {"BackupArn"},
	"RestoreTableFromBackup":    {"BackupArn", "TargetTableName"},
	"RestoreTableToPointInTime": {"TargetTableName"},
	// Global tables.
	"CreateGlobalTable":               {"GlobalTableName", "ReplicationGroup"},
	"DescribeGlobalTable":             {"GlobalTableName"},
	"UpdateGlobalTable":               {"GlobalTableName", "ReplicaUpdates"},
	"DescribeGlobalTableSettings":     {"GlobalTableName"},
	"UpdateGlobalTableSettings":       {"GlobalTableName"},
	"UpdateTableReplicaAutoScaling":   {"TableName"},
	"DescribeTableReplicaAutoScaling": {"TableName"},
	// Resource-based policy.
	"PutResourcePolicy":    {"Policy", "ResourceArn"},
	"GetResourcePolicy":    {"ResourceArn"},
	"DeleteResourcePolicy": {"ResourceArn"},
	// Kinesis streaming destinations.
	"EnableKinesisStreamingDestination":   {"StreamArn", "TableName"},
	"DisableKinesisStreamingDestination":  {"StreamArn", "TableName"},
	"DescribeKinesisStreamingDestination": {"TableName"},
	"UpdateKinesisStreamingDestination":   {"StreamArn", "TableName"},
	// Exports / imports.
	"ExportTableToPointInTime": {"S3Bucket", "TableArn"},
	"DescribeExport":           {"ExportArn"},
	"ImportTable":              {"InputFormat", "S3BucketSource", "TableCreationParameters"},
	"DescribeImport":           {"ImportArn"},
	// Contributor insights.
	"UpdateContributorInsights":   {"ContributorInsightsAction", "TableName"},
	"DescribeContributorInsights": {"TableName"},
	// ListTables, DescribeLimits, ListBackups, ListGlobalTables, ListExports,
	// ListImports, ListContributorInsights and DescribeEndpoints have no
	// required input members.
}

// ddbRequire wraps a handler so that any required input member that is absent or
// JSON-null produces a ValidationException before the handler runs, matching the
// coral-framework message real DynamoDB returns. A malformed body is left for
// the handler itself to report. With no required members the handler is returned
// unwrapped.
func ddbRequire(required []string, h http.HandlerFunc) http.HandlerFunc {
	if len(required) == 0 {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, ddbMaxValidateBody+1))
		_ = r.Body.Close()
		if err != nil || int64(len(body)) > ddbMaxValidateBody {
			AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
			return
		}
		// Restore the body so the wrapped handler can read it again.
		r.Body = io.NopCloser(bytes.NewReader(body))

		var fields map[string]json.RawMessage
		if len(body) > 0 {
			if jerr := json.Unmarshal(body, &fields); jerr != nil {
				// Not valid JSON — let the handler surface its own body error.
				h(w, r)
				return
			}
		}
		for _, m := range required {
			raw, ok := fields[m]
			if !ok || string(bytes.TrimSpace(raw)) == "null" {
				AWSErrorf(w, "ValidationException", http.StatusBadRequest,
					"1 validation error detected: Value null at '%s' failed to satisfy constraint: Member must not be null",
					ddbWireMemberLabel(m))
				return
			}
		}
		h(w, r)
	}
}

// ddbMaxValidateBody bounds how much of a request body the required-member
// pre-check reads. It matches the router's own JSON body cap so the pre-check
// never rejects a body the handler would have accepted.
const ddbMaxValidateBody = 64 << 20 // matches the shared router's JSON body cap

// ddbWireMemberLabel renders a PascalCase Smithy member name as the
// lower-camelCase label DynamoDB's validation messages use (TableName →
// tableName, RequestItems → requestItems).
func ddbWireMemberLabel(member string) string {
	if member == "" {
		return member
	}
	return strings.ToLower(member[:1]) + member[1:]
}
