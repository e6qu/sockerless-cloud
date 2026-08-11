package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_ResourceARN_DynamoDB proves the enforcement gate derives a request's
// resource ARN for an awsJson service (DynamoDB, from the body's TableName), so a
// policy scoped to one table's ARN allows that table and denies another.
func TestIAM_ResourceARN_DynamoDB(t *testing.T) {
	admin := iamClient()
	ddbAdmin := dynamodb.NewFromConfig(sdkConfig(), func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	mkTable := func(name string) {
		_, err := ddbAdmin.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:            aws.String(name),
			BillingMode:          ddbtypes.BillingModePayPerRequest,
			AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
			KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
		})
		require.NoError(t, err)
	}
	mkTable("scoped-table")
	mkTable("other-table")

	user := "ddb-scoped-user"
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-table"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:PutItem",` +
			`"Resource":"arn:aws:dynamodb:us-east-1:123456789012:table/scoped-table"}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	ddb := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	item := map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "x"}}
	_, err = ddb.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("scoped-table"), Item: item})
	assert.NoError(t, err, "PutItem on the granted table ARN must succeed")

	_, err = ddb.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("other-table"), Item: item})
	require.Error(t, err, "PutItem on a different table must be denied by the resource-scoped policy")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// TestIAM_ResourceARN_DynamoDBTransactionsAndBatches proves the enforcement
// gate derives per-item table ARNs for TransactWriteItems, TransactGetItems,
// BatchWriteItem, and BatchGetItem — which carry table names inside
// TransactItems / RequestItems, not top-level — so the standard least-privilege
// pattern (Resource scoped to one table ARN) allows single-table transactions
// and denies any that touch an ungranted table (GitHub issue #870).
func TestIAM_ResourceARN_DynamoDBTransactionsAndBatches(t *testing.T) {
	admin := iamClient()
	ddbAdmin := dynamodb.NewFromConfig(sdkConfig(), func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	mkTable := func(name string) {
		_, err := ddbAdmin.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:            aws.String(name),
			BillingMode:          ddbtypes.BillingModePayPerRequest,
			AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
			KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
		})
		require.NoError(t, err)
	}
	mkTable("txn-scoped-table")
	mkTable("txn-other-table")

	user := "ddb-txn-scoped-user"
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-table-txn"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Action":["dynamodb:TransactWriteItems","dynamodb:TransactGetItems","dynamodb:BatchWriteItem","dynamodb:BatchGetItem"],` +
			`"Resource":["arn:aws:dynamodb:us-east-1:123456789012:table/txn-scoped-table",` +
			`"arn:aws:dynamodb:us-east-1:123456789012:table/txn-scoped-table/index/*"]}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	ddb := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	item := map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "t1"}}
	keyAttr := map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "t1"}}

	_, err = ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{Put: &ddbtypes.Put{TableName: aws.String("txn-scoped-table"), Item: item}},
		},
	})
	assert.NoError(t, err, "TransactWriteItems on the granted table ARN must succeed")

	_, err = ddb.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{
		TransactItems: []ddbtypes.TransactGetItem{
			{Get: &ddbtypes.Get{TableName: aws.String("txn-scoped-table"), Key: keyAttr}},
		},
	})
	assert.NoError(t, err, "TransactGetItems on the granted table ARN must succeed")

	_, err = ddb.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]ddbtypes.WriteRequest{
			"txn-scoped-table": {{PutRequest: &ddbtypes.PutRequest{Item: item}}},
		},
	})
	assert.NoError(t, err, "BatchWriteItem on the granted table ARN must succeed")

	_, err = ddb.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]ddbtypes.KeysAndAttributes{
			"txn-scoped-table": {Keys: []map[string]ddbtypes.AttributeValue{keyAttr}},
		},
	})
	assert.NoError(t, err, "BatchGetItem on the granted table ARN must succeed")

	_, err = ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{Put: &ddbtypes.Put{TableName: aws.String("txn-scoped-table"), Item: item}},
			{Put: &ddbtypes.Put{TableName: aws.String("txn-other-table"), Item: item}},
		},
	})
	require.Error(t, err, "a transaction touching an ungranted table must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))

	_, err = ddb.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]ddbtypes.KeysAndAttributes{
			"txn-other-table": {Keys: []map[string]ddbtypes.AttributeValue{keyAttr}},
		},
	})
	require.Error(t, err, "a batch get on an ungranted table must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// TestIAM_ResourceARN_ECR proves the enforcement gate derives an Amazon
// Elastic Container Registry request's repository ARN from the body, so the
// least-privilege pattern a control plane writes — read access to one
// repository namespace — allows the repositories under that prefix and denies
// the rest. Repository names contain slashes and IAM's `*` spans them, so a
// nested name under the granted prefix is allowed too.
func TestIAM_ResourceARN_ECR(t *testing.T) {
	admin := iamClient()
	ecrAdmin := ecrClient()

	for _, name := range []string{"scoped-ns/control-plane", "scoped-ns/golden/omnibus", "other-ns/control-plane"} {
		_, err := ecrAdmin.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(name)})
		require.NoError(t, err)
	}

	user := "ecr-scoped-user"
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-namespace"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Action":["ecr:DescribeImages","ecr:ListImages"],` +
			`"Resource":"arn:aws:ecr:us-east-1:123456789012:repository/scoped-ns/*"}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	scoped := ecr.NewFromConfig(cfg, func(o *ecr.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = scoped.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String("scoped-ns/control-plane")})
	assert.NoError(t, err, "DescribeImages on a repository under the granted prefix must succeed")

	_, err = scoped.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String("scoped-ns/golden/omnibus")})
	assert.NoError(t, err, "a nested repository name under the granted prefix must succeed")

	_, err = scoped.ListImages(ctx, &ecr.ListImagesInput{RepositoryName: aws.String("scoped-ns/control-plane")})
	assert.NoError(t, err, "ListImages on a repository under the granted prefix must succeed")

	_, err = scoped.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String("other-ns/control-plane")})
	require.Error(t, err, "a repository outside the granted prefix must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}
