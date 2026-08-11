package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogs_MetricFilterCLI exercises put/describe/test/delete-metric-filter.
func TestLogs_MetricFilterCLI(t *testing.T) {
	group := "/cli/metric-filter"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	runCLI(t, awsCLI("logs", "put-metric-filter",
		"--log-group-name", group,
		"--filter-name", "errors",
		"--filter-pattern", "ERROR",
		"--metric-transformations",
		"metricName=ErrorCount,metricNamespace=MyApp,metricValue=1,defaultValue=0",
	))

	out := runCLI(t, awsCLI("logs", "describe-metric-filters",
		"--log-group-name", group, "--output", "json"))
	var desc struct {
		MetricFilters []struct {
			FilterName            string `json:"filterName"`
			FilterPattern         string `json:"filterPattern"`
			MetricTransformations []struct {
				MetricName string `json:"metricName"`
			} `json:"metricTransformations"`
		} `json:"metricFilters"`
	}
	parseJSON(t, out, &desc)
	require.Len(t, desc.MetricFilters, 1)
	assert.Equal(t, "errors", desc.MetricFilters[0].FilterName)
	require.Len(t, desc.MetricFilters[0].MetricTransformations, 1)
	assert.Equal(t, "ErrorCount", desc.MetricFilters[0].MetricTransformations[0].MetricName)

	test := runCLI(t, awsCLI("logs", "test-metric-filter",
		"--filter-pattern", "ERROR",
		"--log-event-messages", "ERROR one", "fine", "ERROR two",
		"--output", "json"))
	var tres struct {
		Matches []struct {
			EventNumber int64 `json:"eventNumber"`
		} `json:"matches"`
	}
	parseJSON(t, test, &tres)
	assert.Len(t, tres.Matches, 2)

	runCLI(t, awsCLI("logs", "delete-metric-filter",
		"--log-group-name", group, "--filter-name", "errors"))
}

// TestLogs_SubscriptionFilterCLI exercises put/describe/delete-subscription-filter.
func TestLogs_SubscriptionFilterCLI(t *testing.T) {
	group := "/cli/sub-filter"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	destArn := "arn:aws:lambda:us-east-1:123456789012:function:sink"
	runCLI(t, awsCLI("logs", "put-subscription-filter",
		"--log-group-name", group,
		"--filter-name", "to-lambda",
		"--filter-pattern", "",
		"--destination-arn", destArn,
	))

	out := runCLI(t, awsCLI("logs", "describe-subscription-filters",
		"--log-group-name", group, "--output", "json"))
	var desc struct {
		SubscriptionFilters []struct {
			FilterName     string `json:"filterName"`
			DestinationArn string `json:"destinationArn"`
		} `json:"subscriptionFilters"`
	}
	parseJSON(t, out, &desc)
	require.Len(t, desc.SubscriptionFilters, 1)
	assert.Equal(t, "to-lambda", desc.SubscriptionFilters[0].FilterName)
	assert.Equal(t, destArn, desc.SubscriptionFilters[0].DestinationArn)

	runCLI(t, awsCLI("logs", "delete-subscription-filter",
		"--log-group-name", group, "--filter-name", "to-lambda"))
}

// TestLogs_RetentionAndTagsCLI exercises put/delete-retention-policy and the
// tag-log-group / list-tags-log-group / untag-log-group verbs, plus delete-log-stream.
func TestLogs_RetentionAndTagsCLI(t *testing.T) {
	group := "/cli/retention"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	runCLI(t, awsCLI("logs", "put-retention-policy",
		"--log-group-name", group, "--retention-in-days", "14"))
	out := runCLI(t, awsCLI("logs", "describe-log-groups",
		"--log-group-name-prefix", group, "--output", "json"))
	var g struct {
		LogGroups []struct {
			RetentionInDays int `json:"retentionInDays"`
		} `json:"logGroups"`
	}
	parseJSON(t, out, &g)
	require.Len(t, g.LogGroups, 1)
	assert.Equal(t, 14, g.LogGroups[0].RetentionInDays)

	runCLI(t, awsCLI("logs", "delete-retention-policy", "--log-group-name", group))

	runCLI(t, awsCLI("logs", "tag-log-group",
		"--log-group-name", group, "--tags", "env=test,team=infra"))
	lt := runCLI(t, awsCLI("logs", "list-tags-log-group",
		"--log-group-name", group, "--output", "json"))
	var tags struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, lt, &tags)
	assert.Equal(t, "test", tags.Tags["env"])

	runCLI(t, awsCLI("logs", "untag-log-group",
		"--log-group-name", group, "--tags", "team"))

	// delete-log-stream round-trip.
	runCLI(t, awsCLI("logs", "create-log-stream",
		"--log-group-name", group, "--log-stream-name", "s1"))
	runCLI(t, awsCLI("logs", "delete-log-stream",
		"--log-group-name", group, "--log-stream-name", "s1"))
}

