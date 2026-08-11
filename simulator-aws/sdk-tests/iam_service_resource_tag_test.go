package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests prove the gate resolves aws:ResourceTag/<k> for services beyond
// EC2/ECS: a grant scoped to a resource's tag is denied until the targeted
// resource actually carries the tag, then allowed. They cover the major
// protocols the resolver handles — SQS (awsJson), SNS (awsQuery), and DynamoDB
// (awsJson) — mirroring TestIAM_ResourceTagCondition.
//
// The resolver (iam_service_resource_tags.go) covers, in addition to EC2/ECS:
//   lambda, sqs, sns, rds, elbv2, elasticache, dynamodb, ecr, logs,
//   stepfunctions, kms, secretsmanager, kinesis, glue, batch, s3.

// restrictedConfig mints a registered IAM user with the given inline policy and
// returns an aws.Config bearing its access key.
func restrictedConfig(t *testing.T, iamc *iam.Client, user, policyName, policyDoc string) aws.Config {
	t.Helper()
	_, err := iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policyDoc),
	})
	require.NoError(t, err)
	key, err := iamc.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	return aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
}

// TestIAM_SQSResourceTagCondition: sqs:DeleteQueue scoped to
// aws:ResourceTag/team=blue is denied on an untagged queue, allowed once tagged.
func TestIAM_SQSResourceTagCondition(t *testing.T) {
	admin := sqsClient()
	iamc := iamClient()

	q, err := admin.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("rt-sqs-q")})
	require.NoError(t, err)
	qurl := aws.ToString(q.QueueUrl)

	cfg := restrictedConfig(t, iamc, "rt-sqs-user", "sqs-scoped",
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":["sqs:DeleteQueue"],"Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/team":"blue"}}}]}`)
	rc := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// Untagged → denied.
	_, err = rc.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(qurl)})
	require.Error(t, err, "DeleteQueue on an untagged queue must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))

	// Tag it team=blue (admin).
	_, err = admin.TagQueue(ctx, &sqs.TagQueueInput{QueueUrl: aws.String(qurl), Tags: map[string]string{"team": "blue"}})
	require.NoError(t, err)

	// Now allowed.
	_, err = rc.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(qurl)})
	assert.NoError(t, err, "DeleteQueue must be allowed once the queue carries team=blue")
}

// TestIAM_SNSResourceTagCondition: sns:DeleteTopic scoped to
// aws:ResourceTag/env=prod (awsQuery protocol).
func TestIAM_SNSResourceTagCondition(t *testing.T) {
	admin := snsClient()
	iamc := iamClient()

	topic, err := admin.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("rt-sns-topic")})
	require.NoError(t, err)
	arn := aws.ToString(topic.TopicArn)

	cfg := restrictedConfig(t, iamc, "rt-sns-user", "sns-scoped",
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":["sns:DeleteTopic"],"Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/env":"prod"}}}]}`)
	rc := sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// Untagged → denied (query protocol → AccessDenied).
	_, err = rc.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(arn)})
	require.Error(t, err, "DeleteTopic on an untagged topic must be denied")
	assert.Equal(t, "AccessDenied", errCodeOf(err))

	// Tag it env=prod.
	_, err = admin.TagResource(ctx, &sns.TagResourceInput{ResourceArn: aws.String(arn),
		Tags: []snstypes.Tag{{Key: aws.String("env"), Value: aws.String("prod")}}})
	require.NoError(t, err)

	_, err = rc.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(arn)})
	assert.NoError(t, err, "DeleteTopic must be allowed once the topic carries env=prod")
}

// TestIAM_DynamoDBResourceTagCondition: dynamodb:DeleteTable scoped to
// aws:ResourceTag/owner=x (awsJson protocol).
func TestIAM_DynamoDBResourceTagCondition(t *testing.T) {
	admin := ddbClient()
	iamc := iamClient()

	tbl := "rt-ddb-table"
	out, err := admin.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(tbl),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
	})
	require.NoError(t, err)
	tableArn := aws.ToString(out.TableDescription.TableArn)

	cfg := restrictedConfig(t, iamc, "rt-ddb-user", "ddb-scoped",
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":["dynamodb:DeleteTable"],"Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/owner":"x"}}}]}`)
	rc := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// Untagged → denied.
	_, err = rc.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tbl)})
	require.Error(t, err, "DeleteTable on an untagged table must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))

	// Tag it owner=x.
	_, err = admin.TagResource(ctx, &dynamodb.TagResourceInput{ResourceArn: aws.String(tableArn),
		Tags: []ddbtypes.Tag{{Key: aws.String("owner"), Value: aws.String("x")}}})
	require.NoError(t, err)

	_, err = rc.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tbl)})
	assert.NoError(t, err, "DeleteTable must be allowed once the table carries owner=x")
}
