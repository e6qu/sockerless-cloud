package aws_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createServiceRole creates an IAM role the service may assume, allowed the
// actions on every resource.
func createServiceRole(t *testing.T, name, service string, actions ...string) string {
	t.Helper()
	iamC := iamClient()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + service + `"},"Action":"sts:AssumeRole"}]}`
	created, err := iamC.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust)})
	require.NoError(t, err)
	allowed, _ := json.Marshal(actions)
	_, err = iamC.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("targets"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":` + string(allowed) + `,"Resource":"*"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamC.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("targets")})
		_, _ = iamC.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	return aws.ToString(created.Role.Arn)
}

func putEventBridgeTarget(t *testing.T, rule string, target ebtypes.Target) {
	t.Helper()
	eb := eventbridgeClient()
	_, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(rule), EventPattern: aws.String(`{"source":["` + rule + `"]}`)})
	require.NoError(t, err)
	out, err := eb.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(rule), Targets: []ebtypes.Target{target}})
	require.NoError(t, err)
	require.Zero(t, out.FailedEntryCount)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(rule), Ids: []string{aws.ToString(target.Id)}})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rule)})
	})
}

func putEventBridgeEvent(t *testing.T, source, detail string) {
	t.Helper()
	out, err := eventbridgeClient().PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
		Source: aws.String(source), DetailType: aws.String("Order Placed"), Detail: aws.String(detail),
	}}})
	require.NoError(t, err)
	require.Zero(t, out.FailedEntryCount)
}

// An Amazon ECS target runs its task definition as the target's role, started
// by events-rule/<rule>.
func TestEventBridge_ECSTargetRunsTheTask_SDK(t *testing.T) {
	ecsC := ecsClient()
	const cluster, rule = "eb-target-cluster", "eb-ecs-target"
	created, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	definition, err := ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("eb-target-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2), Name: aws.String("app"),
			Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	role := createServiceRole(t, "eb-ecs-target-role", "events.amazonaws.com", "ecs:RunTask", "iam:PassRole")
	putEventBridgeTarget(t, rule, ebtypes.Target{
		Id: aws.String("task"), Arn: created.Cluster.ClusterArn, RoleArn: aws.String(role),
		EcsParameters: &ebtypes.EcsParameters{TaskDefinitionArn: definition.TaskDefinition.TaskDefinitionArn, TaskCount: aws.Int32(1)},
	})
	t.Cleanup(func() {
		tasks, _ := ecsC.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
		for _, task := range tasks.TaskArns {
			_, _ = ecsC.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(task)})
		}
	})
	putEventBridgeEvent(t, rule, `{"id":1}`)

	require.Eventually(t, func() bool {
		tasks, err := ecsC.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster), StartedBy: aws.String("events-rule/" + rule)})
		return err == nil && len(tasks.TaskArns) == 1
	}, 30*time.Second, 500*time.Millisecond, "the rule must run one task started by events-rule/%s", rule)
}

// An AWS Batch target submits a job to the job queue the target names.
func TestEventBridge_BatchTargetSubmitsTheJob_SDK(t *testing.T) {
	c := batchClient()
	const rule = "eb-batch-target"
	_, err := c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String("eb-target-ce"), Type: batchtypes.CETypeManaged, State: batchtypes.CEStateEnabled,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteComputeEnvironment(ctx, &batch.DeleteComputeEnvironmentInput{ComputeEnvironment: aws.String("eb-target-ce")})
	})
	queue, err := c.CreateJobQueue(ctx, &batch.CreateJobQueueInput{
		JobQueueName: aws.String("eb-target-jq"), State: batchtypes.JQStateEnabled, Priority: aws.Int32(1),
		ComputeEnvironmentOrder: []batchtypes.ComputeEnvironmentOrder{{
			Order: aws.Int32(1), ComputeEnvironment: aws.String("arn:aws:batch:us-east-1:123456789012:compute-environment/eb-target-ce"),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteJobQueue(ctx, &batch.DeleteJobQueueInput{JobQueue: aws.String("eb-target-jq")}) })
	definition, err := c.RegisterJobDefinition(ctx, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String("eb-target-jd"), Type: batchtypes.JobDefinitionTypeContainer,
		ContainerProperties: &batchtypes.ContainerProperties{Image: aws.String(containerCommandImage), Vcpus: aws.Int32(1), Memory: aws.Int32(128)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeregisterJobDefinition(ctx, &batch.DeregisterJobDefinitionInput{JobDefinition: definition.JobDefinitionArn})
	})
	role := createServiceRole(t, "eb-batch-target-role", "events.amazonaws.com", "batch:SubmitJob")
	putEventBridgeTarget(t, rule, ebtypes.Target{
		Id: aws.String("job"), Arn: queue.JobQueueArn, RoleArn: aws.String(role),
		BatchParameters: &ebtypes.BatchParameters{JobDefinition: definition.JobDefinitionArn, JobName: aws.String("eb-submitted-job")},
	})
	putEventBridgeEvent(t, rule, `{"id":2}`)

	require.Eventually(t, func() bool {
		jobs, err := c.ListJobs(ctx, &batch.ListJobsInput{JobQueue: aws.String("eb-target-jq")})
		if err != nil {
			return false
		}
		for _, job := range jobs.JobSummaryList {
			if aws.ToString(job.JobName) == "eb-submitted-job" {
				return true
			}
		}
		return false
	}, 30*time.Second, 500*time.Millisecond, "the rule must submit eb-submitted-job")
}

// An API destination target calls the endpoint with the connection's API key
// and the event as its body.
func TestEventBridge_ApiDestinationTargetCallsTheEndpoint_SDK(t *testing.T) {
	eb := eventbridgeClient()
	const rule = "eb-api-target"
	var mu sync.Mutex
	var keys, bodies []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		keys, bodies = append(keys, r.Header.Get("x-api-key")), append(bodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(endpoint.Close)
	connection, err := eb.CreateConnection(ctx, &eventbridge.CreateConnectionInput{
		Name: aws.String("eb-api-target-connection"), AuthorizationType: ebtypes.ConnectionAuthorizationTypeApiKey,
		AuthParameters: &ebtypes.CreateConnectionAuthRequestParameters{
			ApiKeyAuthParameters: &ebtypes.CreateConnectionApiKeyAuthRequestParameters{ApiKeyName: aws.String("x-api-key"), ApiKeyValue: aws.String("orders-key")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteConnection(ctx, &eventbridge.DeleteConnectionInput{Name: aws.String("eb-api-target-connection")})
	})
	destination, err := eb.CreateApiDestination(ctx, &eventbridge.CreateApiDestinationInput{
		Name: aws.String("eb-api-target-destination"), ConnectionArn: connection.ConnectionArn,
		InvocationEndpoint: aws.String(endpoint.URL + "/orders"), HttpMethod: ebtypes.ApiDestinationHttpMethodPost,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteApiDestination(ctx, &eventbridge.DeleteApiDestinationInput{Name: aws.String("eb-api-target-destination")})
	})
	role := createServiceRole(t, "eb-api-target-role", "events.amazonaws.com", "events:InvokeApiDestination")
	putEventBridgeTarget(t, rule, ebtypes.Target{Id: aws.String("api"), Arn: destination.ApiDestinationArn, RoleArn: aws.String(role)})
	putEventBridgeEvent(t, rule, `{"id":3}`)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies) == 1
	}, 15*time.Second, 200*time.Millisecond, "the rule must call the API destination")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "orders-key", keys[0])
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(bodies[0]), &event))
	assert.Equal(t, rule, event["source"])
}

