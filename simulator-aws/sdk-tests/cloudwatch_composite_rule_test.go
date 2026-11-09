package aws_sdk_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putQuietMetricAlarm(t *testing.T, cw *cloudwatch.Client, name, namespace string) {
	t.Helper()
	_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String(namespace),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("notBreaching"),
	})
	require.NoError(t, err)
}

func requireValidationError(t *testing.T, err error, message string) {
	t.Helper()
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "want a ValidationError, got %v", err)
	assert.Equal(t, "ValidationError", apiErr.ErrorCode())
	assert.Equal(t, message, apiErr.ErrorMessage())
}

// TestCloudWatch_CompositeAlarmRuleRefusals expects PutCompositeAlarm to
// refuse a rule that does not parse, one with surrounding whitespace and one
// naming an alarm the Region does not hold, and DeleteAlarms to refuse an
// alarm a composite alarm depends on and more than one composite alarm per
// request.
func TestCloudWatch_CompositeAlarmRuleRefusals(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	id := nextCloudWatchProbeID()
	child := fmt.Sprintf("sdk-rule-child-%d", id)
	parent := fmt.Sprintf("sdk-rule-parent-%d", id)
	other := fmt.Sprintf("sdk-rule-other-%d", id)
	missing := fmt.Sprintf("sdk-rule-missing-%d", id)
	putQuietMetricAlarm(t, cw, child, fmt.Sprintf("Custom/Rule-%d", id))
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{child}}) })

	put := func(name, rule string) error {
		_, err := cw.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{AlarmName: aws.String(name), AlarmRule: aws.String(rule)})
		return err
	}
	requireValidationError(t, put(parent, fmt.Sprintf("ALARM(%q) XOR TRUE", child)),
		fmt.Sprintf("Error in AlarmRule [Unsupported token 'XOR' at char %d, must be: <EOF>, 'AND', 'OR']", len(child)+10))
	requireValidationError(t, put(parent, fmt.Sprintf("ALARM(%q) AND", child)),
		fmt.Sprintf("Error in AlarmRule [Unsupported token '<EOF>' at char %d, must be: '(', 'NOT', AT_LEAST, TRUE or FALSE, ALARM, OK, or INSUFFICIENT_DATA]", len(child)+13))
	requireValidationError(t, put(parent, fmt.Sprintf("ALARM(%q", child)),
		fmt.Sprintf("Error in AlarmRule [Unsupported token '<EOF>' at char %d]", len(child)+8))
	requireValidationError(t, put(parent, "TRUE "), "AlarmRule must not contain leading or trailing whitespace or be null")
	requireValidationError(t, put(parent, fmt.Sprintf("ALARM(%s) OR ALARM(%q)", child, missing)),
		fmt.Sprintf("Could not save the composite alarm as alarms [arn:aws:cloudwatch:us-east-1:123456789012:alarm:%s] in the alarm rule do not exist", missing))
	none, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{parent}, AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeCompositeAlarm},
	})
	require.NoError(t, err)
	assert.Empty(t, none.CompositeAlarms, "a refused rule saves nothing")

	require.NoError(t, put(parent, fmt.Sprintf("ALARM(arn:aws:cloudwatch:us-east-1:123456789012:alarm:%s)", child)))
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{parent}}) })
	require.NoError(t, put(other, "TRUE"))
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{other}}) })

	dependsOnIt := fmt.Sprintf("Cannot delete %s as there are composite alarm(s) depending on it.", child)
	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{child}})
	requireValidationError(t, err, dependsOnIt)
	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{child, parent}})
	requireValidationError(t, err, dependsOnIt)
	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{parent, other}})
	requireValidationError(t, err, "Cannot delete [2] Composite Alarms as only one composite alarm delete is allowed in single request")
	kept, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{child, parent, other}, AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeMetricAlarm, cwtypes.AlarmTypeCompositeAlarm},
	})
	require.NoError(t, err)
	assert.Len(t, kept.MetricAlarms, 1, "a refused delete keeps every alarm")
	assert.Len(t, kept.CompositeAlarms, 2, "a refused delete keeps every alarm")

	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{parent}})
	require.NoError(t, err)
	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{child}})
	require.NoError(t, err, "an alarm no composite alarm references deletes")
}

