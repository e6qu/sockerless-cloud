package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSM_DocumentTypeConditionKeyScopesTheGrant covers ssm:DocumentType, read
// from the document a request names — the key a policy uses to allow one kind
// of document and refuse another.
func TestSSM_DocumentTypeConditionKeyScopesTheGrant(t *testing.T) {
	admin := ssmClient()
	documents := map[string]ssmtypes.DocumentType{
		"cond-automation": ssmtypes.DocumentTypeAutomation,
		"cond-command":    ssmtypes.DocumentTypeCommand,
	}
	content := map[ssmtypes.DocumentType]string{
		ssmtypes.DocumentTypeAutomation: `{"schemaVersion":"0.3","mainSteps":[]}`,
		ssmtypes.DocumentTypeCommand:    `{"schemaVersion":"2.2","mainSteps":[]}`,
	}
	for name, kind := range documents {
		_, err := admin.CreateDocument(ctx, &ssm.CreateDocumentInput{
			Name: aws.String(name), DocumentType: kind,
			DocumentFormat: ssmtypes.DocumentFormatJson,
			Content:        aws.String(content[kind])})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = admin.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(name)}) })
	}

	akid, secret := restrictedCredential(t, "ssm-automation-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:DescribeDocument",
		  "Resource":"*","Condition":{"StringEquals":{"ssm:DocumentType":"Automation"}}}]}`)
	restricted := ssm.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *ssm.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.DescribeDocument(ctx, &ssm.DescribeDocumentInput{Name: aws.String("cond-automation")})
	assert.NoError(t, err, "the document is the type the grant names")

	_, err = restricted.DescribeDocument(ctx, &ssm.DescribeDocumentInput{Name: aws.String("cond-command")})
	require.Error(t, err, "a document of another type is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestEventBridge_CreatorAccountConditionKeyScopesTheGrant covers
// events:creatorAccount, the account that created the rule a request names.
func TestEventBridge_CreatorAccountConditionKeyScopesTheGrant(t *testing.T) {
	admin := eventbridgeClient()
	const rule = "cond-creator-rule"
	_, err := admin.PutRule(ctx, &eventbridge.PutRuleInput{
		Name: aws.String(rule), EventPattern: aws.String(`{"source":["cond"]}`)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rule)}) })

	allowed := restrictedEventBridge(t, "eb-own-account", s3ObjectLambdaAccount)
	_, err = allowed.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(rule)})
	assert.NoError(t, err, "the rule was created by the account the grant names")

	refused := restrictedEventBridge(t, "eb-other-account", "999999999999")
	_, err = refused.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(rule)})
	require.Error(t, err, "a rule created by another account is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

func restrictedEventBridge(t *testing.T, user, account string) *eventbridge.Client {
	t.Helper()
	akid, secret := restrictedCredential(t, user,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"events:DescribeRule",
		  "Resource":"*","Condition":{"StringEquals":{"events:creatorAccount":"`+account+`"}}}]}`)
	return eventbridge.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *eventbridge.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

