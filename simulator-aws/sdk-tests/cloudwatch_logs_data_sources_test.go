package aws_sdk_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogs_DataSourceClassification classifies the events of log groups
// tagged cw:datasource:name and cw:datasource:type under that data source, as
// the data source discovery guide describes, and answers GetLogFields,
// ListAggregateLogGroupSummaries, CloudWatch Logs Insights and the field
// index policies that select the data source from those events.
func TestLogs_DataSourceClassification(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	name, typ := uniquename.Alnum("sdkapp"), "orders"
	tagged, alsoTagged, untagged := uniquename.New("/test/ds/tagged"), uniquename.New("/test/ds/also"), uniquename.New("/test/ds/plain")
	for _, group := range []string{tagged, alsoTagged, untagged} {
		newPolicyLogGroup(t, cw, group, "s")
	}
	for _, group := range []string{tagged, alsoTagged} {
		_, err := cw.TagResource(ctx, &cloudwatchlogs.TagResourceInput{
			ResourceArn: aws.String(fmt.Sprintf("arn:aws:logs:us-east-1:123456789012:log-group:%s", group)),
			Tags:        map[string]string{"cw:datasource:name": name, "cw:datasource:type": typ},
		})
		require.NoError(t, err)
	}

	policyName := uniquename.New("sdk-ds-index")
	_, err := cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policyName), PolicyType: cwlogtypes.PolicyTypeFieldIndexPolicy,
		PolicyDocument:    aws.String(`{"Fields":["orderId"]}`),
		SelectionCriteria: aws.String(fmt.Sprintf(`DataSourceName = %q AND DataSourceType = %q`, name, typ)),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{PolicyName: aws.String(policyName), PolicyType: cwlogtypes.PolicyTypeFieldIndexPolicy})
	})
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policyName + "-dup"), PolicyType: cwlogtypes.PolicyTypeFieldIndexPolicy,
		PolicyDocument:    aws.String(`{"Fields":["customer"]}`),
		SelectionCriteria: aws.String(fmt.Sprintf(`DataSourceType = %q AND DataSourceName = %q`, typ, name)),
	})
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "a second policy for the same data source: %v", err)

	_, err = cw.PutIndexPolicy(ctx, &cloudwatchlogs.PutIndexPolicyInput{
		LogGroupIdentifier: aws.String(tagged), PolicyDocument: aws.String(`{"Fields":["region"]}`),
	})
	require.NoError(t, err)

	message := `{"orderId":"o-1","amount":12.5,"region":"eu","paid":true}`
	for _, group := range []string{tagged, alsoTagged, untagged} {
		putMessages(t, cw, group, "s", message)
	}

	fields, err := cw.GetLogFields(ctx, &cloudwatchlogs.GetLogFieldsInput{DataSourceName: aws.String(name), DataSourceType: aws.String(typ)})
	require.NoError(t, err)
	byName := map[string]string{}
	for _, f := range fields.LogFields {
		require.NotNil(t, f.LogFieldType)
		byName[aws.ToString(f.LogFieldName)] = aws.ToString(f.LogFieldType.Type)
	}
	assert.Equal(t, "string", byName["orderId"])
	assert.Equal(t, "double", byName["amount"])
	assert.Equal(t, "boolean", byName["paid"])
	assert.Contains(t, byName, "@data_source_name")
	assert.Contains(t, byName, "@timestamp")

	_, err = cw.GetLogFields(ctx, &cloudwatchlogs.GetLogFieldsInput{DataSourceName: aws.String(name), DataSourceType: aws.String("refunds")})
	var notFound *cwlogtypes.ResourceNotFoundException
	assert.True(t, errors.As(err, &notFound), "a data source no event carries: %v", err)

	agg, err := cw.ListAggregateLogGroupSummaries(ctx, &cloudwatchlogs.ListAggregateLogGroupSummariesInput{
		GroupBy:     cwlogtypes.ListAggregateLogGroupSummariesGroupByDataSourceNameTypeAndFormat,
		DataSources: []cwlogtypes.DataSourceFilter{{Name: aws.String(name)}},
	})
	require.NoError(t, err)
	require.Len(t, agg.AggregateLogGroupSummaries, 1)
	summary := agg.AggregateLogGroupSummaries[0]
	assert.Equal(t, int32(2), aws.ToInt32(summary.LogGroupCount))
	identifiers := map[string]string{}
	for _, id := range summary.GroupingIdentifiers {
		identifiers[aws.ToString(id.Key)] = aws.ToString(id.Value)
	}
	assert.Equal(t, map[string]string{"dataSource.Name": name, "dataSource.Type": typ, "dataSource.Format": "Custom"}, identifiers)

	plain, err := cw.ListAggregateLogGroupSummaries(ctx, &cloudwatchlogs.ListAggregateLogGroupSummariesInput{
		GroupBy:             cwlogtypes.ListAggregateLogGroupSummariesGroupByDataSourceNameAndType,
		LogGroupNamePattern: aws.String(untagged[len("/test/ds/"):]),
	})
	require.NoError(t, err)
	require.Len(t, plain.AggregateLogGroupSummaries, 1)
	assert.Len(t, plain.AggregateLogGroupSummaries[0].GroupingIdentifiers, 2)
	assert.Equal(t, "Unknown", aws.ToString(plain.AggregateLogGroupSummaries[0].GroupingIdentifiers[0].Value))

	start, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: []string{tagged, untagged},
		QueryString:   aws.String(`fields @data_source_name, @data_source_type, @data_format | sort @data_format asc`),
		StartTime:     aws.Int64(time.Now().Add(-time.Hour).Unix()), EndTime: aws.Int64(time.Now().Add(time.Minute).Unix()),
	})
	require.NoError(t, err)
	results, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: start.QueryId})
	require.NoError(t, err)
	var rows []map[string]string
	for _, row := range results.Results {
		r := map[string]string{}
		for _, f := range row {
			r[aws.ToString(f.Field)] = aws.ToString(f.Value)
		}
		rows = append(rows, r)
	}
	require.Len(t, rows, 2)
	assert.Equal(t, []string{name, typ, "Custom"}, []string{rows[0]["@data_source_name"], rows[0]["@data_source_type"], rows[0]["@data_format"]})
	assert.Equal(t, []string{"Unknown", "Unknown", "Default"}, []string{rows[1]["@data_source_name"], rows[1]["@data_source_type"], rows[1]["@data_format"]})

	indexes := func(group string) map[string]cwlogtypes.FieldIndex {
		out, err := cw.DescribeFieldIndexes(ctx, &cloudwatchlogs.DescribeFieldIndexesInput{LogGroupIdentifiers: []string{group}})
		require.NoError(t, err)
		byName := map[string]cwlogtypes.FieldIndex{}
		for _, fi := range out.FieldIndexes {
			byName[aws.ToString(fi.FieldIndexName)] = fi
		}
		return byName
	}
	got := indexes(tagged)
	assert.Equal(t, cwlogtypes.IndexCategoryCustom, got["orderId"].IndexCategory, "the data source policy applies alongside the group's own")
	assert.Equal(t, cwlogtypes.IndexCategoryCustom, got["region"].IndexCategory)
	assert.Equal(t, cwlogtypes.IndexCategoryDefault, got["@data_source_name"].IndexCategory)
	assert.Contains(t, indexes(alsoTagged), "orderId")
	assert.NotContains(t, indexes(untagged), "orderId", "an event of another data source is not indexed")
}