// TestLogs_ExportTaskCLI exercises create/describe/cancel-export-task.
func TestLogs_ExportTaskCLI(t *testing.T) {
	group := "/cli/export-task"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	out := runCLI(t, awsCLI("logs", "create-export-task",
		"--log-group-name", group,
		"--from", "1000", "--to", "2000",
		"--destination", "export-bucket",
		"--output", "json"))
	var created struct {
		TaskId string `json:"taskId"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.TaskId)

	desc := runCLI(t, awsCLI("logs", "describe-export-tasks",
		"--task-id", created.TaskId, "--output", "json"))
	var dres struct {
		ExportTasks []struct {
			LogGroupName string `json:"logGroupName"`
			Destination  string `json:"destination"`
			Status       struct {
				Code string `json:"code"`
			} `json:"status"`
		} `json:"exportTasks"`
	}
	parseJSON(t, desc, &dres)
	require.Len(t, dres.ExportTasks, 1)
	assert.Equal(t, group, dres.ExportTasks[0].LogGroupName)
	assert.Equal(t, "COMPLETED", dres.ExportTasks[0].Status.Code)

	// Completed tasks cannot be cancelled; tolerate the error.
	runCLIIgnore(awsCLI("logs", "cancel-export-task", "--task-id", created.TaskId))
}

// TestLogs_DataProtectionPolicyCLI exercises put/get/delete-data-protection-policy.
func TestLogs_DataProtectionPolicyCLI(t *testing.T) {
	group := "/cli/data-protection"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	doc := `{"Name":"policy","Version":"2021-06-01","Statement":[{"Sid":"audit","DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Audit":{"FindingsDestination":{}}}}]}`
	put := runCLI(t, awsCLI("logs", "put-data-protection-policy",
		"--log-group-identifier", group,
		"--policy-document", doc,
		"--output", "json"))
	var pres struct {
		PolicyDocument string `json:"policyDocument"`
	}
	parseJSON(t, put, &pres)
	assert.Equal(t, doc, pres.PolicyDocument)

	get := runCLI(t, awsCLI("logs", "get-data-protection-policy",
		"--log-group-identifier", group, "--output", "json"))
	var gres struct {
		PolicyDocument     string `json:"policyDocument"`
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	parseJSON(t, get, &gres)
	assert.Equal(t, doc, gres.PolicyDocument)

	runCLI(t, awsCLI("logs", "delete-data-protection-policy",
		"--log-group-identifier", group))
}

// TestLogs_PutMetricFilterCLIRejectsInvalidPattern verifies that an invalid
// filterPattern is rejected at put time, not silently stored.
func TestLogs_PutMetricFilterCLIRejectsInvalidPattern(t *testing.T) {
	group := "/cli/metric-filter-invalid"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	out := runCLIExpectError(t, awsCLI("logs", "put-metric-filter",
		"--log-group-name", group,
		"--filter-name", "bad-filter",
		"--filter-pattern", "{",
		"--metric-transformations",
		"metricName=Bad,metricNamespace=Probe,metricValue=1",
	))
	if !strings.Contains(out, "InvalidParameterException") {
		t.Fatalf("put-metric-filter invalid pattern error = %q, want InvalidParameterException", out)
	}
}

// TestLogs_PutSubscriptionFilterCLIRejectsInvalidPattern verifies the same
// validation on put-subscription-filter.
func TestLogs_PutSubscriptionFilterCLIRejectsInvalidPattern(t *testing.T) {
	group := "/cli/subscription-filter-invalid"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	out := runCLIExpectError(t, awsCLI("logs", "put-subscription-filter",
		"--log-group-name", group,
		"--filter-name", "bad-sub",
		"--filter-pattern", "{",
		"--destination-arn", "arn:aws:lambda:us-east-1:123456789012:function:sink",
	))
	if !strings.Contains(out, "InvalidParameterException") {
		t.Fatalf("put-subscription-filter invalid pattern error = %q, want InvalidParameterException", out)
	}
}