// TestCloudWatch_CompositeAlarmPrecedenceAndUpdate expects AND to bind tighter
// than OR and NOT tighter than AND, and an update that changes the rule's
// outcome to say so in the state reason.
func TestCloudWatch_CompositeAlarmPrecedenceAndUpdate(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	id := nextCloudWatchProbeID()
	name := fmt.Sprintf("sdk-rule-precedence-%d", id)
	topicARN, queueURL := alarmNotificationQueue(t, fmt.Sprintf("rule-precedence-%d", id))
	put := func(rule string) {
		_, err := cw.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{
			AlarmName: aws.String(name), AlarmRule: aws.String(rule),
			AlarmActions: []string{topicARN}, OKActions: []string{topicARN},
		})
		require.NoError(t, err)
	}
	put("TRUE OR TRUE AND FALSE")
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) })
	arn := "arn:aws:cloudwatch:us-east-1:123456789012:alarm:" + name

	created := receiveAlarmNotification(t, queueURL, name)
	assert.Equal(t, "ALARM", created["NewStateValue"], "TRUE OR (TRUE AND FALSE)")
	assert.Equal(t, arn+" was created and its alarm rule evaluates to ALARM", created["NewStateReason"])
	for state, want := range map[cwtypes.StateValue]int{cwtypes.StateValueAlarm: 1, cwtypes.StateValueOk: 0} {
		byState, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
			AlarmNames: []string{name}, AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeCompositeAlarm}, StateValue: state,
		})
		require.NoError(t, err)
		assert.Len(t, byState.CompositeAlarms, want, "DescribeAlarms filtered by StateValue %s", state)
	}

	put("NOT TRUE AND FALSE")
	updated := receiveAlarmNotification(t, queueURL, name)
	assert.Equal(t, "OK", updated["NewStateValue"], "(NOT TRUE) AND FALSE")
	assert.Equal(t, "ALARM", updated["OldStateValue"])
	assert.Equal(t, arn+" was updated and its new alarm rule evaluates to OK", updated["NewStateReason"])
}

// TestCloudWatch_CompositeAlarmHoldEndsWhenAChildFlaps expects a child that
// changes state and changes back to end a composite alarm's SetAlarmState
// hold, the composite returning to its actual state with that child as the
// trigger.
func TestCloudWatch_CompositeAlarmHoldEndsWhenAChildFlaps(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	id := nextCloudWatchProbeID()
	child := fmt.Sprintf("sdk-flap-child-%d", id)
	composite := fmt.Sprintf("sdk-flap-composite-%d", id)
	topicARN, queueURL := alarmNotificationQueue(t, fmt.Sprintf("flap-%d", id))
	putQuietMetricAlarm(t, cw, child, fmt.Sprintf("Custom/Flap-%d", id))
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{child}}) })
	_, err := cw.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{
		AlarmName: aws.String(composite), AlarmRule: aws.String(fmt.Sprintf("ALARM(%s)", child)),
		AlarmActions: []string{topicARN}, OKActions: []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{composite}}) })
	require.Equal(t, "OK", receiveAlarmNotification(t, queueURL, composite)["NewStateValue"])

	_, err = cw.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName: aws.String(composite), StateValue: cwtypes.StateValueAlarm, StateReason: aws.String("held"),
	})
	require.NoError(t, err)
	require.Equal(t, "ALARM", receiveAlarmNotification(t, queueURL, composite)["NewStateValue"])

	// The child moves to ALARM and returns to OK at its next evaluation.
	_, err = cw.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName: aws.String(child), StateValue: cwtypes.StateValueAlarm, StateReason: aws.String("flap"),
	})
	require.NoError(t, err)

	back := receiveAlarmNotification(t, queueURL, composite)
	assert.Equal(t, "OK", back["NewStateValue"])
	assert.Equal(t, "ALARM", back["OldStateValue"])
	childARN := "arn:aws:cloudwatch:us-east-1:123456789012:alarm:" + child
	assert.Equal(t, []any{childARN}, back["TriggeringChildren"])
	assert.True(t, strings.HasPrefix(back["NewStateReason"].(string), childARN+" transitioned to OK at "), "reason: %v", back["NewStateReason"])
}
