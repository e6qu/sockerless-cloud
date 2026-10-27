package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// DynamoDB rejects a request missing an @required member with a
// ValidationException before any business logic, so a missing TableName is
// never a ResourceNotFoundException. TestDDBRequiredMembersMatchSpec holds
// this table to the vendored Smithy model.
var ddbRequiredMembers = map[string][]string{
	"CreateTable":                         {"TableName"},
	"DescribeTable":                       {"TableName"},
	"UpdateTable":                         {"TableName"},
	"SearchVectors":                       {"IndexName", "SearchVector", "TableName", "TopK"},
	"DeleteTable":                         {"TableName"},
	"PutItem":                             {"TableName", "Item"},
	"GetItem":                             {"TableName", "Key"},
	"UpdateItem":                          {"TableName", "Key"},
	"DeleteItem":                          {"TableName", "Key"},
	"Query":                               {"TableName"},
	"Scan":                                {"TableName"},
	"BatchWriteItem":                      {"RequestItems"},
	"BatchGetItem":                        {"RequestItems"},
	"TransactWriteItems":                  {"TransactItems"},
	"TransactGetItems":                    {"TransactItems"},
	"DescribeContinuousBackups":           {"TableName"},
	"UpdateContinuousBackups":             {"TableName", "PointInTimeRecoverySpecification"},
	"DescribeTimeToLive":                  {"TableName"},
	"UpdateTimeToLive":                    {"TableName", "TimeToLiveSpecification"},
	"ListTagsOfResource":                  {"ResourceArn"},
	"TagResource":                         {"ResourceArn", "Tags"},
	"UntagResource":                       {"ResourceArn", "TagKeys"},
	"ExecuteStatement":                    {"Statement"},
	"BatchExecuteStatement":               {"Statements"},
	"ExecuteTransaction":                  {"TransactStatements"},
	"CreateBackup":                        {"BackupName", "TableName"},
	"DescribeBackup":                      {"BackupArn"},
	"DeleteBackup":                        {"BackupArn"},
	"RestoreTableFromBackup":              {"BackupArn", "TargetTableName"},
	"RestoreTableToPointInTime":           {"TargetTableName"},
	"CreateGlobalTable":                   {"GlobalTableName", "ReplicationGroup"},
	"DescribeGlobalTable":                 {"GlobalTableName"},
	"UpdateGlobalTable":                   {"GlobalTableName", "ReplicaUpdates"},
	"DescribeGlobalTableSettings":         {"GlobalTableName"},
	"UpdateGlobalTableSettings":           {"GlobalTableName"},
	"UpdateTableReplicaAutoScaling":       {"TableName"},
	"DescribeTableReplicaAutoScaling":     {"TableName"},
	"PutResourcePolicy":                   {"Policy", "ResourceArn"},
	"GetResourcePolicy":                   {"ResourceArn"},
	"DeleteResourcePolicy":                {"ResourceArn"},
	"EnableKinesisStreamingDestination":   {"StreamArn", "TableName"},
	"DisableKinesisStreamingDestination":  {"StreamArn", "TableName"},
	"DescribeKinesisStreamingDestination": {"TableName"},
	"UpdateKinesisStreamingDestination":   {"StreamArn", "TableName"},
	"ExportTableToPointInTime":            {"S3Bucket", "TableArn"},
	"DescribeExport":                      {"ExportArn"},
	"ImportTable":                         {"InputFormat", "S3BucketSource", "TableCreationParameters"},
	"DescribeImport":                      {"ImportArn"},
	"UpdateContributorInsights":           {"ContributorInsightsAction", "TableName"},
	"DescribeContributorInsights":         {"TableName"},
	// ListTables, DescribeLimits, ListBackups, ListGlobalTables, ListExports,
	// ListImports, ListContributorInsights and DescribeEndpoints require nothing.
}

// An absent or JSON-null required member gets DynamoDB's validation message
// before the handler runs. A malformed body is left to the handler.
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
		r.Body = io.NopCloser(bytes.NewReader(body))

		var fields map[string]json.RawMessage
		if len(body) > 0 {
			if jerr := json.Unmarshal(body, &fields); jerr != nil {
				// Let the handler report invalid JSON.
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

// The router's own JSON body cap, so the pre-check never rejects a body the
// handler would accept.
const ddbMaxValidateBody = 64 << 20

// TableName → tableName, as DynamoDB's validation messages spell it.
func ddbWireMemberLabel(member string) string {
	if member == "" {
		return member
	}
	return strings.ToLower(member[:1]) + member[1:]
}
