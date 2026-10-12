package aws_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_ServiceOwnedBucketsStayOutOfTheCallersAccount follows the presigned
// URL AWS Lambda hands out for a function's deployment package. The bucket it
// names belongs to AWS, not the caller: ListBuckets does not list it, every
// request the caller signs against it is refused, and its name is taken. The
// URL itself still downloads the package.
func TestS3_ServiceOwnedBucketsStayOutOfTheCallersAccount(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	fnName := uniqueName("svc-owned-bucket-fn")
	deployment := lambdaDeploymentZip(t)
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: deployment},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })

	got, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	location := aws.ToString(got.Code.Location)
	parsed, err := url.Parse(location)
	require.NoError(t, err)
	bucket, key, ok := strings.Cut(strings.TrimPrefix(parsed.Path, "/"), "/")
	require.True(t, ok, "the location names a bucket and a key: %s", location)

	download, err := http.Get(location)
	require.NoError(t, err)
	defer download.Body.Close()
	downloaded, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, download.StatusCode, string(downloaded))
	require.Equal(t, deployment, downloaded)

	sc := s3Client()
	listed, err := sc.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	for _, b := range listed.Buckets {
		assert.NotEqual(t, bucket, aws.ToString(b.Name), "ListBuckets lists a bucket AWS Lambda owns")
	}

	_, err = sc.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	var response *awshttp.ResponseError
	require.True(t, errors.As(err, &response), "HeadBucket: %v", err)
	assert.Equal(t, http.StatusForbidden, response.HTTPStatusCode())

	_, err = sc.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	requireS3ErrorCode(t, err, "AccessDenied")
	_, err = sc.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	requireS3ErrorCode(t, err, "AccessDenied")
	_, err = sc.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	requireS3ErrorCode(t, err, "AccessDenied")

	_, err = sc.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var taken *s3types.BucketAlreadyExists
	require.True(t, errors.As(err, &taken), "CreateBucket on a name AWS owns: %v", err)

	again, err := http.Get(location)
	require.NoError(t, err)
	defer again.Body.Close()
	require.Equal(t, http.StatusOK, again.StatusCode, "the caller's refused requests must not have removed the package")
}
