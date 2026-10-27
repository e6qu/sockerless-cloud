package aws_sdk_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A directory bucket sits in one Availability Zone and is named for it, and a
// client reaches it through a session rather than by signing each request with
// its own credentials.
//
// The CLI suite covers ListDirectoryBuckets: with a BaseEndpoint,
// aws-sdk-go-v2 v1.110.0 resolves it onto the S3 Express auth scheme, whose
// identity resolver demands a bucket the operation lacks and fails with "get
// identity: bucket name is missing" before sending a request. botocore reaches
// the endpoint.

const s3ExpressZone = "use1-az4"
const s3ExpressBucket = "sim-express--use1-az4--x-s3"

// s3ExpressClient is an S3 client with no endpoint override, so the SDK resolves
// S3 Express One Zone's hostnames as it does against AWS: the regional control
// endpoint for bucket calls and the bucket's zonal endpoint for sessions and
// objects. simHTTPClient resolves those names to the simulator.
func s3ExpressClient() *s3.Client {
	return s3.NewFromConfig(sdkConfig(), func(o *s3.Options) {
		o.EndpointOptions.DisableHTTPS = true
	})
}

// TestS3_DirectoryBucketIsPlacedInItsZoneAndListedApart expects a directory
// bucket to carry its zone, its name to agree with the Location that placed
// it, and the two bucket listings to be separate.
func TestS3_DirectoryBucketIsPlacedInItsZoneAndListedApart(t *testing.T) {
	t.Parallel()
	admin := s3ExpressClient()

	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s3ExpressBucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			Location: &s3types.LocationInfo{
				Type: s3types.LocationTypeAvailabilityZone,
				Name: aws.String(s3ExpressZone),
			},
			Bucket: &s3types.BucketInfo{
				Type:           s3types.BucketTypeDirectory,
				DataRedundancy: s3types.DataRedundancySingleAvailabilityZone,
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(s3ExpressBucket)}) })

	// A name without the zone is refused: the name makes the bucket addressable at
	// its zonal endpoint. Not being a directory bucket name, it goes to the
	// ordinary endpoint, as a caller who got the name wrong sends it.
	_, err = s3Client().CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("sim-express-unzoned"),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			Location: &s3types.LocationInfo{
				Type: s3types.LocationTypeAvailabilityZone,
				Name: aws.String(s3ExpressZone),
			},
			Bucket: &s3types.BucketInfo{Type: s3types.BucketTypeDirectory},
		},
	})
	require.Error(t, err, "a directory bucket name must carry its zone")
	assert.Equal(t, "InvalidBucketName", errCodeOf(err))

	// A name whose zone disagrees with the Location that placed it is refused.
	_, err = admin.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("sim-express--usw2-az1--x-s3"),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			Location: &s3types.LocationInfo{
				Type: s3types.LocationTypeAvailabilityZone,
				Name: aws.String(s3ExpressZone),
			},
			Bucket: &s3types.BucketInfo{Type: s3types.BucketTypeDirectory},
		},
	})
	require.Error(t, err, "the name's zone and the Location must agree")
	assert.Equal(t, "InvalidBucketName", errCodeOf(err))

	// The directory bucket is in one listing and not the other.
	listed, err := s3Client().ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	for _, b := range listed.Buckets {
		assert.NotEqual(t, s3ExpressBucket, aws.ToString(b.Name),
			"ListBuckets returns general purpose buckets; a directory bucket is listed by ListDirectoryBuckets")
	}

	directory, err := admin.ListDirectoryBuckets(ctx, &s3.ListDirectoryBucketsInput{})
	require.NoError(t, err)
	var names []string
	for _, b := range directory.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	assert.Contains(t, names, s3ExpressBucket, "ListDirectoryBuckets returns the directory buckets")
}

