package aws_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsSourceEventMessages lists the messages DescribeEvents answers for one
// source over the past hour, oldest first.
func rdsSourceEventMessages(t *testing.T, ctx context.Context, c *rds.Client, sourceType types.SourceType, id string) []string {
	t.Helper()
	out, err := c.DescribeEvents(ctx, &rds.DescribeEventsInput{
		SourceType: sourceType, SourceIdentifier: aws.String(id), Duration: aws.Int32(60),
	})
	require.NoError(t, err)
	var messages []string
	for _, event := range out.Events {
		messages = append(messages, aws.ToString(event.Message))
	}
	return messages
}

// assertSubsequence checks that want appears in got in order, other entries
// allowed between them.
func assertSubsequence(t *testing.T, want, got []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	assert.Equal(t, len(want), i, "want %q in order within %q", want, got)
}

// A DB instance's life writes the events the Amazon RDS event table lists:
// creation, its first backup, a reboot, a stop and a start, a manual
// snapshot's creation and deletion, a parameter change and the instance's
// deletion. DescribeEvents returns them, and the DB instance events reach the
// default event bus as "RDS DB Instance Event" from aws.rds.
func TestRDS_LifecycleWritesEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, eb, sqsC := rdsClient(), eventbridgeClient(), sqsClient()
	id := fmt.Sprintf("events-%d", time.Now().UnixNano()%1_000_000)
	f := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "postgres"}

	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(id + "-q")})
	require.NoError(t, err)
	attrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := attrs.Attributes["QueueArn"]
	rule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(id + "-rule"),
		EventPattern: aws.String(fmt.Sprintf(`{"source":["aws.rds"],"detail":{"SourceIdentifier":[%q]}}`, id)),
	})
	require.NoError(t, err)
	setEBQueuePolicy(t, sqsC, queue.QueueUrl, queueARN, aws.ToString(rule.RuleArn))
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String(id + "-rule"), Targets: []ebtypes.Target{{Id: aws.String("queue"), Arn: aws.String(queueARN)}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(context.Background(), &eventbridge.RemoveTargetsInput{Rule: aws.String(id + "-rule"), Ids: []string{"queue"}})
		_, _ = eb.DeleteRule(context.Background(), &eventbridge.DeleteRuleInput{Name: aws.String(id + "-rule")})
	})

	group := id + "-params"
	_, err = c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName: aws.String(group), DBParameterGroupFamily: aws.String("postgres17"), Description: aws.String("events"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(context.Background(), &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(group)})
	})
	_, err = c.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{
		DBParameterGroupName: aws.String(group),
		Parameters: []types.Parameter{{
			ParameterName: aws.String("log_min_duration_statement"), ParameterValue: aws.String("250"), ApplyMethod: types.ApplyMethodImmediate,
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Updated parameter log_min_duration_statement to 250 with apply method immediate."},
		rdsSourceEventMessages(t, ctx, c, types.SourceTypeDbParameterGroup, group))

	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), DBInstanceClass: aws.String("db.t3.micro"), Engine: aws.String("postgres"),
		AllocatedStorage: aws.Int32(20), MasterUsername: aws.String("events"), MasterUserPassword: aws.String("events-password-1"),
	})
	require.NoError(t, err)
	f.cleanup(id)
	f.waitAvailable(id)

	_, err = c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{DBInstanceIdentifier: aws.String(id)})
	require.NoError(t, err)
	f.waitAvailable(id)

	_, err = c.StopDBInstance(ctx, &rds.StopDBInstanceInput{DBInstanceIdentifier: aws.String(id)})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		out, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
		return err == nil && aws.ToString(out.DBInstances[0].DBInstanceStatus) == "stopped"
	}, 3*time.Minute, waiterMinDelay)
	_, err = c.StartDBInstance(ctx, &rds.StartDBInstanceInput{DBInstanceIdentifier: aws.String(id)})
	require.NoError(t, err)
	f.waitAvailable(id)

	snapshot := id + "-snap"
	_, err = c.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{DBInstanceIdentifier: aws.String(id), DBSnapshotIdentifier: aws.String(snapshot)})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBSnapshotAvailableWaiter(c, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(snapshot)}, 3*time.Minute))
	_, err = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{DBSnapshotIdentifier: aws.String(snapshot)})
	require.NoError(t, err)
	assert.Equal(t, []string{"Creating manual snapshot.", "Manual snapshot created.", "Deleted user snapshot."},
		rdsSourceEventMessages(t, ctx, c, types.SourceTypeDbSnapshot, snapshot))

	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(id), SkipFinalSnapshot: aws.Bool(true)})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBInstanceDeletedWaiter(c, func(o *rds.DBInstanceDeletedWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 3*time.Minute))

	assertSubsequence(t, []string{
		"DB instance created.",
		"Backing up DB instance.",
		"Finished DB instance backup.",
		"DB instance restarted.",
		"DB instance stopped.",
		"DB instance started.",
		"DB instance deleted.",
	}, rdsSourceEventMessages(t, ctx, c, types.SourceTypeDbInstance, id))

	received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, received.Messages)
	var event struct {
		Source     string   `json:"source"`
		DetailType string   `json:"detail-type"`
		Resources  []string `json:"resources"`
		Detail     struct {
			EventID          string   `json:"EventID"`
			SourceType       string   `json:"SourceType"`
			SourceIdentifier string   `json:"SourceIdentifier"`
			Message          string   `json:"Message"`
			EventCategories  []string `json:"EventCategories"`
		} `json:"detail"`
	}
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(received.Messages[0].Body)), &event))
	assert.Equal(t, "aws.rds", event.Source)
	assert.Equal(t, "RDS DB Instance Event", event.DetailType)
	assert.Equal(t, "RDS-EVENT-0005", event.Detail.EventID)
	assert.Equal(t, "DB_INSTANCE", event.Detail.SourceType)
	assert.Equal(t, id, event.Detail.SourceIdentifier)
	assert.Equal(t, []string{"creation"}, event.Detail.EventCategories)
	require.Len(t, event.Resources, 1)
}