// A Kinesis target puts the event on the stream as the target's role.
func TestEventBridge_KinesisTargetPutsTheEvent_SDK(t *testing.T) {
	k := kinesisClient()
	const rule, stream = "eb-kinesis-target", "eb-target-stream"
	_, err := k.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(stream), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = k.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(stream)}) })
	summary, err := k.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(stream)})
	require.NoError(t, err)
	role := createServiceRole(t, "eb-kinesis-target-role", "events.amazonaws.com", "kinesis:PutRecord")
	putEventBridgeTarget(t, rule, ebtypes.Target{
		Id: aws.String("stream"), Arn: summary.StreamDescriptionSummary.StreamARN, RoleArn: aws.String(role),
		KinesisParameters: &ebtypes.KinesisParameters{PartitionKeyPath: aws.String("$.detail.customer")},
	})
	putEventBridgeEvent(t, rule, `{"customer":"c-7"}`)

	iterator, err := k.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName: aws.String(stream), ShardId: aws.String("shardId-000000000000"), ShardIteratorType: ktypes.ShardIteratorTypeTrimHorizon,
	})
	require.NoError(t, err)
	var records []ktypes.Record
	require.Eventually(t, func() bool {
		out, err := k.GetRecords(ctx, &kinesis.GetRecordsInput{ShardIterator: iterator.ShardIterator})
		if err != nil {
			return false
		}
		records = out.Records
		return len(records) == 1
	}, 15*time.Second, 200*time.Millisecond)
	assert.Equal(t, "c-7", aws.ToString(records[0].PartitionKey))
	var event map[string]any
	require.NoError(t, json.Unmarshal(records[0].Data, &event))
	assert.Equal(t, rule, event["source"])
}

// An EventBridge Scheduler invocation its target refuses reaches the target's
// dead-letter queue with the attributes naming the schedule and the error.
func TestScheduler_TargetFailureGoesToTheDeadLetterQueue_SDK(t *testing.T) {
	sched := schedulerClient()
	sqsC := sqsClient()
	dlq, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("scheduler-sdk-dlq")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	attributes, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: dlq.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	dlqARN := attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
	role := createServiceRole(t, "scheduler-sdk-execution", "scheduler.amazonaws.com", "sqs:SendMessage")
	missingQueue := "arn:aws:sqs:us-east-1:123456789012:scheduler-sdk-missing"

	fireAt := time.Now().UTC().Add(3 * time.Second).Format("2006-01-02T15:04:05")
	created, err := sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String("sdk-dead-letter"),
		ScheduleExpression: aws.String("at(" + fireAt + ")"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn: aws.String(missingQueue), RoleArn: aws.String(role), Input: aws.String("nightly-run"),
			RetryPolicy:      &schedtypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(2), MaximumEventAgeInSeconds: aws.Int32(60)},
			DeadLetterConfig: &schedtypes.DeadLetterConfig{Arn: aws.String(dlqARN)},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String("sdk-dead-letter")})
	})

	var message sqstypes.Message
	require.Eventually(t, func() bool {
		out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 1, MessageAttributeNames: []string{"All"},
		})
		if err != nil || len(out.Messages) == 0 {
			return false
		}
		message = out.Messages[0]
		return true
	}, 30*time.Second, 100*time.Millisecond, "the refused invocation must reach the dead-letter queue")
	assert.Equal(t, "nightly-run", aws.ToString(message.Body))
	for name, want := range map[string]string{
		"SCHEDULE_ARN": aws.ToString(created.ScheduleArn), "TARGET_ARN": missingQueue,
		"ERROR_CODE": "AWS.SimpleQueueService.NonExistentQueue",
	} {
		assert.Equal(t, want, aws.ToString(message.MessageAttributes[name].StringValue), name)
	}
}
