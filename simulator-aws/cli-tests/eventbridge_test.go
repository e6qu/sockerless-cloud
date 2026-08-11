package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertEventBridgeCLIEnvelope(t *testing.T, body, source, detailType string, detail map[string]any) {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &event))
	assert.Equal(t, source, event["source"])
	assert.Equal(t, detailType, event["detail-type"])
	assert.Equal(t, detail, event["detail"])
	assert.NotEmpty(t, event["id"])
	assert.NotEmpty(t, event["time"])
}

func TestEventBridgeCLI_RuleTargetPutEvents(t *testing.T) {
	runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "eb-cli-q"))
	t.Cleanup(func() {
		out := runCLI(t, awsCLI("sqs", "get-queue-url", "--queue-name", "eb-cli-q"))
		var q struct {
			QueueUrl string `json:"QueueUrl"`
		}
		parseJSON(t, out, &q)
		runCLI(t, awsCLI("sqs", "delete-queue", "--queue-url", q.QueueUrl))
	})

	out := runCLI(t, awsCLI("sqs", "get-queue-url", "--queue-name", "eb-cli-q"))
	var q struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &q)
	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", q.QueueUrl,
		"--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	ruleOut := runCLI(t, awsCLI("events", "put-rule",
		"--name", "eb-cli-rule",
		"--event-pattern", `{"source":["sockerless.cli"]}`))
	var rule struct {
		RuleArn string `json:"RuleArn"`
	}
	parseJSON(t, ruleOut, &rule)
	t.Cleanup(func() {
		runCLI(t, awsCLI("events", "remove-targets", "--rule", "eb-cli-rule", "--ids", "queue"))
		runCLI(t, awsCLI("events", "delete-rule", "--name", "eb-cli-rule"))
	})

	runCLI(t, awsCLI("events", "put-targets",
		"--rule", "eb-cli-rule",
		"--targets", `[{"Id":"queue","Arn":"`+queueARN+`"}]`))
	// EventBridge → SQS delivery is authorized by the queue's resource policy.
	setEBQueuePolicyCLI(t, q.QueueUrl, queueARN, rule.RuleArn)

	out = runCLI(t, awsCLI("events", "list-targets-by-rule", "--rule", "eb-cli-rule"))
	var targets struct {
		Targets []struct {
			ID  string `json:"Id"`
			Arn string `json:"Arn"`
		} `json:"Targets"`
	}
	parseJSON(t, out, &targets)
	require.Len(t, targets.Targets, 1)
	assert.Equal(t, queueARN, targets.Targets[0].Arn)

	entries, err := json.Marshal([]map[string]string{{
		"Source":     "sockerless.cli",
		"DetailType": "example",
		"Detail":     `{"cli":true}`,
	}})
	require.NoError(t, err)
	runCLI(t, awsCLI("events", "put-events", "--entries", string(entries)))

	out = runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", q.QueueUrl))
	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 1)
	assertEventBridgeCLIEnvelope(t, recv.Messages[0].Body, "sockerless.cli", "example",
		map[string]any{"cli": true})
}

