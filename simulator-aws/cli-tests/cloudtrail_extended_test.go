package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudTrailCLI_TrailLifecycle drives the trail control plane through the
// aws CLI: create-trail → get-trail / describe-trails / list-trails →
// start-logging / get-trail-status / stop-logging → update-trail →
// put/get-event-selectors → put/get-insight-selectors → add/list/remove-tags →
// delete-trail.
func TestCloudTrailCLI_TrailLifecycle(t *testing.T) {
	const bucket = "cli-ct-ext-bucket"
	const trail = "cli-ct-ext-trail"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() { _ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run() })
	t.Cleanup(func() { _ = awsCLI("cloudtrail", "delete-trail", "--name", trail).Run() })

	arn := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-trail",
		"--name", trail, "--s3-bucket-name", bucket,
		"--query", "TrailARN", "--output", "text")))
	if arn == "" {
		t.Fatal("create-trail returned no TrailARN")
	}

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-trail",
		"--name", trail, "--query", "Trail.TrailARN", "--output", "text")))
	if got != arn {
		t.Fatalf("get-trail ARN: got %q, want %q", got, arn)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "describe-trails",
		"--query", "length(trailList[?Name=='"+trail+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("describe-trails missing trail (got %q)", got)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-trails",
		"--query", "length(Trails[?Name=='"+trail+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("list-trails missing trail (got %q)", got)
	}

	// Logging on/off via get-trail-status.
	runCLI(t, awsCLI("cloudtrail", "start-logging", "--name", trail))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-trail-status",
		"--name", trail, "--query", "IsLogging", "--output", "text")))
	if got != "True" {
		t.Fatalf("after start-logging IsLogging: got %q, want True", got)
	}
	runCLI(t, awsCLI("cloudtrail", "stop-logging", "--name", trail))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-trail-status",
		"--name", trail, "--query", "IsLogging", "--output", "text")))
	if got != "False" {
		t.Fatalf("after stop-logging IsLogging: got %q, want False", got)
	}

	// update-trail changes the key prefix.
	runCLI(t, awsCLI("cloudtrail", "update-trail", "--name", trail, "--s3-key-prefix", "logs"))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-trail",
		"--name", trail, "--query", "Trail.S3KeyPrefix", "--output", "text")))
	if got != "logs" {
		t.Fatalf("update-trail S3KeyPrefix: got %q, want logs", got)
	}

	// Event selectors round-trip.
	runCLI(t, awsCLI("cloudtrail", "put-event-selectors", "--trail-name", trail,
		"--event-selectors", `[{"ReadWriteType":"All","IncludeManagementEvents":true}]`))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-event-selectors",
		"--trail-name", trail, "--query", "EventSelectors[0].ReadWriteType", "--output", "text")))
	if got != "All" {
		t.Fatalf("get-event-selectors ReadWriteType: got %q, want All", got)
	}

	// Insight selectors round-trip.
	runCLI(t, awsCLI("cloudtrail", "put-insight-selectors", "--trail-name", trail,
		"--insight-selectors", `[{"InsightType":"ApiCallRateInsight"}]`))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-insight-selectors",
		"--trail-name", trail, "--query", "InsightSelectors[0].InsightType", "--output", "text")))
	if got != "ApiCallRateInsight" {
		t.Fatalf("get-insight-selectors InsightType: got %q, want ApiCallRateInsight", got)
	}

	// Tags round-trip.
	runCLI(t, awsCLI("cloudtrail", "add-tags", "--resource-id", arn,
		"--tags-list", "Key=env,Value=test"))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-tags",
		"--resource-id-list", arn,
		"--query", "ResourceTagList[0].TagsList[0].Key", "--output", "text")))
	if got != "env" {
		t.Fatalf("list-tags Key: got %q, want env", got)
	}
	runCLI(t, awsCLI("cloudtrail", "remove-tags", "--resource-id", arn,
		"--tags-list", "Key=env,Value=test"))
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-tags",
		"--resource-id-list", arn,
		"--query", "length(ResourceTagList[0].TagsList)", "--output", "text")))
	if got != "0" {
		t.Fatalf("after remove-tags tag count: got %q, want 0", got)
	}
}

// TestCloudTrailCLI_ChannelLifecycle drives CloudTrail Lake channel CRUD through
// the aws CLI: create-channel → get-channel / list-channels → delete-channel.
func TestCloudTrailCLI_ChannelLifecycle(t *testing.T) {
	const name = "cli-ct-ext-channel"
	const eds = "arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/EXAMPLE-eds"

	arn := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-channel",
		"--name", name, "--source", "Custom",
		"--destinations", `[{"Type":"EVENT_DATA_STORE","Location":"`+eds+`"}]`,
		"--query", "ChannelArn", "--output", "text")))
	if arn == "" {
		t.Fatal("create-channel returned no ChannelArn")
	}
	t.Cleanup(func() { _ = awsCLI("cloudtrail", "delete-channel", "--channel", arn).Run() })

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "get-channel",
		"--channel", arn, "--query", "Name", "--output", "text")))
	if got != name {
		t.Fatalf("get-channel Name: got %q, want %q", got, name)
	}

	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "list-channels",
		"--query", "length(Channels[?ChannelArn=='"+arn+"'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("list-channels missing channel (got %q)", got)
	}

	runCLI(t, awsCLI("cloudtrail", "delete-channel", "--channel", arn))
	if err := awsCLI("cloudtrail", "get-channel", "--channel", arn).Run(); err == nil {
		t.Fatal("get-channel on a deleted channel must error")
	}
}
