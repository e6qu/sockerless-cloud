package aws_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSFN_GenericAWSSDKIntegrations_SDK(t *testing.T) {
	statesAPI := sfnClient()
	dynamoAPI := ddbClient()
	secretsAPI := smClient()
	glueAPI := glueClient()
	apiGatewayAPI := apigwClient()
	batchAPI := batchClient()
	lambdaAPI := lambdaClient()
	iamAPI := iamClient()
	s3API := s3Client()
	route53API := route53Client()
	cloudFrontAPI := cfClient()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tableName := "sfn-sdk-" + suffix
	secretName := "sfn-sdk-" + suffix
	databaseName := "sfn_sdk_" + suffix
	restAPIName := "sfn-sdk-" + suffix
	consumableName := "sfn-sdk-" + suffix
	roleName := "sfn-sdk-" + suffix
	bucketName := "sfn-sdk-" + suffix
	functionName := "sfn-sdk-" + suffix

	_, err := lambdaAPI.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
	})
	require.NoError(t, err)

	_, err = s3API.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err)

	_, err = iamAPI.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		Path:                     aws.String("/sfn-sdk/"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)

	_, err = dynamoAPI.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{
			AttributeName: aws.String("id"),
			AttributeType: ddbtypes.ScalarAttributeTypeS,
		}},
		KeySchema: []ddbtypes.KeySchemaElement{{
			AttributeName: aws.String("id"),
			KeyType:       ddbtypes.KeyTypeHash,
		}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = dynamoAPI.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
		_, _ = secretsAPI.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
			SecretId:                   aws.String(secretName),
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
		_, _ = glueAPI.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(databaseName)})
		restAPIs, _ := apiGatewayAPI.GetRestApis(ctx, &apigateway.GetRestApisInput{})
		for _, restAPI := range restAPIs.Items {
			if aws.ToString(restAPI.Name) == restAPIName {
				_, _ = apiGatewayAPI.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: restAPI.Id})
			}
		}
		_, _ = batchAPI.DeleteConsumableResource(ctx, &batch.DeleteConsumableResourceInput{
			ConsumableResource: aws.String(consumableName),
		})
		_, _ = iamAPI.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
		_, _ = s3API.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucketName)})
		_, _ = lambdaAPI.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})

	definition, err := json.Marshal(map[string]any{
		"StartAt": "WriteItem",
		"States": map[string]any{
			"WriteItem": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:dynamodb:putItem",
				"Parameters": map[string]any{
					"TableName": tableName,
					"Item": map[string]any{
						"id":    map[string]any{"S": "from-workflow"},
						"value": map[string]any{"S": "persisted"},
					},
				},
				"ResultPath": nil,
				"Next":       "CreateSecret",
			},
			"CreateSecret": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:secretsmanager:createSecret",
				"Parameters": map[string]any{
					"Name":         secretName,
					"SecretString": "persisted",
				},
				"ResultPath": nil,
				"Next":       "CreateDatabase",
			},
			"CreateDatabase": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:glue:createDatabase",
				"Parameters": map[string]any{
					"DatabaseInput": map[string]any{"Name": databaseName},
				},
				"ResultPath": nil,
				"Next":       "ListBudgets",
			},
			"ListBudgets": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:budgets:describeBudgets",
				"Parameters": map[string]any{
					"AccountId":  "123456789012",
					"MaxResults": 10,
				},
				"ResultPath": nil,
				"Next":       "ListBuildProjects",
			},
			"ListBuildProjects": map[string]any{
				"Type":       "Task",
				"Resource":   "arn:aws:states:::aws-sdk:codebuild:listProjects",
				"Parameters": map[string]any{},
				"ResultPath": nil,
				"Next":       "CreateRestAPI",
			},
			"CreateRestAPI": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:apigateway:createRestApi",
				"Parameters": map[string]any{
					"Name":        restAPIName,
					"Description": "created through AWS Step Functions",
				},
				"ResultPath": nil,
				"Next":       "CreateConsumableResource",
			},
			"CreateConsumableResource": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:batch:createConsumableResource",
				"Parameters": map[string]any{
					"ConsumableResourceName": consumableName,
					"TotalQuantity":          11,
					"ResourceType":           "REPLENISHABLE",
				},
				"ResultPath": nil,
				"Next":       "ListFunctions",
			},
			"ListFunctions": map[string]any{
				"Type":       "Task",
				"Resource":   "arn:aws:states:::aws-sdk:lambda:listFunctions",
				"Parameters": map[string]any{},
				"ResultPath": nil,
				"Next":       "TagBucket",
			},
			"TagBucket": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:s3:putBucketTagging",
				"Parameters": map[string]any{
					"Bucket": bucketName,
					"Tagging": map[string]any{
						"TagSet": []any{map[string]any{"Key": "owner", "Value": "step-functions"}},
					},
				},
				"ResultPath": nil,
				"Next":       "ListHostedZones",
			},
			"ListHostedZones": map[string]any{
				"Type":       "Task",
				"Resource":   "arn:aws:states:::aws-sdk:route53:listHostedZones",
				"Parameters": map[string]any{},
				"ResultPath": nil,
				"Next":       "ListDistributions",
			},
			"ListDistributions": map[string]any{
				"Type":       "Task",
				"Resource":   "arn:aws:states:::aws-sdk:cloudfront:listDistributions",
				"Parameters": map[string]any{},
				"ResultPath": nil,
				"Next":       "ListRoles",
			},
			"ListRoles": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:iam:listRoles",
				"Parameters": map[string]any{
					"PathPrefix": "/sfn-sdk/",
				},
				"ResultPath": "$.IAM",
				"Next":       "InvokeFunction",
			},
			"InvokeFunction": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::aws-sdk:lambda:invoke",
				"Parameters": map[string]any{
					"FunctionName": functionName,
					"Payload":      base64.StdEncoding.EncodeToString([]byte(`{"source":"aws-sdk-integration"}`)),
				},
				"ResultPath": "$.Lambda",
				"End":        true,
			},
		},
	})
	require.NoError(t, err)
	machine, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-generic-sdk-" + suffix),
		Definition: aws.String(string(definition)),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
			StateMachineArn: machine.StateMachineArn,
		})
	})
	execution, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: machine.StateMachineArn,
	})
	require.NoError(t, err)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		described, describeErr := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
			ExecutionArn: execution.ExecutionArn,
		})
		if !assert.NoError(collect, describeErr) {
			return
		}
		assert.Equal(collect, sfntypes.ExecutionStatusSucceeded, described.Status)
		assert.Empty(collect, aws.ToString(described.Error))
		assert.Empty(collect, aws.ToString(described.Cause))
	}, 10*time.Second, 100*time.Millisecond)

	item, err := dynamoAPI.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key:       map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "from-workflow"}},
	})
	require.NoError(t, err)
	require.Equal(t, "persisted", item.Item["value"].(*ddbtypes.AttributeValueMemberS).Value)

	secret, err := secretsAPI.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	})
	require.NoError(t, err)
	assert.Equal(t, "persisted", aws.ToString(secret.SecretString))

	database, err := glueAPI.GetDatabase(ctx, &glue.GetDatabaseInput{Name: aws.String(databaseName)})
	require.NoError(t, err)
	assert.Equal(t, databaseName, aws.ToString(database.Database.Name))

	restAPIs, err := apiGatewayAPI.GetRestApis(ctx, &apigateway.GetRestApisInput{})
	require.NoError(t, err)
	createdRestAPI := false
	for _, restAPI := range restAPIs.Items {
		if aws.ToString(restAPI.Name) == restAPIName {
			createdRestAPI = true
			assert.Equal(t, "created through AWS Step Functions", aws.ToString(restAPI.Description))
		}
	}
	assert.True(t, createdRestAPI)

	consumable, err := batchAPI.DescribeConsumableResource(ctx, &batch.DescribeConsumableResourceInput{
		ConsumableResource: aws.String(consumableName),
	})
	require.NoError(t, err)
	assert.Equal(t, consumableName, aws.ToString(consumable.ConsumableResourceName))
	assert.EqualValues(t, 11, aws.ToInt64(consumable.TotalQuantity))

	_, err = lambdaAPI.ListFunctions(ctx, &lambda.ListFunctionsInput{})
	require.NoError(t, err)
	tags, err := s3API.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucketName)})
	require.NoError(t, err)
	require.Equal(t, []s3types.Tag{{Key: aws.String("owner"), Value: aws.String("step-functions")}}, tags.TagSet)
	_, err = route53API.ListHostedZones(ctx, &route53.ListHostedZonesInput{})
	require.NoError(t, err)
	_, err = cloudFrontAPI.ListDistributions(ctx, &cloudfront.ListDistributionsInput{})
	require.NoError(t, err)

	completed, err := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
		ExecutionArn: execution.ExecutionArn,
	})
	require.NoError(t, err)
	var workflowOutput struct {
		IAM struct {
			Roles []struct {
				RoleName string
			}
		}
		Lambda struct {
			Payload string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(completed.Output)), &workflowOutput))
	require.Len(t, workflowOutput.IAM.Roles, 1)
	assert.Equal(t, roleName, workflowOutput.IAM.Roles[0].RoleName)
	decodedPayload, err := base64.StdEncoding.DecodeString(workflowOutput.Lambda.Payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{"source":"aws-sdk-integration"}`, string(decodedPayload))
}

// TestSFN_EventingAndObservabilityIntegrations_SDK proves the workflow runtime
// executes the real service integrations rather than merely accepting their
// Amazon States Language definitions. Every resource and observation travels
// through an official AWS SDK client at the simulator coordinate.
func TestSFN_EventingAndObservabilityIntegrations_SDK(t *testing.T) {
	queueAPI := sqsClient()
	topicAPI := snsClient()
	eventAPI := eventbridgeClient()
	statesAPI := sfnClient()
	metricsAPI := cloudwatchClient()

	queue, err := queueAPI.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("sfn-integrations")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = queueAPI.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
	attributes, err := queueAPI.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := attributes.Attributes["QueueArn"]
	policy := fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":%q}]}`,
		queueARN,
	)
	_, err = queueAPI.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, Attributes: map[string]string{"Policy": policy},
	})
	require.NoError(t, err)

	topic, err := topicAPI.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("sfn-integrations")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = topicAPI.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })
	_, err = topicAPI.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	ruleName := "sfn-integrations"
	pattern := `{"source":["sfn.integration"]}`
	_, err = eventAPI.PutRule(ctx, &eventbridge.PutRuleInput{
		Name: aws.String(ruleName), EventPattern: aws.String(pattern),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eventAPI.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String(ruleName), Ids: []string{"queue"},
		})
		_, _ = eventAPI.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(ruleName)})
	})
	_, err = eventAPI.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule:    aws.String(ruleName),
		Targets: []eventtypes.Target{{Id: aws.String("queue"), Arn: aws.String(queueARN)}},
	})
	require.NoError(t, err)

	definition, err := json.Marshal(map[string]any{
		"StartAt": "Queue",
		"States": map[string]any{
			"Queue": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::sqs:sendMessage",
				"Parameters": map[string]any{"QueueUrl": aws.ToString(queue.QueueUrl), "MessageBody": "from-step-functions-sqs"},
				"Next":       "Topic",
			},
			"Topic": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::sns:publish",
				"Parameters": map[string]any{"TopicArn": aws.ToString(topic.TopicArn), "Message": "from-step-functions-sns"},
				"Next":       "Event",
			},
			"Event": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::events:putEvents",
				"Parameters": map[string]any{"Entries": []any{map[string]any{
					"Source": "sfn.integration", "DetailType": "Workflow", "Detail": `{"delivered":true}`,
				}}},
				"Next": "Metric",
			},
			"Metric": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::aws-sdk:cloudwatch:putMetricData",
				"Parameters": map[string]any{
					"Namespace":  "Sockerless/StepFunctions",
					"MetricData": []any{map[string]any{"MetricName": "Completed", "Value": 1}},
				},
				"End": true,
			},
		},
	})
	require.NoError(t, err)
	machine, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-service-integrations"), Definition: aws.String(string(definition)),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: machine.StateMachineArn})
	})
	execution, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: machine.StateMachineArn})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		described, describeErr := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: execution.ExecutionArn})
		return describeErr == nil && described.Status == sfntypes.ExecutionStatusSucceeded
	}, 10*time.Second, 100*time.Millisecond)

	var bodies []string
	require.Eventually(t, func() bool {
		received, receiveErr := queueAPI.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 10, VisibilityTimeout: 0,
		})
		if receiveErr != nil {
			return false
		}
		bodies = bodies[:0]
		for _, message := range received.Messages {
			bodies = append(bodies, aws.ToString(message.Body))
		}
		return len(bodies) == 3
	}, 5*time.Second, 100*time.Millisecond)
	assert.Contains(t, bodies, "from-step-functions-sqs")
	assert.Condition(t, func() bool {
		for _, body := range bodies {
			if len(body) > 0 && body[0] == '{' && strings.Contains(body, "from-step-functions-sns") {
				return true
			}
		}
		return false
	}, "Amazon SNS must deliver its notification envelope")
	assert.Condition(t, func() bool {
		for _, body := range bodies {
			if strings.Contains(body, `"delivered":true`) {
				return true
			}
		}
		return false
	}, "Amazon EventBridge must deliver the matched event")

	history, err := statesAPI.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{ExecutionArn: execution.ExecutionArn})
	require.NoError(t, err)
	var succeededTasks int
	for _, event := range history.Events {
		if event.Type == sfntypes.HistoryEventTypeTaskSucceeded {
			succeededTasks++
		}
	}
	assert.Equal(t, 4, succeededTasks, "every external service integration must complete")

	now := time.Now().UTC()
	metrics, err := metricsAPI.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(now.Add(-time.Minute)),
		EndTime:   aws.Time(now.Add(time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("completed"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("Sockerless/StepFunctions"),
					MetricName: aws.String("Completed"),
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Sum"),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, metrics.MetricDataResults, 1)
	assert.Equal(t, []float64{1}, metrics.MetricDataResults[0].Values)
}

// TestSFN_AmazonECSAndCodeBuildIntegrations_SDK proves the optimized
// RunTask.sync and StartBuild.sync resources execute their actual cloud
// workloads. The AWS CodeBuild workload uses the vendor AWS CLI with the
// standard global endpoint coordinate to send a message back through Amazon
// SQS; no simulator-only endpoint or execution branch participates.
func TestSFN_AmazonECSAndCodeBuildIntegrations_SDK(t *testing.T) {
	statesAPI := sfnClient()
	ecsAPI := ecsClient()
	buildAPI := codebuildClient()
	queueAPI := sqsClient()

	const (
		clusterName = "sfn-ecs-integration"
		familyName  = "sfn-ecs-integration"
		projectName = "sfn-codebuild-integration"
	)
	subnetID := createECSTestSubnet(t, "sfn-ecs-integration")
	_, err := ecsAPI.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsAPI.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	taskDefinition, err := ecsAPI.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(familyName),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:    aws.String("work"),
			Image:   aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			Command: []string{"sh", "-c", "printf step-functions-ecs"},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsAPI.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: taskDefinition.TaskDefinition.TaskDefinitionArn,
		})
	})

	queue, err := queueAPI.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("sfn-workload-endpoint")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = queueAPI.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl})
	})
	_, err = buildAPI.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(projectName),
		Source: &cbtypes.ProjectSource{
			Type: cbtypes.SourceTypeNoSource,
			Buildspec: aws.String(`version: 0.2
phases:
  build:
    commands:
      - aws sqs send-message --queue-url "$QUEUE_URL" --message-body from-step-functions-codebuild
`),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/aws-cli/aws-cli:2.27.49"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/codebuild-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = buildAPI.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(projectName)})
	})
	containerEndpoint := fmt.Sprintf("http://host.docker.internal:%d", simPort)
	containerQueueURL := strings.Replace(
		aws.ToString(queue.QueueUrl),
		baseURL,
		containerEndpoint,
		1,
	)

	definition, err := json.Marshal(map[string]any{
		"StartAt": "RunContainer",
		"States": map[string]any{
			"RunContainer": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::ecs:runTask.sync",
				"Parameters": map[string]any{
					"Cluster":        clusterName,
					"TaskDefinition": aws.ToString(taskDefinition.TaskDefinition.TaskDefinitionArn),
					"LaunchType":     "FARGATE",
					"NetworkConfiguration": map[string]any{"AwsvpcConfiguration": map[string]any{
						"Subnets": []string{subnetID},
					}},
				},
				"Next": "RunBuild",
			},
			"RunBuild": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::codebuild:startBuild.sync",
				"Parameters": map[string]any{
					"ProjectName": projectName,
					"EnvironmentVariablesOverride": []any{
						map[string]any{"Name": "AWS_ACCESS_KEY_ID", "Value": "test", "Type": "PLAINTEXT"},
						map[string]any{"Name": "AWS_SECRET_ACCESS_KEY", "Value": "test", "Type": "PLAINTEXT"},
						map[string]any{"Name": "AWS_DEFAULT_REGION", "Value": "us-east-1", "Type": "PLAINTEXT"},
						map[string]any{"Name": "AWS_ENDPOINT_URL", "Value": containerEndpoint, "Type": "PLAINTEXT"},
						map[string]any{"Name": "QUEUE_URL", "Value": containerQueueURL, "Type": "PLAINTEXT"},
					},
				},
				"End": true,
			},
		},
	})
	require.NoError(t, err)
	machine, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-ecs-codebuild-integrations"), Definition: aws.String(string(definition)),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: machine.StateMachineArn})
	})
	execution, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: machine.StateMachineArn})
	require.NoError(t, err)
	// Amazon Elastic Container Service (Amazon ECS) and AWS CodeBuild both
	// provision containers asynchronously. A cold external runner may need
	// longer than one minute to fetch both configured images, just as the real
	// services may spend several minutes in provisioning and queued states.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		described, describeErr := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: execution.ExecutionArn})
		if !assert.NoError(collect, describeErr) {
			return
		}
		assert.Equal(collect, sfntypes.ExecutionStatusSucceeded, described.Status)
		assert.Empty(collect, aws.ToString(described.Error))
		assert.Empty(collect, aws.ToString(described.Cause))
	}, 3*time.Minute, 200*time.Millisecond)

	var deliveredReceipt string
	require.Eventually(t, func() bool {
		received, receiveErr := queueAPI.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 1, VisibilityTimeout: 30,
		})
		if receiveErr != nil || len(received.Messages) != 1 ||
			aws.ToString(received.Messages[0].Body) != "from-step-functions-codebuild" {
			return false
		}
		deliveredReceipt = aws.ToString(received.Messages[0].ReceiptHandle)
		return true
	}, 10*time.Second, 100*time.Millisecond)
	_, err = queueAPI.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: queue.QueueUrl, ReceiptHandle: aws.String(deliveredReceipt),
	})
	require.NoError(t, err)

	const failingProject = "sfn-codebuild-real-failure"
	_, err = buildAPI.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(failingProject),
		Source: &cbtypes.ProjectSource{
			Type: cbtypes.SourceTypeNoSource,
			Buildspec: aws.String(`version: 0.2
phases:
  build:
    commands:
      - test -f /etc/alpine-release
      - exit 7
`),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/codebuild-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = buildAPI.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(failingProject)})
	})
	failingDefinition, err := json.Marshal(map[string]any{
		"StartAt": "FailingBuild",
		"States": map[string]any{
			"FailingBuild": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::codebuild:startBuild.sync",
				"Parameters": map[string]any{"ProjectName": failingProject},
				"End":        true,
			},
		},
	})
	require.NoError(t, err)
	failingMachine, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-codebuild-real-failure"), Definition: aws.String(string(failingDefinition)),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: failingMachine.StateMachineArn})
	})
	failingExecution, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: failingMachine.StateMachineArn,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		described, describeErr := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
			ExecutionArn: failingExecution.ExecutionArn,
		})
		return describeErr == nil && described.Status == sfntypes.ExecutionStatusFailed &&
			aws.ToString(described.Error) == "CodeBuild.BuildFailed"
	}, 30*time.Second, 100*time.Millisecond)

	const cancelledProject = "sfn-codebuild-real-cancellation"
	_, err = buildAPI.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(cancelledProject),
		Source: &cbtypes.ProjectSource{
			Type: cbtypes.SourceTypeNoSource,
			Buildspec: aws.String(`version: 0.2
phases:
  build:
    commands:
      - sleep 5
      - aws sqs send-message --queue-url "$QUEUE_URL" --message-body cancelled-build-leaked
`),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/aws-cli/aws-cli:2.27.49"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/codebuild-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = buildAPI.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(cancelledProject)})
	})
	cancelDefinition, err := json.Marshal(map[string]any{
		"StartAt": "LongBuild",
		"States": map[string]any{
			"LongBuild": map[string]any{
				"Type": "Task", "Resource": "arn:aws:states:::codebuild:startBuild.sync",
				"Parameters": map[string]any{
					"ProjectName": cancelledProject,
					"EnvironmentVariablesOverride": []any{
						map[string]any{"Name": "AWS_ACCESS_KEY_ID", "Value": "test", "Type": "PLAINTEXT"},
						map[string]any{"Name": "AWS_SECRET_ACCESS_KEY", "Value": "test", "Type": "PLAINTEXT"},
						map[string]any{"Name": "AWS_DEFAULT_REGION", "Value": "us-east-1", "Type": "PLAINTEXT"},
						map[string]any{"Name": "AWS_ENDPOINT_URL", "Value": containerEndpoint, "Type": "PLAINTEXT"},
						map[string]any{"Name": "QUEUE_URL", "Value": containerQueueURL, "Type": "PLAINTEXT"},
					},
				},
				"End": true,
			},
		},
	})
	require.NoError(t, err)
	cancelMachine, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-codebuild-real-cancellation"), Definition: aws.String(string(cancelDefinition)),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: cancelMachine.StateMachineArn})
	})
	cancelExecution, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: cancelMachine.StateMachineArn,
	})
	require.NoError(t, err)
	var cancelledBuildID string
	require.Eventually(t, func() bool {
		listed, listErr := buildAPI.ListBuildsForProject(ctx, &codebuild.ListBuildsForProjectInput{
			ProjectName: aws.String(cancelledProject),
		})
		if listErr != nil || len(listed.Ids) == 0 {
			return false
		}
		cancelledBuildID = listed.Ids[0]
		builds, getErr := buildAPI.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{cancelledBuildID}})
		return getErr == nil && len(builds.Builds) == 1 && builds.Builds[0].BuildStatus == cbtypes.StatusTypeInProgress
	}, 20*time.Second, 100*time.Millisecond)
	_, err = statesAPI.StopExecution(ctx, &sfn.StopExecutionInput{
		ExecutionArn: cancelExecution.ExecutionArn,
		Error:        aws.String("OperatorCancelled"),
		Cause:        aws.String("external cancellation validation"),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		builds, getErr := buildAPI.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{cancelledBuildID}})
		return getErr == nil && len(builds.Builds) == 1 && builds.Builds[0].BuildStatus == cbtypes.StatusTypeStopped
	}, 10*time.Second, 100*time.Millisecond)
	time.Sleep(6 * time.Second)
	received, err := queueAPI.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 10, VisibilityTimeout: 0,
	})
	require.NoError(t, err)
	assert.Empty(t, received.Messages, "a stopped CodeBuild container must not complete its delayed Amazon SQS write")
}

