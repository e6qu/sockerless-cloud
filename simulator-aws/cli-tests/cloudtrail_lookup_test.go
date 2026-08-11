package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudTrailLookupFilterKeysCLI verifies `cloudtrail lookup-events` honours
// the ResourceName / ResourceType / EventId / ReadOnly attribute keys, not only
// EventName.
func TestCloudTrailLookupFilterKeysCLI(t *testing.T) {
	const cluster = "cli-ct-filter-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	// EventId filter — capture the id via EventName, then look it up exactly.
	eventID := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=EventName,AttributeValue=CreateCluster",
		"--query", "Events[0].EventId", "--output", "text")))
	if eventID == "" {
		t.Fatal("CreateCluster event not recorded")
	}
	got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=EventId,AttributeValue="+eventID,
		"--query", "Events[0].EventName", "--output", "text")))
	if got != "CreateCluster" {
		t.Fatalf("EventId filter: got %q, want CreateCluster", got)
	}

	// ResourceName filter — the cluster the call acted on.
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=ResourceName,AttributeValue="+cluster,
		"--query", "Events[0].EventName", "--output", "text")))
	if got != "CreateCluster" {
		t.Fatalf("ResourceName filter: got %q, want CreateCluster", got)
	}

	// ResourceType filter — AWS::ECS::Cluster.
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=ResourceType,AttributeValue=AWS::ECS::Cluster",
		"--query", "length(Events[?EventName=='CreateCluster'])", "--output", "text")))
	if got == "0" || got == "" {
		t.Fatalf("ResourceType filter returned no CreateCluster event (got %q)", got)
	}

	// Negative: a non-matching ResourceName must return no CreateCluster event —
	// proves the filter is applied, not silently ignored.
	got = strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=ResourceName,AttributeValue=cli-no-such-resource",
		"--query", "length(Events[?EventName=='CreateCluster'])", "--output", "text")))
	if got != "0" {
		t.Fatalf("non-matching ResourceName must return 0 CreateCluster events, got %q", got)
	}
}

// TestCloudTrailRecordsSchedulerAPICallCLI verifies EventBridge Scheduler API
// calls are recorded in CloudTrail against scheduler.amazonaws.com.
func TestCloudTrailRecordsSchedulerAPICallCLI(t *testing.T) {
	const name = "cli-ct-schedule"
	runCLI(t, awsCLI("scheduler", "create-schedule",
		"--name", name,
		"--schedule-expression", "rate(1 hour)",
		"--flexible-time-window", `{"Mode":"OFF"}`,
		"--target", `{"Arn":"arn:aws:sqs:us-east-1:123456789012:cli-ct-q","RoleArn":"arn:aws:iam::123456789012:role/scheduler-role"}`,
	))
	t.Cleanup(func() { _ = awsCLI("scheduler", "delete-schedule", "--name", name).Run() })

	src := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=EventName,AttributeValue=CreateSchedule",
		"--query", "Events[0].EventSource", "--output", "text")))
	if src != "scheduler.amazonaws.com" {
		t.Fatalf("CreateSchedule EventSource: got %q, want scheduler.amazonaws.com", src)
	}
}

func TestCloudTrailRecordsRESTServiceAPICallsCLI(t *testing.T) {
	const bucket = "cli-ct-rest-bucket"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() { _ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run() })

	if out, err := awsCLI("lambda", "get-function", "--function-name", "missing-cli-ct-function").CombinedOutput(); err == nil {
		t.Fatalf("lambda get-function on a missing function should fail, got success:\n%s", out)
	}

	apiID := strings.TrimSpace(runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-ct-rest-api",
		"--protocol-type", "HTTP",
		"--query", "ApiId",
		"--output", "text")))
	if apiID == "" {
		t.Fatal("apigatewayv2 create-api returned empty ApiId")
	}
	t.Cleanup(func() { _ = awsCLI("apigatewayv2", "delete-api", "--api-id", apiID).Run() })

	runCLI(t, awsCLI("cloudwatch", "put-metric-data",
		"--namespace", "CLI/CT",
		"--metric-data", "MetricName=Recorded,Value=1",
	))

	check := func(source, name string) {
		t.Helper()
		got := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
			"--lookup-attributes", "AttributeKey=EventSource,AttributeValue="+source,
			"--query", "length(Events[?EventName=='"+name+"'])",
			"--output", "text")))
		if got == "0" || got == "" {
			t.Fatalf("CloudTrail EventSource %s did not include %s (got %q)", source, name, got)
		}
	}
	check("s3.amazonaws.com", "CreateBucket")
	check("lambda.amazonaws.com", "GetFunction")
	check("apigateway.amazonaws.com", "CreateApi")
	check("monitoring.amazonaws.com", "PutMetricData")

	record := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=EventName,AttributeValue=GetFunction",
		"--query", "Events[0].CloudTrailEvent",
		"--output", "text")))
	if !strings.Contains(record, "ResourceNotFoundException") || !strings.Contains(record, "missing-cli-ct-function") {
		t.Fatalf("failed Lambda REST event did not carry error details: %s", record)
	}
}

// TestCloudTrailLookupEventsPaginationCLI proves the lookup-events NextToken is
// a stable cursor: an event ingested mid-pagination must not make a previously
// returned EventId reappear on a later page (the old absolute-offset token
// overlapped pages on head-insertion).
func TestCloudTrailLookupEventsPaginationCLI(t *testing.T) {
	for i := 0; i < 6; i++ {
		runCLI(t, awsCLI("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "1"))
	}

	seen := map[string]int{}
	token := ""
	pages := 0
	for pages < 10 {
		args := []string{"cloudtrail", "lookup-events", "--no-paginate", "--max-results", "3", "--output", "json"}
		if token != "" {
			args = append(args, "--next-token", token)
		}
		var res struct {
			Events []struct {
				EventId string `json:"EventId"`
			} `json:"Events"`
			NextToken string `json:"NextToken"`
		}
		parseJSON(t, runCLI(t, awsCLI(args...)), &res)
		for _, e := range res.Events {
			seen[e.EventId]++
		}
		pages++
		if pages == 1 {
			runCLI(t, awsCLI("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "1"))
		}
		if res.NextToken == "" || len(res.Events) == 0 {
			break
		}
		token = res.NextToken
	}

	if pages < 2 {
		t.Fatalf("test must span multiple pages; got %d", pages)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("EventId %s appeared on %d pages; the cursor must return each event once", id, n)
		}
	}
}