// TestECS_PropagateTagsConditionKeyScopesTheGrant covers ecs:propagate-tags,
// where a request asks Amazon ECS to copy tags from — the key a policy uses to
// require that they come from the service rather than the task definition.
func TestECS_PropagateTagsConditionKeyScopesTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "ecs-service-tags-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:CreateService",
		  "Resource":"*","Condition":{"StringEquals":{"ecs:propagate-tags":"SERVICE"}}}]}`)
	restricted := ecs.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// The grant is what is under test, so the call is judged by whether it was
	// authorized: a request that clears IAM fails afterwards on the cluster or
	// task definition it names, which is a different error.
	_, err := restricted.CreateService(ctx, &ecs.CreateServiceInput{
		ServiceName: aws.String("cond-tagged-service"), Cluster: aws.String("cond-absent-cluster"),
		TaskDefinition: aws.String("cond-absent-task"),
		PropagateTags:  ecstypes.PropagateTagsService})
	if err != nil {
		assert.NotContains(t, err.Error(), "not authorized",
			"the request propagates tags the way the grant requires, so IAM must not refuse it")
	}

	_, err = restricted.CreateService(ctx, &ecs.CreateServiceInput{
		ServiceName: aws.String("cond-untagged-service"), Cluster: aws.String("cond-absent-cluster"),
		TaskDefinition: aws.String("cond-absent-task"),
		PropagateTags:  ecstypes.PropagateTagsTaskDefinition})
	require.Error(t, err, "propagating from somewhere else is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestRDS_ManageMasterUserPasswordConditionKeyScopesTheGrant covers
// rds:ManageMasterUserPassword, which a policy uses to require that a master
// password be managed in AWS Secrets Manager rather than supplied by hand.
func TestRDS_ManageMasterUserPasswordConditionKeyScopesTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "rds-managed-passwords-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"rds:ModifyDBInstance",
		  "Resource":"*","Condition":{"Bool":{"rds:ManageMasterUserPassword":"true"}}}]}`)
	restricted := rds.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *rds.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// Authorization is what is under test: a request that clears it fails on
	// the instance it names, which is a different error from a refusal.
	_, err := restricted.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier:     aws.String("cond-absent-instance"),
		ManageMasterUserPassword: aws.Bool(true)})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not authorized",
		"the request asks for a managed password, which is what the grant requires")

	_, err = restricted.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String("cond-absent-instance")})
	require.Error(t, err, "a request that does not ask for a managed password is not covered")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestSFN_StateMachineQualifierConditionKeyScopesTheGrant covers
// states:StateMachineQualifier, the version or alias a request names — the key
// a policy uses to allow a call against one published version and not another.
func TestSFN_StateMachineQualifierConditionKeyScopesTheGrant(t *testing.T) {
	admin := sfnClient()
	machine, err := admin.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("cond-qualifier-sm"),
		Definition: aws.String(`{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: machine.StateMachineArn})
	})
	published, err := admin.PublishStateMachineVersion(ctx, &sfn.PublishStateMachineVersionInput{
		StateMachineArn: machine.StateMachineArn})
	require.NoError(t, err)
	versionARN := aws.ToString(published.StateMachineVersionArn)

	akid, secret := restrictedCredential(t, "sfn-version-one-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"states:DescribeStateMachine",
		  "Resource":"*","Condition":{"StringEquals":{"states:StateMachineQualifier":"1"}}}]}`)
	restricted := sfn.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *sfn.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
		StateMachineArn: aws.String(versionARN)})
	assert.NoError(t, err, "the request names the version the grant allows")

	_, err = restricted.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
		StateMachineArn: machine.StateMachineArn})
	require.Error(t, err, "the unqualified state machine carries no qualifier, so the grant does not match")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestLambda_FunctionArnConditionKeyScopesTheGrant covers lambda:FunctionArn,
