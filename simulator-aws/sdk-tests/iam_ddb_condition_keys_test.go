package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamoDB_LeadingKeysAndAttributesScopeTheGrant covers Amazon DynamoDB's
// fine-grained access control: the pair of condition keys a policy uses to let
// a principal reach only its own rows (dynamodb:LeadingKeys, the partition-key
// values a request may name) and only some of their columns
// (dynamodb:Attributes). Both are settled by the request.
func TestDynamoDB_LeadingKeysAndAttributesScopeTheGrant(t *testing.T) {
	admin := ddbClient()
	table := "ddb-fine-grained"
	_, err := admin.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("owner"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("owner"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })

	for _, owner := range []string{"alice", "bob"} {
		_, err = admin.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table),
			Item: map[string]ddbtypes.AttributeValue{
				"owner":   &ddbtypes.AttributeValueMemberS{Value: owner},
				"profile": &ddbtypes.AttributeValueMemberS{Value: "public"},
				"secret":  &ddbtypes.AttributeValueMemberS{Value: "private"},
			}})
		require.NoError(t, err)
	}

	// The policy AWS documents for this: read your own row, and only the
	// columns you are allowed to see.
	akid, secret := restrictedCredential(t, "ddb-alice",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:GetItem","Resource":"*",
		  "Condition":{
		    "ForAllValues:StringEquals":{"dynamodb:LeadingKeys":["alice"],
		                                 "dynamodb:Attributes":["owner","profile"]}}}]}`)
	alice := dynamodb.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = alice.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table),
		Key:             map[string]ddbtypes.AttributeValue{"owner": &ddbtypes.AttributeValueMemberS{Value: "alice"}},
		AttributesToGet: []string{"owner", "profile"}})
	assert.NoError(t, err, "her own row, and the columns the grant names")

	_, err = alice.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table),
		Key:             map[string]ddbtypes.AttributeValue{"owner": &ddbtypes.AttributeValueMemberS{Value: "bob"}},
		AttributesToGet: []string{"owner", "profile"}})
	require.Error(t, err, "another principal's row is outside the leading keys the grant allows")
	assert.Contains(t, err.Error(), "not authorized")

	_, err = alice.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table),
		Key:             map[string]ddbtypes.AttributeValue{"owner": &ddbtypes.AttributeValueMemberS{Value: "alice"}},
		AttributesToGet: []string{"owner", "secret"}})
	require.Error(t, err, "a column the grant does not name is refused even on her own row")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestKMS_EncryptionContextConditionKeyScopesTheGrant covers
// kms:EncryptionContext:<key>, which is how a policy allows a key to be used
// only for data labelled a particular way — the label travels with the request
// and is bound into the ciphertext.
func TestKMS_EncryptionContextConditionKeyScopesTheGrant(t *testing.T) {
	admin := kmsClient()
	key, err := admin.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("encryption context")})
	require.NoError(t, err)
	keyID := aws.ToString(key.KeyMetadata.KeyId)

	akid, secret := restrictedCredential(t, "kms-one-context",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Encrypt","Resource":"*",
		  "Condition":{"StringEquals":{"kms:EncryptionContext:tenant":"acme"}}}]}`)
	restricted := kms.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *kms.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(keyID),
		Plaintext: []byte("payload"), EncryptionContext: map[string]string{"tenant": "acme"}})
	assert.NoError(t, err, "the request carries the encryption context the grant names")

	_, err = restricted.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(keyID),
		Plaintext: []byte("payload"), EncryptionContext: map[string]string{"tenant": "other"}})
	require.Error(t, err, "another tenant's context is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}