// TestSFN_AmazonECSRunsTerraformAgainstSimulator_SDK is the consumer-side
// proof that an infrastructure-as-code provider running inside an Amazon ECS
// workload can apply resources back to the cloud that launched it. Terraform
// uses the AWS provider's standard AWS_ENDPOINT_URL coordinate; the state
// machine supplies no simulator-aware path, provider block, or custom broker.
func TestSFN_AmazonECSRunsTerraformAgainstSimulator_SDK(t *testing.T) {
	statesAPI := sfnClient()
	ecsAPI := ecsClient()
	queueAPI := sqsClient()
	logsAPI := cwLogsClient()

	const (
		clusterName = "sfn-terraform-apply"
		familyName  = "sfn-terraform-apply"
		queueName   = "sfn-terraform-apply"
		logGroup    = "/ecs/sfn-terraform-apply"
	)
	subnetID := createECSTestSubnet(t, "sfn-terraform-apply")
	_, err := ecsAPI.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsAPI.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})

	const terraformConfiguration = `
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.50.0"
    }
  }
}

provider "aws" {
  region                      = "us-east-1"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_region_validation      = true
  skip_requesting_account_id  = true
}

resource "aws_sqs_queue" "proof" {
  name = "sfn-terraform-apply"

  tags = {
    ProvisionedBy = "Terraform inside AWS Step Functions launched Amazon ECS"
  }
}
`
	containerEndpoint := fmt.Sprintf("http://host.docker.internal:%d", simPort)
	taskDefinition, err := ecsAPI.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(familyName),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("512"),
		Memory:                  aws.String("1024"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("terraform"),
			Image:      aws.String(terraformECSImage),
			EntryPoint: []string{"sh", "-c"},
			Command: []string{
				`set -eu
mkdir -p /workspace
cd /workspace
printf '%s' "$TF_CONFIGURATION" > main.tf
timeout -s TERM 120 terraform init -input=false -no-color
timeout -s TERM 120 terraform apply -auto-approve -input=false -no-color`,
			},
			Environment: []ecstypes.KeyValuePair{
				{Name: aws.String("AWS_ACCESS_KEY_ID"), Value: aws.String("test")},
				{Name: aws.String("AWS_SECRET_ACCESS_KEY"), Value: aws.String("test")},
				{Name: aws.String("AWS_DEFAULT_REGION"), Value: aws.String("us-east-1")},
				{Name: aws.String("AWS_ENDPOINT_URL"), Value: aws.String(containerEndpoint)},
				{Name: aws.String("TF_IN_AUTOMATION"), Value: aws.String("true")},
				{Name: aws.String("TF_CLI_CONFIG_FILE"), Value: aws.String("/terraformrc")},
				// Prove provider installation is fully offline: any accidental
				// HTTPS registry access fails instead of borrowing host egress.
				// The simulator is the task's declared cloud endpoint, so it
				// must bypass environment proxies just as localhost does.
				{Name: aws.String("HTTPS_PROXY"), Value: aws.String("http://127.0.0.1:1")},
				{Name: aws.String("NO_PROXY"), Value: aws.String("host.docker.internal,169.254.170.2")},
				{Name: aws.String("CHECKPOINT_DISABLE"), Value: aws.String("1")},
				{Name: aws.String("TF_CONFIGURATION"), Value: aws.String(terraformConfiguration)},
			},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroup,
					"awslogs-region":        "us-east-1",
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsAPI.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: taskDefinition.TaskDefinition.TaskDefinitionArn,
		})
		_, _ = logsAPI.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
			LogGroupName: aws.String(logGroup),
		})
	})

	definition, err := json.Marshal(map[string]any{
		"StartAt": "ApplyInfrastructure",
		"States": map[string]any{
			"ApplyInfrastructure": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::ecs:runTask.sync",
				"Parameters": map[string]any{
					"Cluster":        clusterName,
					"TaskDefinition": aws.ToString(taskDefinition.TaskDefinition.TaskDefinitionArn),
					"LaunchType":     "FARGATE",
					"NetworkConfiguration": map[string]any{"AwsvpcConfiguration": map[string]any{
						"Subnets": []string{subnetID},
					}},
				},
				"End": true,
			},
		},
	})
	require.NoError(t, err)
	machine, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-terraform-apply"),
		Definition: aws.String(string(definition)),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
			StateMachineArn: machine.StateMachineArn,
		})
	})
	execution, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: machine.StateMachineArn,
	})
	require.NoError(t, err)
	taskOutput := func() string {
		logs, logsErr := logsAPI.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName: aws.String(logGroup),
		})
		if logsErr != nil {
			return fmt.Sprintf("failed to read task output: %v", logsErr)
		}
		var messages []string
		for _, event := range logs.Events {
			messages = append(messages, aws.ToString(event.Message))
		}
		return strings.Join(messages, "\n")
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		described, describeErr := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
			ExecutionArn: execution.ExecutionArn,
		})
		require.NoError(t, describeErr)
		switch described.Status {
		case sfntypes.ExecutionStatusSucceeded:
			goto executionSucceeded
		case sfntypes.ExecutionStatusFailed, sfntypes.ExecutionStatusAborted, sfntypes.ExecutionStatusTimedOut:
			t.Fatalf(
				"Terraform workflow reached %s: %s: %s\ncontainer output:\n%s",
				described.Status,
				aws.ToString(described.Error),
				aws.ToString(described.Cause),
				taskOutput(),
			)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf(
				"Terraform workflow remained %s after five minutes\ncontainer output:\n%s",
				described.Status,
				taskOutput(),
			)
		}
		time.Sleep(500 * time.Millisecond)
	}

executionSucceeded:
	taskLogs, err := logsAPI.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	var taskMessages []string
	for _, event := range taskLogs.Events {
		taskMessages = append(taskMessages, aws.ToString(event.Message))
	}
	assert.Contains(t, strings.Join(taskMessages, "\n"), "Apply complete! Resources: 1 added")

	queues, err := queueAPI.ListQueues(ctx, &sqs.ListQueuesInput{QueueNamePrefix: aws.String(queueName)})
	require.NoError(t, err)
	require.Len(t, queues.QueueUrls, 1)
	t.Cleanup(func() {
		_, _ = queueAPI.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queues.QueueUrls[0])})
	})
	attributes, err := queueAPI.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(queues.QueueUrls[0])})
	require.NoError(t, err)
	assert.Equal(t, "Terraform inside AWS Step Functions launched Amazon ECS", attributes.Tags["ProvisionedBy"])
}
