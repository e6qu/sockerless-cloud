package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_ResourceARN_DynamoDB expects the gate to derive an awsJson request's
// resource ARN from the body's TableName, so a policy scoped to one table
// allows it and denies another.
func TestIAM_ResourceARN_DynamoDB(t *testing.T) {
	t.Parallel()
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
	scopedTable, otherTable := uniquename.New("scoped-table"), uniquename.New("other-table")
	mkTable(scopedTable)
	mkTable(otherTable)

	user := uniquename.New("ddb-scoped-user")
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-table"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:PutItem",` +
			`"Resource":"arn:aws:dynamodb:us-east-1:123456789012:table/` + scopedTable + `"}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	ddb := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	item := map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "x"}}
	_, err = ddb.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(scopedTable), Item: item})
	assert.NoError(t, err, "PutItem on the granted table ARN must succeed")

	_, err = ddb.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(otherTable), Item: item})
	require.Error(t, err, "PutItem on a different table must be denied by the resource-scoped policy")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// TestIAM_ResourceARN_DynamoDBTransactionsAndBatches expects per-item table ARNs
// for TransactWriteItems, TransactGetItems, BatchWriteItem and BatchGetItem,
// whose table names sit inside TransactItems or RequestItems: a policy scoped
// to one table allows single-table transactions and denies any touching another
// table.
func TestIAM_ResourceARN_DynamoDBTransactionsAndBatches(t *testing.T) {
	t.Parallel()
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
	scopedTable, otherTable := uniquename.New("txn-scoped-table"), uniquename.New("txn-other-table")
	mkTable(scopedTable)
	mkTable(otherTable)

	user := uniquename.New("ddb-txn-scoped-user")
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-table-txn"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Action":["dynamodb:PutItem","dynamodb:GetItem","dynamodb:BatchWriteItem","dynamodb:BatchGetItem"],` +
			`"Resource":["arn:aws:dynamodb:us-east-1:123456789012:table/` + scopedTable + `",` +
			`"arn:aws:dynamodb:us-east-1:123456789012:table/` + scopedTable + `/index/*"]}]}`),
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
			{Put: &ddbtypes.Put{TableName: aws.String(scopedTable), Item: item}},
		},
	})
	assert.NoError(t, err, "TransactWriteItems on the granted table ARN must succeed")

	_, err = ddb.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{
		TransactItems: []ddbtypes.TransactGetItem{
			{Get: &ddbtypes.Get{TableName: aws.String(scopedTable), Key: keyAttr}},
		},
	})
	assert.NoError(t, err, "TransactGetItems on the granted table ARN must succeed")

	_, err = ddb.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]ddbtypes.WriteRequest{
			scopedTable: {{PutRequest: &ddbtypes.PutRequest{Item: item}}},
		},
	})
	assert.NoError(t, err, "BatchWriteItem on the granted table ARN must succeed")

	_, err = ddb.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]ddbtypes.KeysAndAttributes{
			scopedTable: {Keys: []map[string]ddbtypes.AttributeValue{keyAttr}},
		},
	})
	assert.NoError(t, err, "BatchGetItem on the granted table ARN must succeed")

	_, err = ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{Put: &ddbtypes.Put{TableName: aws.String(scopedTable), Item: item}},
			{Put: &ddbtypes.Put{TableName: aws.String(otherTable), Item: item}},
		},
	})
	require.Error(t, err, "a transaction touching an ungranted table must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))

	_, err = ddb.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]ddbtypes.KeysAndAttributes{
			otherTable: {Keys: []map[string]ddbtypes.AttributeValue{keyAttr}},
		},
	})
	require.Error(t, err, "a batch get on an ungranted table must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// TestIAM_ResourceARN_ECR expects the gate to derive an Amazon ECR request's
// repository ARN from the body, so read access to one repository namespace
// allows the repositories under the prefix, nested names included (IAM's `*`
// spans slashes), and denies the rest.
func TestIAM_ResourceARN_ECR(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	ecrAdmin := ecrClient()

	scopedNS, otherNS := uniquename.New("scoped-ns"), uniquename.New("other-ns")
	for _, name := range []string{scopedNS + "/control-plane", scopedNS + "/golden/omnibus", otherNS + "/control-plane"} {
		_, err := ecrAdmin.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(name)})
		require.NoError(t, err)
	}

	user := uniquename.New("ecr-scoped-user")
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-namespace"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Action":["ecr:DescribeImages","ecr:ListImages"],` +
			`"Resource":"arn:aws:ecr:us-east-1:123456789012:repository/` + scopedNS + `/*"}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	scoped := ecr.NewFromConfig(cfg, func(o *ecr.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = scoped.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String(scopedNS + "/control-plane")})
	assert.NoError(t, err, "DescribeImages on a repository under the granted prefix must succeed")

	_, err = scoped.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String(scopedNS + "/golden/omnibus")})
	assert.NoError(t, err, "a nested repository name under the granted prefix must succeed")

	_, err = scoped.ListImages(ctx, &ecr.ListImagesInput{RepositoryName: aws.String(scopedNS + "/control-plane")})
	assert.NoError(t, err, "ListImages on a repository under the granted prefix must succeed")

	_, err = scoped.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String(otherNS + "/control-plane")})
	require.Error(t, err, "a repository outside the granted prefix must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}
