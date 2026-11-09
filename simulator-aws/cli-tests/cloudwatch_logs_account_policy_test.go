package aws_cli_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The log classes guide: read paths answer ESSENTIALS for the Infrequent
// Access class from December 2026, and INFREQUENT_ACCESS before.
func essentialsReadClassCLI() string {
	if time.Now().UTC().Before(time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)) {
		return "INFREQUENT_ACCESS"
	}
	return "ESSENTIALS"
}

func putCLILogLines(t *testing.T, group, stream string, lines ...string) {
	t.Helper()
	now := json.Number(strconv.FormatInt(time.Now().UnixMilli(), 10))
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		events = append(events, map[string]any{"timestamp": now, "message": line})
	}
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream, "--log-events", string(encoded)))
}

// The account-level guide's Amazon Kinesis Data Streams example:
// put-account-policy with a SUBSCRIPTION_FILTER_POLICY document and a
// LogGroupName NOT IN selection, then get-records, whose records carry
// policyLevel ACCOUNT_LEVEL_POLICY and the policy's name as the filter.
func TestLogs_AccountSubscriptionPolicyCLI(t *testing.T) {
	prefix := uniquename.New("/cli/account-subscription")
	included, excluded := prefix+"/Example1", prefix+"/LogGroupToExclude1"
	for _, group := range []string{included, excluded} {
		runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
		t.Cleanup(func() { runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group)) })
		runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "logStream1"))
	}
	stream := uniquename.New("cli-TestStream")
	streamARN := createCLIActiveStream(t, stream)
	role := createCLILogsDeliveryRole(t, uniquename.New("cli-CWLtoKinesisRole"), "kinesis:PutRecord", streamARN)
	policy := uniquename.New("ExamplePolicy")
	runCLI(t, awsCLI("logs", "put-account-policy",
		"--policy-name", policy,
		"--policy-type", "SUBSCRIPTION_FILTER_POLICY",
		"--policy-document", `{"RoleArn":"`+role+`", "DestinationArn":"`+streamARN+`", "FilterPattern": "Test", "Distribution": "Random"}`,
		"--selection-criteria", `LogGroupName NOT IN ["`+excluded+`"]`,
		"--scope", "ALL"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("logs", "delete-account-policy", "--policy-name", policy, "--policy-type", "SUBSCRIPTION_FILTER_POLICY"))
	})
	var described struct {
		AccountPolicies []struct {
			PolicyName        string `json:"policyName"`
			SelectionCriteria string `json:"selectionCriteria"`
			Scope             string `json:"scope"`
		} `json:"accountPolicies"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-account-policies", "--policy-type", "SUBSCRIPTION_FILTER_POLICY")), &described)
	require.Len(t, described.AccountPolicies, 1)
	assert.Equal(t, policy, described.AccountPolicies[0].PolicyName)
	assert.Equal(t, "ALL", described.AccountPolicies[0].Scope)

	refused := runCLIExpectError(t, awsCLI("logs", "put-account-policy", "--policy-name", policy+"-second",
		"--policy-type", "SUBSCRIPTION_FILTER_POLICY", "--policy-document", `{"DestinationArn":"`+streamARN+`","RoleArn":"`+role+`"}`))
	assert.Contains(t, refused, "LimitExceededException")

	putCLILogLines(t, included, "logStream1", "Simple Test")
	delivered := awaitCLIStreamMessages(t, stream, 1)
	m := delivered[0]
	assert.Equal(t, included, m.LogGroup)
	assert.Equal(t, []string{policy}, m.SubscriptionFilters)
	assert.Equal(t, "ACCOUNT_LEVEL_POLICY", m.PolicyLevel)
	require.Len(t, m.LogEvents, 1)
	assert.Equal(t, "Simple Test", m.LogEvents[0].Message)
}

// The API reference's transformer policy example, and a retention policy's
// dry run, which reports the impact without saving the policy.
func TestLogs_AccountTransformerAndRetentionPoliciesCLI(t *testing.T) {
	prefix := uniquename.New("/cli/account-policies")
	group := prefix + "/test-app"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group)) })
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "s"))

	policy := uniquename.New("ExamplePolicy")
	runCLI(t, awsCLI("logs", "put-account-policy", "--policy-name", policy, "--policy-type", "TRANSFORMER_POLICY",
		"--policy-document", `[{"parseJSON":{}},{"addKeys":{"entries":[{"key":"metadata.transformed_in","value":"CloudWatchLogs"}]}}]`,
		"--selection-criteria", `LogGroupNamePrefix= "`+prefix+`"`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("logs", "delete-account-policy", "--policy-name", policy, "--policy-type", "TRANSFORMER_POLICY"))
	})
	refused := runCLIExpectError(t, awsCLI("logs", "put-account-policy", "--policy-name", policy+"-nested",
		"--policy-type", "TRANSFORMER_POLICY", "--policy-document", `[{"parseJSON":{}}]`,
		"--selection-criteria", `LogGroupNamePrefix= "`+group+`"`))
	assert.Contains(t, refused, "InvalidParameterException")

	start := time.Now()
	putCLILogLines(t, group, "s", `{"status":"ok"}`)
	var query struct {
		QueryID string `json:"queryId"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "start-query", "--log-group-name", group, "--query-string", "fields @message",
		"--start-time", strconv.FormatInt(start.Add(-time.Minute).Unix(), 10),
		"--end-time", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))), &query)
	var results struct {
		Results [][]struct {
			Field string `json:"field"`
			Value string `json:"value"`
		} `json:"results"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "get-query-results", "--query-id", query.QueryID)), &results)
	require.Len(t, results.Results, 1)
	for _, f := range results.Results[0] {
		if f.Field == "@message" {
			assert.JSONEq(t, `{"status":"ok","metadata":{"transformed_in":"CloudWatchLogs"}}`, f.Value)
		}
	}

	var preview struct {
		DryRunResult struct {
			TotalMatchedLogGroups           int `json:"totalMatchedLogGroups"`
			DecreasedRetentionLogGroupCount int `json:"decreasedRetentionLogGroupCount"`
			TopLogGroupsImpacted            []struct {
				LogGroupName      string `json:"logGroupName"`
				CurrentRetention  string `json:"currentRetention"`
				ProposedRetention string `json:"proposedRetention"`
			} `json:"topLogGroupsImpacted"`
		} `json:"dryRunResult"`
		AccountPolicy *json.RawMessage `json:"accountPolicy"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "put-account-policy", "--policy-name", uniquename.New("retention"),
		"--policy-type", "RETENTION_POLICY", "--policy-document", `{"retentionInDays": 90, "enforcementMode": "RETROACTIVE"}`,
		"--selection-criteria", `LogGroupNamePrefix="`+prefix+`"`, "--dry-run")), &preview)
	assert.Nil(t, preview.AccountPolicy)
	assert.Equal(t, 1, preview.DryRunResult.TotalMatchedLogGroups)
	assert.Equal(t, 1, preview.DryRunResult.DecreasedRetentionLogGroupCount)
	require.Len(t, preview.DryRunResult.TopLogGroupsImpacted, 1)
	assert.Equal(t, "NEVER_EXPIRE", preview.DryRunResult.TopLogGroupsImpacted[0].CurrentRetention)
	assert.Equal(t, "90", preview.DryRunResult.TopLogGroupsImpacted[0].ProposedRetention)
}