func TestEventBridgeCLI_BusArchiveReplay(t *testing.T) {
	out := runCLI(t, awsCLI("events", "create-event-bus",
		"--name", "eb-cli-bus",
		"--description", "cli bus"))
	var bus struct {
		EventBusArn string `json:"EventBusArn"`
	}
	parseJSON(t, out, &bus)
	require.NotEmpty(t, bus.EventBusArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("events", "delete-archive", "--archive-name", "eb-cli-archive"))
		runCLI(t, awsCLI("events", "delete-event-bus", "--name", "eb-cli-bus"))
	})

	out = runCLI(t, awsCLI("events", "describe-event-bus", "--name", "eb-cli-bus"))
	var described struct {
		Name string `json:"Name"`
		Arn  string `json:"Arn"`
	}
	parseJSON(t, out, &described)
	assert.Equal(t, "eb-cli-bus", described.Name)
	assert.Equal(t, bus.EventBusArn, described.Arn)

	out = runCLI(t, awsCLI("events", "list-event-buses", "--name-prefix", "eb-cli-bus"))
	var buses struct {
		EventBuses []struct {
			Name string `json:"Name"`
		} `json:"EventBuses"`
	}
	parseJSON(t, out, &buses)
	require.NotEmpty(t, buses.EventBuses)
	var found bool
	for _, b := range buses.EventBuses {
		if b.Name == "eb-cli-bus" {
			found = true
		}
	}
	require.True(t, found, "list-event-buses should include eb-cli-bus")

	runCLI(t, awsCLI("events", "put-permission",
		"--event-bus-name", "eb-cli-bus",
		"--statement-id", "cli-permission",
		"--action", "events:PutEvents",
		"--principal", "123456789012"))
	out = runCLI(t, awsCLI("events", "describe-event-bus", "--name", "eb-cli-bus"))
	var policyBus struct {
		Policy string `json:"Policy"`
	}
	parseJSON(t, out, &policyBus)
	require.Contains(t, policyBus.Policy, "cli-permission")
	runCLI(t, awsCLI("events", "remove-permission",
		"--event-bus-name", "eb-cli-bus",
		"--statement-id", "cli-permission"))

	out = runCLI(t, awsCLI("events", "create-archive",
		"--archive-name", "eb-cli-archive",
		"--event-source-arn", bus.EventBusArn,
		"--event-pattern", `{"source":["sockerless.cli.archive"]}`))
	var archive struct {
		ArchiveArn string `json:"ArchiveArn"`
		State      string `json:"State"`
	}
	parseJSON(t, out, &archive)
	require.NotEmpty(t, archive.ArchiveArn)
	assert.Equal(t, "ENABLED", archive.State)

	out = runCLI(t, awsCLI("events", "describe-archive", "--archive-name", "eb-cli-archive"))
	var describedArchive struct {
		ArchiveName    string `json:"ArchiveName"`
		EventSourceArn string `json:"EventSourceArn"`
	}
	parseJSON(t, out, &describedArchive)
	assert.Equal(t, "eb-cli-archive", describedArchive.ArchiveName)
	assert.Equal(t, bus.EventBusArn, describedArchive.EventSourceArn)

	out = runCLI(t, awsCLI("events", "list-archives", "--event-source-arn", bus.EventBusArn))
	var archives struct {
		Archives []struct {
			ArchiveName string `json:"ArchiveName"`
		} `json:"Archives"`
	}
	parseJSON(t, out, &archives)
	require.Len(t, archives.Archives, 1)

	runCLI(t, awsCLI("events", "put-events", "--entries", `[{"EventBusName":"eb-cli-bus","Source":"sockerless.cli.archive","DetailType":"example","Detail":"{\"cli\":true}"}]`))
	out = runCLI(t, awsCLI("events", "start-replay",
		"--replay-name", "eb-cli-replay",
		"--event-source-arn", archive.ArchiveArn,
		"--event-start-time", "2026-05-27T00:00:00Z",
		"--event-end-time", "2026-05-29T00:00:00Z",
		"--destination", `{"Arn":"`+bus.EventBusArn+`"}`))
	var replay struct {
		ReplayArn string `json:"ReplayArn"`
		State     string `json:"State"`
	}
	parseJSON(t, out, &replay)
	require.NotEmpty(t, replay.ReplayArn)
	assert.Equal(t, "COMPLETED", replay.State)

	out = runCLI(t, awsCLI("events", "describe-replay", "--replay-name", "eb-cli-replay"))
	var describedReplay struct {
		ReplayName string `json:"ReplayName"`
		State      string `json:"State"`
	}
	parseJSON(t, out, &describedReplay)
	assert.Equal(t, "eb-cli-replay", describedReplay.ReplayName)
	assert.Equal(t, "COMPLETED", describedReplay.State)

	out = runCLI(t, awsCLI("events", "list-replays", "--event-source-arn", archive.ArchiveArn))
	var replays struct {
		Replays []struct {
			ReplayName string `json:"ReplayName"`
		} `json:"Replays"`
	}
	parseJSON(t, out, &replays)
	require.Len(t, replays.Replays, 1)
}

// TestEventBridgeCLI_TestEventPattern exercises the test-event-pattern verb:
// a matching event returns Result=true and a near-miss returns Result=false.
func TestEventBridgeCLI_TestEventPattern(t *testing.T) {
	pattern := `{"source":["sockerless.cli.tep"],"detail":{"code":[{"numeric":[">",200]}]}}`

	out := runCLI(t, awsCLI("events", "test-event-pattern",
		"--event-pattern", pattern,
		"--event", `{"id":"1","account":"000000000000","source":"sockerless.cli.tep","detail-type":"job","detail":{"code":500}}`))
	var match struct {
		Result bool `json:"Result"`
	}
	parseJSON(t, out, &match)
	assert.True(t, match.Result)

	out = runCLI(t, awsCLI("events", "test-event-pattern",
		"--event-pattern", pattern,
		"--event", `{"id":"2","account":"000000000000","source":"sockerless.cli.tep","detail-type":"job","detail":{"code":100}}`))
	var noMatch struct {
		Result bool `json:"Result"`
	}
	parseJSON(t, out, &noMatch)
	assert.False(t, noMatch.Result)
}

