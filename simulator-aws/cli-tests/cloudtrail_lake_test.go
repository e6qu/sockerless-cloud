package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudTrailCLI_EventDataStoreLifecycle drives the CloudTrail Lake event
// data store control plane through the aws CLI: create-event-data-store →
// get-event-data-store / list-event-data-stores → update-event-data-store →
// stop/start-event-data-store-ingestion → enable/disable-federation →
// delete-event-data-store → restore-event-data-store.
func TestCloudTrailCLI_EventDataStoreLifecycle(t *testing.T) {
	const name = "cli-ct-eds"

	arn := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-event-data-store",
		"--name", name, "--retention-period", "90",
		"--query", "EventDataStoreArn", "--output", "text")))
	if arn == "" {
		t.Fatal("create-event-data-store returned no EventDataStoreArn")
	}
	t.Cleanup(func() {
		_ = awsCLI("cloudtrail", "delete-event-data-store", "--event-data-store", arn).Run()
	})

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-event-data-store",
		"--event-data-store", arn, "--query", "Status", "--output", "text")))
	if got != "ENABLED" {
		t.Fatalf("get-event-data-store Status: got %q, want ENABLED", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-event-data-stores",
		"--query", "length(EventDataStores[?EventDataStoreArn=='"+arn+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("list-event-data-stores missing store (got %q)", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "update-event-data-store",
		"--event-data-store", arn, "--retention-period", "120",
		"--query", "RetentionPeriod", "--output", "text")))
	if got != "120" {
		t.Fatalf("update-event-data-store RetentionPeriod: got %q, want 120", got)
	}

	runCLI(t, awsCLI("cloudtrail", "stop-event-data-store-ingestion", "--event-data-store", arn))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-event-data-store",
		"--event-data-store", arn, "--query", "Status", "--output", "text")))
	if got != "STOPPED_INGESTION" {
		t.Fatalf("after stop-ingestion Status: got %q, want STOPPED_INGESTION", got)
	}
	runCLI(t, awsCLI("cloudtrail", "start-event-data-store-ingestion", "--event-data-store", arn))

	runCLI(t, awsCLI("cloudtrail", "enable-federation", "--event-data-store", arn,
		"--federation-role-arn", "arn:aws:iam::123456789012:role/ct-federation"))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-event-data-store",
		"--event-data-store", arn, "--query", "FederationStatus", "--output", "text")))
	if got != "ENABLED" {
		t.Fatalf("after enable-federation FederationStatus: got %q, want ENABLED", got)
	}
	runCLI(t, awsCLI("cloudtrail", "disable-federation", "--event-data-store", arn))

	runCLI(t, awsCLI("cloudtrail", "delete-event-data-store", "--event-data-store", arn))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-event-data-store",
		"--event-data-store", arn, "--query", "Status", "--output", "text")))
	if got != "PENDING_DELETION" {
		t.Fatalf("after delete Status: got %q, want PENDING_DELETION", got)
	}
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "restore-event-data-store",
		"--event-data-store", arn, "--query", "Status", "--output", "text")))
	if got != "ENABLED" {
		t.Fatalf("after restore Status: got %q, want ENABLED", got)
	}
}

// TestCloudTrailCLI_LakeQuery drives the Lake query surface through the aws CLI:
// start-query → describe-query / get-query-results / list-queries →
// cancel-query, plus generate-query and search-sample-queries.
func TestCloudTrailCLI_LakeQuery(t *testing.T) {
	const eds = "arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/cli-lake-query"

	qid := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "start-query",
		"--query-statement", "SELECT eventName FROM "+eds+" LIMIT 10",
		"--query", "QueryId", "--output", "text")))
	if qid == "" {
		t.Fatal("start-query returned no QueryId")
	}

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "describe-query",
		"--query-id", qid, "--query", "QueryStatus", "--output", "text")))
	if got != "FINISHED" {
		t.Fatalf("describe-query QueryStatus: got %q, want FINISHED", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-query-results",
		"--query-id", qid, "--query", "QueryStatus", "--output", "text")))
	if got != "FINISHED" {
		t.Fatalf("get-query-results QueryStatus: got %q, want FINISHED", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-queries",
		"--event-data-store", eds,
		"--query", "length(Queries[?QueryId=='"+qid+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("list-queries missing query (got %q)", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "cancel-query",
		"--query-id", qid, "--query", "QueryStatus", "--output", "text")))
	if got != "CANCELLED" {
		t.Fatalf("cancel-query QueryStatus: got %q, want CANCELLED", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "generate-query",
		"--event-data-stores", eds, "--prompt", "list recent events",
		"--query", "QueryStatement", "--output", "text")))
	if got == "" {
		t.Fatal("generate-query returned no QueryStatement")
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "search-sample-queries",
		"--search-phrase", "CreateBucket",
		"--query", "length(SearchResults)", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("search-sample-queries returned no results (got %q)", got)
	}
}