// An Essentials group ingests but takes no metric filter and answers no
// get-log-events; a Delivery group keeps two days, which put-retention-policy
// cannot change; filter-log-events answers each event's one ID.
func TestLogs_LogClassesAndEventIDsCLI(t *testing.T) {
	t.Parallel()
	prefix := uniquename.New("/cli/classes")
	essentials, deliveryGroup, standard := prefix+"/essentials", prefix+"/delivery", prefix+"/standard"
	for group, class := range map[string]string{essentials: "ESSENTIALS", deliveryGroup: "DELIVERY", standard: "STANDARD"} {
		runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group, "--log-group-class", class))
		t.Cleanup(func() { runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group)) })
		runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "s"))
	}
	var described struct {
		LogGroups []struct {
			LogGroupName    string `json:"logGroupName"`
			LogGroupClass   string `json:"logGroupClass"`
			RetentionInDays int    `json:"retentionInDays"`
		} `json:"logGroups"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-log-groups", "--log-group-name-prefix", prefix)), &described)
	require.Len(t, described.LogGroups, 3)
	for _, g := range described.LogGroups {
		switch g.LogGroupName {
		case essentials:
			assert.Equal(t, essentialsReadClassCLI(), g.LogGroupClass)
		case deliveryGroup:
			assert.Equal(t, "DELIVERY", g.LogGroupClass)
			assert.Equal(t, 2, g.RetentionInDays)
		}
	}
	assert.Contains(t, runCLIExpectError(t, awsCLI("logs", "put-retention-policy", "--log-group-name", deliveryGroup,
		"--retention-in-days", "30")), "InvalidParameterException")

	putCLILogLines(t, essentials, "s", "ingested")
	assert.Contains(t, runCLIExpectError(t, awsCLI("logs", "get-log-events", "--log-group-name", essentials,
		"--log-stream-name", "s")), "InvalidParameterException")
	assert.Contains(t, runCLIExpectError(t, awsCLI("logs", "put-metric-filter", "--log-group-name", essentials,
		"--filter-name", "f", "--filter-pattern", "", "--metric-transformations",
		"metricName=M,metricNamespace=Test/LogClasses,metricValue=1")), "InvalidOperationException")

	putCLILogLines(t, standard, "s", "first", "second")
	var filtered struct {
		Events []struct {
			EventID string `json:"eventId"`
		} `json:"events"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "filter-log-events", "--log-group-name", standard)), &filtered)
	require.Len(t, filtered.Events, 2)
	first := []string{filtered.Events[0].EventID, filtered.Events[1].EventID}
	parseJSON(t, runCLI(t, awsCLI("logs", "filter-log-events", "--log-group-name", standard)), &filtered)
	assert.Equal(t, first, []string{filtered.Events[0].EventID, filtered.Events[1].EventID})
	assert.Len(t, first[0], 56)
	assert.NotEqual(t, first[0], first[1])
}

// test-transformer with a substituteString whose from uses Java's
// look-behind and backreferences.
func TestLogs_SubstituteStringJavaPatternCLI(t *testing.T) {
	t.Parallel()
	var out struct {
		TransformedLogs []struct {
			TransformedEventMessage string `json:"transformedEventMessage"`
		} `json:"transformedLogs"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "test-transformer",
		"--transformer-config", `[{"parseJSON":{}},{"substituteString":{"entries":[`+
			`{"source":"price","from":"(?<=\\$)\\d+","to":"N"},{"source":"doubled","from":"(\\w)\\1","to":"<$1>"}]}}]`,
		"--log-event-messages", `{"price":"cost $15 and 20","doubled":"aabbcd"}`)), &out)
	require.Len(t, out.TransformedLogs, 1)
	assert.JSONEq(t, `{"price":"cost $N and 20","doubled":"<a><b>cd"}`, out.TransformedLogs[0].TransformedEventMessage)
}