// TestEventBridge_LogGroupTargetNeedsResourcePolicy_SDK holds an Amazon
// EventBridge rule's CloudWatch Logs target to the account's CloudWatch Logs
// resource policies: without one granting events.amazonaws.com the delivery
// is refused and reaches the dead-letter queue, and once one does, the event
// lands in the log group.
func TestEventBridge_LogGroupTargetNeedsResourcePolicy_SDK(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	eb := eventbridgeClient()
	sqsC := sqsClient()
	group, rule := uniquename.New("/aws/events/sdk-target"), uniquename.New("eb-sdk-logs-target")
	newPolicyLogGroup(t, cw, group, "")
	groupARN := "arn:aws:logs:us-east-1:123456789012:log-group:" + group

	dlq, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("eb-sdk-logs-dlq"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := queueARNOf(t, sqsC, dlq.QueueUrl)
	created, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(rule), EventPattern: aws.String(`{"source":["` + rule + `"]}`)})
	require.NoError(t, err)
	ruleARN := aws.ToString(created.RuleArn)
	setEBQueuePolicy(t, sqsC, dlq.QueueUrl, dlqARN, ruleARN)
	putEventBridgeTarget(t, rule, ebtypes.Target{
		Id: aws.String("logs"), Arn: aws.String(groupARN),
		RetryPolicy:      &ebtypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(0), MaximumEventAgeInSeconds: aws.Int32(60)},
		DeadLetterConfig: &ebtypes.DeadLetterConfig{Arn: aws.String(dlqARN)},
	})

	putEventBridgeEvent(t, rule, `{"attempt":"refused"}`)
	refused := receiveWithAttributes(t, sqsC, dlq.QueueUrl, 20*time.Second)
	assert.Equal(t, "AccessDeniedException", messageAttribute(refused, "ERROR_CODE"))
	assert.Equal(t, groupARN, messageAttribute(refused, "TARGET_ARN"))

	policyName := uniquename.New("sdk-eventbridge-to-logs")
	_, err = cw.PutResourcePolicy(ctx, &cloudwatchlogs.PutResourcePolicyInput{
		PolicyName: aws.String(policyName),
		PolicyDocument: aws.String(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Principal":{"Service":["events.amazonaws.com","delivery.logs.amazonaws.com"]},
			"Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"%s:*",
			"Condition":{"ArnEquals":{"aws:SourceArn":%q}}}]}`, groupARN, ruleARN)),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteResourcePolicy(ctx, &cloudwatchlogs.DeleteResourcePolicyInput{PolicyName: aws.String(policyName)})
	})

	putEventBridgeEvent(t, rule, `{"attempt":"granted"}`)
	var messages []string
	require.Eventually(t, func() bool {
		out, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group)})
		if err != nil {
			return false
		}
		messages = messages[:0]
		for _, e := range out.Events {
			messages = append(messages, aws.ToString(e.Message))
		}
		return len(messages) > 0
	}, 20*time.Second, 200*time.Millisecond, "the granted delivery must reach the log group")
	require.Len(t, messages, 1)
	assert.Contains(t, messages[0], `"granted"`)
}
