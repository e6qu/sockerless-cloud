package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A policy grants IAM actions, not API operations: listing a bucket is
// s3:ListBucket, a multipart upload is s3:PutObject, and a copy also reads its
// source with s3:GetObject.
func TestS3_OperationsAreAuthorizedByTheirIAMActions(t *testing.T) {
	admin := s3Client()
	for _, bucket := range []string{"iam-action-names-src", "iam-action-names-dst"} {
		_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
	}
	_, err := admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("iam-action-names-src"), Key: aws.String("secret"), Body: strings.NewReader("x")})
	require.NoError(t, err)

	akid, secret := restrictedCredential(t, "s3-action-names",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::iam-action-names-dst"},
		  {"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::iam-action-names-dst/*"}]}`)
	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	_, err = restricted.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("iam-action-names-dst")})
	assert.NoError(t, err, "s3:ListBucket grants ListObjectsV2")

	upload, err := restricted.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String("iam-action-names-dst"), Key: aws.String("big")})
	require.NoError(t, err, "s3:PutObject grants CreateMultipartUpload")
	_, err = restricted.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String("iam-action-names-dst"), Key: aws.String("big"), UploadId: upload.UploadId})
	assert.Error(t, err, "s3:AbortMultipartUpload is its own action, which the policy does not grant")

	_, err = restricted.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String("iam-action-names-dst"), Key: aws.String("copy"),
		CopySource: aws.String("iam-action-names-src/secret")})
	require.Error(t, err, "a copy needs s3:GetObject on its source")
	assert.Contains(t, err.Error(), "s3:GetObject")
}

// PartiQL statements are authorized as the verb they are.
func TestDynamoDB_PartiQLIsAuthorizedByItsVerb(t *testing.T) {
	admin := dynamodb.NewFromConfig(sdkConfig(), func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err := admin.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String("partiql-verbs"),
		BillingMode:          ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String("partiql-verbs")})
	})

	akid, secret := restrictedCredential(t, "partiql-select-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:PartiQLSelect",
		  "Resource":"arn:aws:dynamodb:us-east-1:123456789012:table/partiql-verbs"}]}`)
	restricted := dynamodb.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement: aws.String(`SELECT * FROM "partiql-verbs"`)})
	assert.NoError(t, err, "dynamodb:PartiQLSelect grants a SELECT")

	_, err = restricted.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement: aws.String(`INSERT INTO "partiql-verbs" VALUE {'id':'1'}`)})
	require.Error(t, err, "an INSERT needs dynamodb:PartiQLInsert")
	assert.Contains(t, err.Error(), "PartiQLInsert")
}
