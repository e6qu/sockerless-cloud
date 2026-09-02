package aws_sdk_test

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3ctypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3ControlClient talks to the S3 control plane, where access points live.
// CreateAccessPoint and its siblings carry a modeled host prefix — the account
// id — so the endpoint has to be a name the prefix can attach to rather than
// an IP literal.
func s3ControlClient() *s3control.Client {
	return s3control.NewFromConfig(sdkConfig(), func(o *s3control.Options) {
		o.BaseEndpoint = aws.String(simEndpoint("s3-control"))
	})
}

// s3ObjectLambdaS3Client reads through Object Lambda access points and posts
// the transformation callback. Both are host-addressed — the access point by
// its own alias, WriteGetObjectResponse by the request route — so this client
// takes a named endpoint and leaves path-style addressing off, which the SDK
// refuses to combine with an ARN bucket anyway.
func s3ObjectLambdaS3Client() *s3.Client {
	return s3.NewFromConfig(sdkConfig(), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(simEndpoint("s3"))
	})
}

const s3ObjectLambdaAccount = "123456789012"

// TestS3_AccessPointLifecycle covers the standard access point an Object
// Lambda access point is built on: create, read, list, its policy, and delete.
func TestS3_AccessPointLifecycle(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket, apName := "ap-lifecycle-bucket", "ap-lifecycle"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	created, err := sc.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		Name:      aws.String(apName),
		Bucket:    aws.String(bucket),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(created.AccessPointArn), ":accesspoint/"+apName)
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	})

	got, err := sc.GetAccessPoint(ctx, &s3control.GetAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	assert.Equal(t, bucket, aws.ToString(got.Bucket))
	assert.Equal(t, s3ctypes.NetworkOriginInternet, got.NetworkOrigin,
		"an access point with no VPC configuration is reachable from the internet")

	listed, err := sc.ListAccessPoints(ctx, &s3control.ListAccessPointsInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, listed.AccessPointList, 1)
	assert.Equal(t, apName, aws.ToString(listed.AccessPointList[0].Name))

	// A policy naming a principal is not public; PolicyStatus says so.
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"s3:GetObject",`+
		`"Resource":"%s/object/*"}]}`, s3ObjectLambdaAccount, aws.ToString(created.AccessPointArn))
	_, err = sc.PutAccessPointPolicy(ctx, &s3control.PutAccessPointPolicyInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName),
		Policy: aws.String(policy)})
	require.NoError(t, err)

	readBack, err := sc.GetAccessPointPolicy(ctx, &s3control.GetAccessPointPolicyInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	assert.JSONEq(t, policy, aws.ToString(readBack.Policy))

	status, err := sc.GetAccessPointPolicyStatus(ctx, &s3control.GetAccessPointPolicyStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	assert.False(t, status.PolicyStatus.IsPublic)

	_, err = sc.DeleteAccessPointPolicy(ctx, &s3control.DeleteAccessPointPolicyInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	_, err = sc.GetAccessPointPolicy(ctx, &s3control.GetAccessPointPolicyInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.Error(t, err, "the policy is gone once deleted")

	_, err = sc.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	_, err = sc.GetAccessPoint(ctx, &s3control.GetAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.Error(t, err)
}

// TestS3_CreateAccessPointRejectsMissingBucket holds the create to a bucket
// that exists: an access point over nothing would serve reads of nothing.
func TestS3_CreateAccessPointRejectsMissingBucket(t *testing.T) {
	_, err := s3ControlClient().CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		Name:      aws.String("ap-no-bucket"),
		Bucket:    aws.String("bucket-that-was-never-created"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchBucket")
}

// TestS3_ObjectLambdaGetObject is the whole Object Lambda loop: a read
// addressed to the Object Lambda access point runs the transformation
// function, which fetches the stored object and posts a transformed body back
// through WriteGetObjectResponse. The caller receives what the function wrote
// — never the stored bytes.
func TestS3_ObjectLambdaGetObject(t *testing.T) {
	sc, s3c, lc := s3ControlClient(), s3Client(), lambdaClient()
	bucket, apName, olapName := "olap-bucket", "olap-supporting-ap", "olap-uppercase"
	key, stored := "greeting.txt", "hello from the stored object"
	fnName := "olap-uppercase-fn"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: strings.NewReader(stored), ContentType: aws.String("text/plain"),
	})
	require.NoError(t, err)

	_, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::" + s3ObjectLambdaAccount + ":role/olap-transform"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Timeout:      aws.Int32(30),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})

	createdAP, err := sc.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		Name:      aws.String(apName), Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	})

	fnARN := fmt.Sprintf("arn:aws:lambda:us-east-1:%s:function:%s", s3ObjectLambdaAccount, fnName)
	configuration := &s3ctypes.ObjectLambdaConfiguration{
		SupportingAccessPoint: createdAP.AccessPointArn,
		TransformationConfigurations: []s3ctypes.ObjectLambdaTransformationConfiguration{{
			Actions: []s3ctypes.ObjectLambdaTransformationConfigurationAction{
				s3ctypes.ObjectLambdaTransformationConfigurationActionGetObject,
			},
			ContentTransformation: &s3ctypes.ObjectLambdaContentTransformationMemberAwsLambda{
				Value: s3ctypes.AwsLambdaTransformation{FunctionArn: aws.String(fnARN)},
			},
		}},
	}
	createdOLAP, err := sc.CreateAccessPointForObjectLambda(ctx, &s3control.CreateAccessPointForObjectLambdaInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		Name:      aws.String(olapName), Configuration: configuration,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(createdOLAP.ObjectLambdaAccessPointArn),
		"arn:aws:s3-object-lambda:")
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessPointForObjectLambda(ctx, &s3control.DeleteAccessPointForObjectLambdaInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(olapName)})
	})

	readConfig, err := sc.GetAccessPointConfigurationForObjectLambda(ctx,
		&s3control.GetAccessPointConfigurationForObjectLambdaInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(olapName)})
	require.NoError(t, err)
	require.NotNil(t, readConfig.Configuration)
	assert.Equal(t, aws.ToString(createdAP.AccessPointArn),
		aws.ToString(readConfig.Configuration.SupportingAccessPoint))
	require.Len(t, readConfig.Configuration.TransformationConfigurations, 1)

	listed, err := sc.ListAccessPointsForObjectLambda(ctx,
		&s3control.ListAccessPointsForObjectLambdaInput{AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	require.Len(t, listed.ObjectLambdaAccessPointList, 1)
	assert.Equal(t, olapName, aws.ToString(listed.ObjectLambdaAccessPointList[0].Name))

	// The read goes to the Object Lambda access point by its ARN, which is how
	// a client addresses one. The SDK resolves it to the access point's own
	// endpoint, so the request never names the bucket.
	got, err := s3ObjectLambdaS3Client().GetObject(ctx, &s3.GetObjectInput{
		Bucket: createdOLAP.ObjectLambdaAccessPointArn,
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	defer got.Body.Close()
	transformed, err := io.ReadAll(got.Body)
	require.NoError(t, err)
	assert.Equal(t, "HELLO FROM THE STORED OBJECT", string(transformed),
		"the caller receives what the transformation function wrote")
	assert.Equal(t, "text/plain", aws.ToString(got.ContentType),
		"x-amz-fwd-header-Content-Type reaches the caller as Content-Type")

	// The stored object is untouched: the transformation happens on the way
	// out, not in the bucket.
	direct, err := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key)})
	require.NoError(t, err)
	defer direct.Body.Close()
	original, err := io.ReadAll(direct.Body)
	require.NoError(t, err)
	assert.Equal(t, stored, string(original))
}

// TestS3_CreateObjectLambdaRejectsDanglingConfiguration holds the create to a
// supporting access point and a transformation function that both exist.
func TestS3_CreateObjectLambdaRejectsDanglingConfiguration(t *testing.T) {
	sc := s3ControlClient()
	_, err := sc.CreateAccessPointForObjectLambda(ctx, &s3control.CreateAccessPointForObjectLambdaInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		Name:      aws.String("olap-dangling"),
		Configuration: &s3ctypes.ObjectLambdaConfiguration{
			SupportingAccessPoint: aws.String(
				"arn:aws:s3:us-east-1:" + s3ObjectLambdaAccount + ":accesspoint/never-created"),
			TransformationConfigurations: []s3ctypes.ObjectLambdaTransformationConfiguration{{
				Actions: []s3ctypes.ObjectLambdaTransformationConfigurationAction{
					s3ctypes.ObjectLambdaTransformationConfigurationActionGetObject,
				},
				ContentTransformation: &s3ctypes.ObjectLambdaContentTransformationMemberAwsLambda{
					Value: s3ctypes.AwsLambdaTransformation{
						FunctionArn: aws.String("arn:aws:lambda:us-east-1:" +
							s3ObjectLambdaAccount + ":function:never-created"),
					},
				},
			}},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

// TestS3_WriteGetObjectResponseRejectsUnknownRoute holds the callback to a
// read that is actually in flight. The route token is what proves the caller
// is the function that read was routed to.
func TestS3_WriteGetObjectResponseRejectsUnknownRoute(t *testing.T) {
	_, err := s3ObjectLambdaS3Client().WriteGetObjectResponse(ctx, &s3.WriteGetObjectResponseInput{
		RequestRoute: aws.String("a-route-nobody-is-waiting-on"),
		RequestToken: aws.String("a-token-nobody-issued"),
		Body:         strings.NewReader("bytes with nowhere to go"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchRequestRoute")
}
