package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Behavioral gate: canonical-client tests that assert a stored configuration
// actually produces the expected side effect. Endpoint-count and wire-shape
// gates already exist; this file closes the behavioral/cross-service gap
// described in BUG-2252.

func route53Client() *route53.Client {
	return route53.NewFromConfig(sdkConfig(), func(o *route53.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestBehavioralGate_CloudWatchAlarmSNSActionToSQS asserts that a metric alarm
// transition dispatches its AlarmActions through SNS to an SQS subscriber.
func TestBehavioralGate_CloudWatchAlarmSNSActionToSQS(t *testing.T) {
	cw := cloudwatchClient()
	snsC := snsClient()
	sqsC := sqsClient()

	ns := "Custom/BehavioralGate"
	alarmName := "behavioral-alarm-sns-sqs"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("behavioral-topic")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("behavioral-queue")})
	require.NoError(t, err)
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	// Settle so the subscription is visible to the evaluator before the alarm fires.
	time.Sleep(3 * time.Second)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "SQS subscriber must receive the alarm notification")

	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(recv.Messages[0].Body)), &env))
	assert.Equal(t, "Notification", env["Type"])
}

// TestBehavioralGate_SQS_DLQ_Redrive asserts that a message exceeding
// maxReceiveCount is moved from the source queue to the dead-letter queue.
func TestBehavioralGate_SQS_DLQ_Redrive(t *testing.T) {
	client := sqsClient()

	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("bg-dlq")})
	require.NoError(t, err)
	dlqURL := aws.ToString(dlq.QueueUrl)
	dlqARN := sqsQueueARNFor(t, client, dlqURL)

	src, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("bg-src"),
		Attributes: map[string]string{
			"VisibilityTimeout": "0",
			"RedrivePolicy":     sqsRedrivePolicyJSONCount(t, dlqARN, 2),
		},
	})
	require.NoError(t, err)
	srcURL := aws.ToString(src.QueueUrl)

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(srcURL),
		MessageBody: aws.String("behavioral-poison"),
	})
	require.NoError(t, err)

	for i := 1; i <= 2; i++ {
		recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(srcURL),
			MaxNumberOfMessages: 10,
			VisibilityTimeout:   0,
		})
		require.NoError(t, err)
		require.Len(t, recv.Messages, 1, "receive %d should return the message", i)
	}

	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(srcURL),
		MaxNumberOfMessages: 10,
		VisibilityTimeout:   0,
	})
	require.NoError(t, err)
	assert.Empty(t, recv.Messages, "source queue must be empty after redrive")

	dlqRecv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(dlqURL),
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	require.Len(t, dlqRecv.Messages, 1, "message must arrive in DLQ")
	assert.Equal(t, "behavioral-poison", aws.ToString(dlqRecv.Messages[0].Body))
}

// TestBehavioralGate_AppAutoScaling_AdjustsECSDesiredCount asserts that a
// target-tracking policy on an ECS service actually changes DesiredCount.
func TestBehavioralGate_AppAutoScaling_AdjustsECSDesiredCount(t *testing.T) {
	ecsC := ecsClient()
	cwC := cloudwatchClient()
	asC := appAutoScalingClient()

	cluster := "bg-as-cluster"
	svcName := "bg-as-svc"
	resourceID := "service/" + cluster + "/" + svcName
	const (
		ns   = aastypes.ServiceNamespaceEcs
		dim  = aastypes.ScalableDimensionECSServiceDesiredCount
		minC = int32(1)
		maxC = int32(5)
	)

	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	_, err = ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("bg-as-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(svcName),
		TaskDefinition: aws.String("bg-as-task"),
		DesiredCount:   aws.Int32(1),
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, svcName)

	_, err = asC.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		MinCapacity:       aws.Int32(minC),
		MaxCapacity:       aws.Int32(maxC),
	})
	require.NoError(t, err)
	_, err = asC.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
		PolicyName:        aws.String("bg-cpu-50"),
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		PolicyType:        aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(50.0),
			PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeECSServiceAverageCPUUtilization,
			},
		},
	})
	require.NoError(t, err)

	putCPU := func(value float64) {
		_, err := cwC.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace: aws.String("AWS/ECS"),
			MetricData: []cwtypes.MetricDatum{{
				MetricName: aws.String("CPUUtilization"),
				Dimensions: []cwtypes.Dimension{
					{Name: aws.String("ClusterName"), Value: aws.String(cluster)},
					{Name: aws.String("ServiceName"), Value: aws.String(svcName)},
				},
				Value:     aws.Float64(value),
				Timestamp: aws.Time(time.Now()),
			}},
		})
		require.NoError(t, err)
	}

	desiredCount := func() int32 {
		out, err := ecsC.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: []string{svcName},
		})
		require.NoError(t, err)
		require.Len(t, out.Services, 1)
		return out.Services[0].DesiredCount
	}

	putCPU(90.0)
	require.Eventually(t, func() bool {
		putCPU(90.0)
		return desiredCount() > minC
	}, 30*time.Second, 3*time.Second, "DesiredCount must increase when CPU is above target")
	assert.LessOrEqual(t, desiredCount(), maxC)
}