// TestCloudTrailCLI_DashboardAndImport drives the dashboard and import surfaces:
// create-dashboard → update-dashboard → start-dashboard-refresh →
// delete-dashboard; start-import → get-import → list-imports /
// list-import-failures → stop-import.
func TestCloudTrailCLI_DashboardAndImport(t *testing.T) {
	const dash = "cli-ct-dashboard"
	arn := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-dashboard",
		"--name", dash,
		"--widgets", `[{"QueryStatement":"SELECT eventName FROM $EDS_ID LIMIT 10","ViewProperties":{"type":"table"}}]`,
		"--query", "DashboardArn", "--output", "text")))
	if arn == "" {
		t.Fatal("create-dashboard returned no DashboardArn")
	}
	t.Cleanup(func() { _ = awsCLI("cloudtrail", "delete-dashboard", "--dashboard-id", dash).Run() })

	runCLI(t, awsCLI("cloudtrail", "update-dashboard", "--dashboard-id", arn,
		"--widgets", `[{"QueryStatement":"SELECT eventSource FROM $EDS_ID LIMIT 5","ViewProperties":{"type":"table"}}]`))

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-dashboard",
		"--dashboard-id", arn, "--query", "DashboardArn", "--output", "text")))
	if got != arn {
		t.Fatalf("get-dashboard DashboardArn: got %q, want %q", got, arn)
	}
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-dashboards",
		"--query", "length(Dashboards[?DashboardArn=='"+arn+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("list-dashboards missing dashboard (got %q)", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "start-dashboard-refresh",
		"--dashboard-id", arn, "--query", "RefreshId", "--output", "text")))
	if got == "" {
		t.Fatal("start-dashboard-refresh returned no RefreshId")
	}
	runCLI(t, awsCLI("cloudtrail", "delete-dashboard", "--dashboard-id", dash))

	const eds = "arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/cli-import-eds"
	importID := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "start-import",
		"--destinations", eds,
		"--import-source", `{"S3":{"S3LocationUri":"s3://cli-ct-import/AWSLogs/","S3BucketRegion":"us-east-1","S3BucketAccessRoleArn":"arn:aws:iam::123456789012:role/ct-import"}}`,
		"--query", "ImportId", "--output", "text")))
	if importID == "" {
		t.Fatal("start-import returned no ImportId")
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-import",
		"--import-id", importID, "--query", "ImportId", "--output", "text")))
	if got != importID {
		t.Fatalf("get-import ImportId: got %q, want %q", got, importID)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-imports",
		"--query", "length(Imports[?ImportId=='"+importID+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("list-imports missing import (got %q)", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-import-failures",
		"--import-id", importID, "--query", "length(Failures)", "--output", "text")))
	if got != "0" {
		t.Fatalf("list-import-failures count: got %q, want 0", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "stop-import",
		"--import-id", importID, "--query", "ImportStatus", "--output", "text")))
	if got != "STOPPED" {
		t.Fatalf("stop-import ImportStatus: got %q, want STOPPED", got)
	}
}

// TestCloudTrailCLI_Governance drives the resource-policy, org delegated-admin,
// event-configuration, public-keys, insight-metric, and update-channel surfaces.
func TestCloudTrailCLI_Governance(t *testing.T) {
	const channelArn = "arn:aws:cloudtrail:us-east-1:123456789012:channel/cli-gov-channel"
	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},"Action":"cloudtrail:GetChannel","Resource":"*"}]}`

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "put-resource-policy",
		"--resource-arn", channelArn, "--resource-policy", policy,
		"--query", "ResourceArn", "--output", "text")))
	if got != channelArn {
		t.Fatalf("put-resource-policy ResourceArn: got %q, want %q", got, channelArn)
	}
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-resource-policy",
		"--resource-arn", channelArn, "--query", "ResourceArn", "--output", "text")))
	if got != channelArn {
		t.Fatalf("get-resource-policy ResourceArn: got %q, want %q", got, channelArn)
	}
	runCLI(t, awsCLI("cloudtrail", "delete-resource-policy", "--resource-arn", channelArn))
	if err := awsCLI("cloudtrail", "get-resource-policy", "--resource-arn", channelArn).Run(); err == nil {
		t.Fatal("get-resource-policy after delete must error")
	}

	runCLI(t, awsCLI("cloudtrail", "register-organization-delegated-admin",
		"--member-account-id", "210987654321"))
	runCLI(t, awsCLI("cloudtrail", "deregister-organization-delegated-admin",
		"--delegated-admin-account-id", "210987654321"))

	const edsName = "cli-ct-gov-eds"
	edsArn := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-event-data-store",
		"--name", edsName, "--query", "EventDataStoreArn", "--output", "text")))
	if edsArn == "" {
		t.Fatal("create-event-data-store returned no ARN")
	}
	t.Cleanup(func() {
		_ = awsCLI("cloudtrail", "delete-event-data-store", "--event-data-store", edsArn).Run()
	})

	// PutEventConfiguration / GetEventConfiguration are exercised via the SDK
	// test (TestCloudTrail_GovernanceSDK); their CLI subcommands postdate the
	// aws CLI pinned in this environment, so they are not driven here.

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-public-keys",
		"--query", "length(PublicKeyList)", "--output", "text")))
	if got != "0" {
		t.Fatalf("list-public-keys count: got %q, want 0", got)
	}

	// list-insights-metric-data returns a real (empty) series.
	runCLI(t, awsCLI("cloudtrail", "list-insights-metric-data",
		"--event-name", "PutBucketPolicy", "--event-source", "s3.amazonaws.com",
		"--insight-type", "ApiCallRateInsight"))

	// update-channel renames a Lake channel.
	chArn := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-channel",
		"--name", "cli-gov-update-channel", "--source", "Custom",
		"--destinations", `[{"Type":"EVENT_DATA_STORE","Location":"`+edsArn+`"}]`,
		"--query", "ChannelArn", "--output", "text")))
	if chArn == "" {
		t.Fatal("create-channel returned no ChannelArn")
	}
	t.Cleanup(func() { _ = awsCLI("cloudtrail", "delete-channel", "--channel", chArn).Run() })
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "update-channel",
		"--channel", chArn, "--name", "cli-gov-update-channel-renamed",
		"--query", "Name", "--output", "text")))
	if got != "cli-gov-update-channel-renamed" {
		t.Fatalf("update-channel Name: got %q, want renamed", got)
	}
}