// TestS3_SessionOpensTheOneBucketItWasCreatedFor expects CreateSession's
// temporary credentials to authenticate later requests as the caller who asked
// for them, within the session's scope: one bucket, in the mode it was created
// in.
func TestS3_SessionOpensTheOneBucketItWasCreatedFor(t *testing.T) {
	t.Parallel()
	admin := s3ExpressClient()
	other := "sim-express-other--use1-az4--x-s3"

	for _, name := range []string{s3ExpressBucket, other} {
		_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(name),
			CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
				Location: &s3types.LocationInfo{
					Type: s3types.LocationTypeAvailabilityZone,
					Name: aws.String(s3ExpressZone),
				},
				Bucket: &s3types.BucketInfo{Type: s3types.BucketTypeDirectory},
			},
		})
		if err != nil {
			require.Equal(t, "BucketAlreadyOwnedByYou", errCodeOf(err))
		}
	}
	_, err := admin.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s3ExpressBucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	// Only a directory bucket has a zonal endpoint to open a session on.
	general := "sim-express-general-purpose"
	plain := s3Client()
	_, err = plain.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(general)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = plain.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(general)}) })
	_, err = plain.CreateSession(ctx, &s3.CreateSessionInput{Bucket: aws.String(general)})
	require.Error(t, err, "a general purpose bucket has no session")

	// ReadWrite is the default.
	session, err := admin.CreateSession(ctx, &s3.CreateSessionInput{Bucket: aws.String(s3ExpressBucket)})
	require.NoError(t, err)
	require.NotNil(t, session.Credentials)
	require.NotEmpty(t, aws.ToString(session.Credentials.AccessKeyId))
	require.NotEmpty(t, aws.ToString(session.Credentials.SessionToken))
	require.NotNil(t, session.Credentials.Expiration)

	sessionClient := func(creds *s3types.SessionCredentials) *s3.Client {
		return s3.NewFromConfig(aws.Config{Region: "us-east-1",
			Credentials: credentials.NewStaticCredentialsProvider(
				aws.ToString(creds.AccessKeyId), aws.ToString(creds.SecretAccessKey),
				aws.ToString(creds.SessionToken)),
			HTTPClient: sdkHTTPClient},
			func(o *s3.Options) {
				o.EndpointOptions.DisableHTTPS = true
				// The test drives the session itself, so DisableS3ExpressSessionAuth stops the
				// SDK from signing with a session of its own while carrying the token of the
				// one under test.
				o.DisableS3ExpressSessionAuth = aws.Bool(true)
				o.APIOptions = append(o.APIOptions, withSessionTokenHeader(aws.ToString(creds.SessionToken)))
			})
	}

	rw := sessionClient(session.Credentials)
	_, err = rw.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s3ExpressBucket), Key: aws.String("k")})
	assert.NoError(t, err, "the session opens the bucket it was created for")

	// The same session does not open a different directory bucket.
	_, err = rw.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(other), Key: aws.String("k")})
	require.Error(t, err, "a session opens only the bucket it was created for")
	assert.Equal(t, "AccessDenied", errCodeOf(err))

	// A ReadOnly session reads and does not write.
	readOnly, err := admin.CreateSession(ctx, &s3.CreateSessionInput{
		Bucket: aws.String(s3ExpressBucket), SessionMode: s3types.SessionModeReadOnly})
	require.NoError(t, err)
	ro := sessionClient(readOnly.Credentials)
	_, err = ro.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s3ExpressBucket), Key: aws.String("k")})
	assert.NoError(t, err, "a ReadOnly session reads")
	_, err = ro.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s3ExpressBucket), Key: aws.String("k2"), Body: strings.NewReader("v")})
	require.Error(t, err, "a ReadOnly session does not write")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// withSessionTokenHeader carries a session's token in x-amz-s3session-token,
// as Amazon S3 documents for a zonal call signed with credentials the caller
// got from CreateSession. The SDK only manages a ReadWrite session for the
// addressed bucket, so the scope assertions drive sessions by hand.
func withSessionTokenHeader(token string) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Finalize.Add(middleware.FinalizeMiddlewareFunc("withSessionTokenHeader",
			func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
				if req, ok := in.Request.(*smithyhttp.Request); ok {
					req.Header.Set("x-amz-s3session-token", token)
				}
				return next.HandleFinalize(ctx, in)
			}), middleware.After)
	}
}

// TestS3_TheSDKEstablishesTheSessionItself expects an ordinary client
// addressing a directory bucket to call CreateSession, sign with the result,
// carry the token, and round-trip the object.
func TestS3_TheSDKEstablishesTheSessionItself(t *testing.T) {
	t.Parallel()
	client := s3ExpressClient()
	bucket := "sim-express-sdk-managed--use1-az4--x-s3"

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			Location: &s3types.LocationInfo{
				Type: s3types.LocationTypeAvailabilityZone,
				Name: aws.String(s3ExpressZone),
			},
			Bucket: &s3types.BucketInfo{Type: s3types.BucketTypeDirectory},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}) })

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("through-a-session"),
		Body: strings.NewReader("payload")})
	require.NoError(t, err)

	read, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("through-a-session")})
	require.NoError(t, err)
	defer func() { _ = read.Body.Close() }()
	body := make([]byte, 7)
	_, _ = read.Body.Read(body)
	assert.Equal(t, "payload", string(body))
}