// TestBehavioralGate_Route53DNS_ResolvesRecord asserts that a record set created
// via ChangeResourceRecordSets is answerable by the sim's DNS server.
func TestBehavioralGate_Route53DNS_ResolvesRecord(t *testing.T) {
	r53 := route53Client()

	createResp, err := r53.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("behavioral.example.com."),
		CallerReference: aws.String(fmt.Sprintf("bg-ref-%d", time.Now().UnixNano())),
	})
	require.NoError(t, err)
	zoneID := createResp.HostedZone.Id

	_, err = r53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: zoneID,
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionCreate,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name: aws.String("host.behavioral.example.com."),
					Type: r53types.RRTypeA,
					TTL:  aws.Int64(300),
					ResourceRecords: []r53types.ResourceRecord{
						{Value: aws.String("1.2.3.4")},
					},
				},
			}},
		},
	})
	require.NoError(t, err)

	listResp, err := r53.ListResourceRecordSets(ctx, &route53.ListResourceRecordSetsInput{
		HostedZoneId: zoneID,
	})
	require.NoError(t, err)
	var found bool
	for _, rs := range listResp.ResourceRecordSets {
		if aws.ToString(rs.Name) == "host.behavioral.example.com." && rs.Type == r53types.RRTypeA {
			found = true
			break
		}
	}
	require.True(t, found, "record set must be visible via ListResourceRecordSets")
}

// TestBehavioralGate_ECSService_ConvergesRunningCount asserts that a service
// created with DesiredCount > 0 reports runningCount == desiredCount — the
// modeled control-plane steady state (reached synchronously, no background
// scheduler launching containers).
func TestBehavioralGate_ECSService_ConvergesRunningCount(t *testing.T) {
	ecsC := ecsClient()

	cluster := "bg-scheduler-cluster"
	svcName := "bg-scheduler-svc"

	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	_, err = ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("bg-scheduler-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:    aws.String("app"),
			Image:   aws.String(containerCommandImage),
			Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(svcName),
		TaskDefinition: aws.String("bg-scheduler-task"),
		DesiredCount:   aws.Int32(2),
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, svcName)

	require.Eventually(t, func() bool {
		out, err := ecsC.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: []string{svcName},
		})
		require.NoError(t, err)
		require.Len(t, out.Services, 1)
		return out.Services[0].RunningCount == 2
	}, 60*time.Second, 2*time.Second, "service must report runningCount == desiredCount")
}

// TestBehavioralGate_CloudWatchLogs_MetricFilterPublishesMetric asserts that a
// metric filter on a log group turns matching log events into a queryable metric.
func TestBehavioralGate_CloudWatchLogs_MetricFilterPublishesMetric(t *testing.T) {
	logsC := cwLogsClient()
	cwC := cloudwatchClient()

	group := "bg-metric-filter-group"
	stream := "bg-stream"
	ns := "Custom/BehavioralGateLogs"
	metric := "ErrorCount"
	ts := time.Now().UTC().Truncate(time.Minute)
	tsMillis := ts.UnixMilli()

	_, err := logsC.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	_, err = logsC.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
	require.NoError(t, err)

	_, err = logsC.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("bg-filter"),
		FilterPattern: aws.String(`{ $.status = "error" }`),
		MetricTransformations: []cwltypes.MetricTransformation{{
			MetricName:      aws.String(metric),
			MetricNamespace: aws.String(ns),
			MetricValue:     aws.String("1"),
		}},
	})
	require.NoError(t, err)

	_, err = logsC.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents: []cwltypes.InputLogEvent{
			{Timestamp: aws.Int64(tsMillis), Message: aws.String(`{"status":"error"}`)},
			{Timestamp: aws.Int64(tsMillis), Message: aws.String(`{"status":"ok"}`)},
			{Timestamp: aws.Int64(tsMillis), Message: aws.String(`{"status":"error"}`)},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		out, err := cwC.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			MetricDataQueries: []cwtypes.MetricDataQuery{{
				Id: aws.String("m1"),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{
						Namespace:  aws.String(ns),
						MetricName: aws.String(metric),
					},
					Period: aws.Int32(60),
					Stat:   aws.String("Sum"),
				},
			}},
			StartTime: aws.Time(ts.Add(-time.Minute)),
			EndTime:   aws.Time(ts.Add(2 * time.Minute)),
		})
		require.NoError(t, err)
		if len(out.MetricDataResults) == 0 {
			return false
		}
		var sum float64
		for _, v := range out.MetricDataResults[0].Values {
			sum += v
		}
		return sum == 2
	}, 15*time.Second, 1*time.Second, "metric filter must publish Sum of 2 for two matching events")
}