// the function an event-source mapping or a function URL is about — the key a
// policy uses to let a principal wire up one function and not another.
func TestLambda_FunctionArnConditionKeyScopesTheGrant(t *testing.T) {
	admin := lambdaClient()
	permitted := lambdaConditionFunction(t, admin, "cond-url-permitted")
	refused := lambdaConditionFunction(t, admin, "cond-url-refused")

	akid, secret := restrictedCredential(t, "lambda-one-function",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateFunctionUrlConfig",
		  "Resource":"*","Condition":{"StringEquals":{"lambda:FunctionArn":"`+permitted+`"}}}]}`)
	restricted := lambda.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *lambda.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String("cond-url-permitted"), AuthType: lambdatypes.FunctionUrlAuthTypeNone})
	assert.NoError(t, err, "the grant names this function")

	_, err = restricted.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String("cond-url-refused"), AuthType: lambdatypes.FunctionUrlAuthTypeNone})
	require.Error(t, err, "another function is not covered by the grant: %s", refused)
	assert.Contains(t, err.Error(), "not authorized")
}

// lambdaConditionFunction creates a function for the condition-key tests and
// returns its ARN.
func lambdaConditionFunction(t *testing.T, client *lambda.Client, name string) string {
	t.Helper()
	created, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String("public.ecr.aws/docker/library/busybox:latest")},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})
	return aws.ToString(created.FunctionArn)
}

// TestCloudMap_ServiceCreatedByAccountConditionKeyScopesTheGrant covers
// servicediscovery:ServiceCreatedByAccount, the account that created the AWS
// Cloud Map service a request names.
func TestCloudMap_ServiceCreatedByAccountConditionKeyScopesTheGrant(t *testing.T) {
	admin := cmClient()
	namespace, err := admin.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name: aws.String("cond-created-ns")})
	require.NoError(t, err)
	operation, err := admin.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: namespace.OperationId})
	require.NoError(t, err)
	namespaceID, ok := operation.Operation.Targets["NAMESPACE"]
	require.True(t, ok, "the create operation names the namespace it made")

	created, err := admin.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("cond-created-svc"), NamespaceId: aws.String(namespaceID)})
	require.NoError(t, err)
	serviceID := aws.ToString(created.Service.Id)
	t.Cleanup(func() {
		_, _ = admin.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(serviceID)})
	})

	client := func(user, account string) *servicediscovery.Client {
		akid, secret := restrictedCredential(t, user,
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"servicediscovery:GetService",
			  "Resource":"*",
			  "Condition":{"StringEquals":{"servicediscovery:ServiceCreatedByAccount":"`+account+`"}}}]}`)
		return servicediscovery.NewFromConfig(aws.Config{Region: "us-east-1",
			Credentials: credentials.NewStaticCredentialsProvider(akid, secret, ""),
			HTTPClient:  simHTTPClient},
			func(o *servicediscovery.Options) { o.BaseEndpoint = aws.String(simEndpoint("servicediscovery")) })
	}

	_, err = client("cm-own-account", s3ObjectLambdaAccount).GetService(ctx,
		&servicediscovery.GetServiceInput{Id: aws.String(serviceID)})
	assert.NoError(t, err, "the service was created by the account the grant names")

	_, err = client("cm-other-account", "999999999999").GetService(ctx,
		&servicediscovery.GetServiceInput{Id: aws.String(serviceID)})
	require.Error(t, err, "a service created by another account is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestDynamoDB_SelectConditionKeyScopesTheGrant covers dynamodb:Select, how
// much of a matched item a read returns — the key a policy pins so a caller can
// count rows without reading them.
func TestDynamoDB_SelectConditionKeyScopesTheGrant(t *testing.T) {
	admin := ddbClient()
	const table = "cond-select-table"
	_, err := admin.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })

	akid, secret := restrictedCredential(t, "ddb-counts-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:Scan","Resource":"*",
		  "Condition":{"StringEquals":{"dynamodb:Select":"COUNT"}}}]}`)
	restricted := dynamodb.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(table), Select: ddbtypes.SelectCount})
	assert.NoError(t, err, "counting is what the grant allows")

	_, err = restricted.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(table), Select: ddbtypes.SelectAllAttributes})
	require.Error(t, err, "reading the attributes is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestACM_CertificateKeyPairOriginConditionKeyScopesTheGrant covers
// acm:CertificateKeyPairOrigin, which says who made a certificate's key pair —
// the caller, for one it imported, or AWS, for one this service issued. A
// policy uses it to keep operators away from certificates whose private key
// came from outside.
func TestACM_CertificateKeyPairOriginConditionKeyScopesTheGrant(t *testing.T) {
	admin := acmClient()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(4242),
		Subject:      pkix.Name{CommonName: "origin.example.com"},
		DNSNames:     []string{"origin.example.com"},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	imported, err := admin.ImportCertificate(ctx, &acm.ImportCertificateInput{
		Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		PrivateKey: pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}),
	})
	require.NoError(t, err)

	issued, err := admin.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String("issued.example.com")})
	require.NoError(t, err)

	akid, secret := restrictedCredential(t, "acm-imported-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"acm:DescribeCertificate",
		  "Resource":"*",
		  "Condition":{"StringEquals":{"acm:CertificateKeyPairOrigin":"IMPORTED"}}}]}`)
	restricted := acm.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *acm.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: imported.CertificateArn})
	assert.NoError(t, err, "the caller imported this certificate's key pair, which the grant names")

	_, err = restricted.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: issued.CertificateArn})
	require.Error(t, err, "a certificate this service issued is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}