// TestEventBridgeCLI_ListRuleNamesByTarget creates a rule with a target and
// asserts list-rule-names-by-target returns that rule's name for the ARN.
func TestEventBridgeCLI_ListRuleNamesByTarget(t *testing.T) {
	targetARN := "arn:aws:sqs:us-east-1:000000000000:eb-cli-lrnbt-target"
	runCLI(t, awsCLI("events", "put-rule",
		"--name", "eb-cli-lrnbt-rule",
		"--event-pattern", `{"source":["sockerless.cli.lrnbt"]}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("events", "remove-targets", "--rule", "eb-cli-lrnbt-rule", "--ids", "target"))
		runCLI(t, awsCLI("events", "delete-rule", "--name", "eb-cli-lrnbt-rule"))
	})
	runCLI(t, awsCLI("events", "put-targets",
		"--rule", "eb-cli-lrnbt-rule",
		"--targets", `[{"Id":"target","Arn":"`+targetARN+`"}]`))

	out := runCLI(t, awsCLI("events", "list-rule-names-by-target", "--target-arn", targetARN))
	var names struct {
		RuleNames []string `json:"RuleNames"`
	}
	parseJSON(t, out, &names)
	require.Contains(t, names.RuleNames, "eb-cli-lrnbt-rule")
}

// TestEventBridgeCLI_UpdateEventBus creates a custom bus and asserts
// update-event-bus mutates its description, observable via describe-event-bus.
func TestEventBridgeCLI_UpdateEventBus(t *testing.T) {
	runCLI(t, awsCLI("events", "create-event-bus",
		"--name", "eb-cli-update-bus",
		"--description", "initial"))
	t.Cleanup(func() {
		runCLI(t, awsCLI("events", "delete-event-bus", "--name", "eb-cli-update-bus"))
	})

	out := runCLI(t, awsCLI("events", "update-event-bus",
		"--name", "eb-cli-update-bus",
		"--description", "updated description"))
	var updated struct {
		Name        string `json:"Name"`
		Description string `json:"Description"`
	}
	parseJSON(t, out, &updated)
	assert.Equal(t, "eb-cli-update-bus", updated.Name)
	assert.Equal(t, "updated description", updated.Description)

	out = runCLI(t, awsCLI("events", "describe-event-bus", "--name", "eb-cli-update-bus"))
	var described struct {
		Description string `json:"Description"`
	}
	parseJSON(t, out, &described)
	assert.Equal(t, "updated description", described.Description)
}

// TestEventBridgeCLI_ContentFilterPattern exercises a content-filtering event
// pattern over the CLI: nested detail-object matching plus the prefix and
// numeric matchers. It asserts a matching event is delivered and a near-miss
// (right source, wrong detail) is correctly rejected.
func TestEventBridgeCLI_ContentFilterPattern(t *testing.T) {
	runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "eb-cli-content-q"))
	out := runCLI(t, awsCLI("sqs", "get-queue-url", "--queue-name", "eb-cli-content-q"))
	var q struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &q)
	t.Cleanup(func() {
		runCLI(t, awsCLI("sqs", "delete-queue", "--queue-url", q.QueueUrl))
	})

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", q.QueueUrl,
		"--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	// detail.state prefix "run" AND detail.code numeric > 200.
	pattern := `{"source":["sockerless.cli.content"],"detail":{"state":[{"prefix":"run"}],"code":[{"numeric":[">",200]}]}}`
	ruleOut := runCLI(t, awsCLI("events", "put-rule",
		"--name", "eb-cli-content-rule",
		"--event-pattern", pattern))
	var rule struct {
		RuleArn string `json:"RuleArn"`
	}
	parseJSON(t, ruleOut, &rule)
	t.Cleanup(func() {
		runCLI(t, awsCLI("events", "remove-targets", "--rule", "eb-cli-content-rule", "--ids", "queue"))
		runCLI(t, awsCLI("events", "delete-rule", "--name", "eb-cli-content-rule"))
	})
	runCLI(t, awsCLI("events", "put-targets",
		"--rule", "eb-cli-content-rule",
		"--targets", `[{"Id":"queue","Arn":"`+queueARN+`"}]`))
	setEBQueuePolicyCLI(t, q.QueueUrl, queueARN, rule.RuleArn)

	entries, err := json.Marshal([]map[string]string{
		{"Source": "sockerless.cli.content", "DetailType": "job", "Detail": `{"state":"running","code":500}`},
		{"Source": "sockerless.cli.content", "DetailType": "job", "Detail": `{"state":"queued","code":100}`},
	})
	require.NoError(t, err)
	runCLI(t, awsCLI("events", "put-events", "--entries", string(entries)))

	// Drain up to 10 messages; only the matching event must arrive.
	out = runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", q.QueueUrl,
		"--max-number-of-messages", "10"))
	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 1, "only the matching event must be delivered")
	assertEventBridgeCLIEnvelope(t, recv.Messages[0].Body, "sockerless.cli.content", "job",
		map[string]any{"state": "running", "code": float64(500)})
}

// setEBQueuePolicyCLI attaches a queue policy authorizing EventBridge to deliver
// from the given rule, so EventBridge → SQS delivery is admitted.
func setEBQueuePolicyCLI(t *testing.T, queueURL, queueARN, ruleArn string) {
	t.Helper()
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"` + queueARN + `",` +
		`"Condition":{"ArnEquals":{"aws:SourceArn":"` + ruleArn + `"}}}]}`
	attrs, _ := json.Marshal(map[string]string{"Policy": policy})
	runCLI(t, awsCLI("sqs", "set-queue-attributes", "--queue-url", queueURL, "--attributes", string(attrs)))
}
