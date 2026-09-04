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

// S3 Express One Zone. A directory bucket is placed in one Availability Zone
// and named for it, and a client reaches it by establishing a session rather
// than by signing each request with its own credentials.
//
//	GET /{bucket}?session          CreateSession
//	GET /?x-id=ListDirectoryBuckets ListDirectoryBuckets
//
// ListDirectoryBuckets is driven from the CLI suite rather than here: with a
// BaseEndpoint configured, aws-sdk-go-v2 v1.110.0 resolves that operation onto
// the S3 Express auth scheme, whose identity resolver demands a bucket the
// operation does not have, and fails with "get identity: bucket name is
// missing" before emitting a request. The same call over botocore reaches the
// endpoint, so the operation is exercised there.

const s3ExpressZone = "use1-az4"
const s3ExpressBucket = "sim-express--use1-az4--x-s3"

// s3ExpressClient is an ordinary S3 client with no endpoint override, so the
// SDK resolves S3 Express One Zone's own hostnames exactly as it does against
// Amazon S3 — the regional control endpoint for the bucket calls and the
// bucket's zonal endpoint for the session and the object calls. simHTTPClient
// resolves those names to the simulator; nothing else differs.
func s3ExpressClient() *s3.Client {
	return s3.NewFromConfig(sdkConfig(), func(o *s3.Options) {
		o.EndpointOptions.DisableHTTPS = true
	})
}

// TestS3_DirectoryBucketIsPlacedInItsZoneAndListedApart covers the placement
// half: a directory bucket carries the zone it sits in, its name has to agree
// with the Location that placed it, and the two listings are separate surfaces.
func TestS3_DirectoryBucketIsPlacedInItsZoneAndListedApart(t *testing.T) {
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

	// A name that does not carry the zone it is placed in is refused: the name
	// is what makes the bucket addressable at its zonal endpoint. It is not a
	// directory bucket's name, so the client addresses the ordinary endpoint
	// with it, which is what a caller who got the name wrong does.
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

	// The two listings are separate surfaces: the directory bucket is in one
	// and not the other.
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

// TestS3_SessionOpensTheOneBucketItWasCreatedFor covers the authentication
// half. CreateSession returns temporary credentials, and they are real: a later
// request signed with them authenticates as the caller who asked for them, and
// the session's scope is enforced — one bucket, and the mode it was created in.
func TestS3_SessionOpensTheOneBucketItWasCreatedFor(t *testing.T) {
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

	// A session on a general purpose bucket is refused: only a directory
	// bucket has a zonal endpoint to establish one on.
	general := "sim-express-general-purpose"
	plain := s3Client()
	_, err = plain.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(general)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = plain.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(general)}) })
	_, err = plain.CreateSession(ctx, &s3.CreateSessionInput{Bucket: aws.String(general)})
	require.Error(t, err, "a general purpose bucket has no session")

	// ReadWrite, the default.
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
			HTTPClient: simHTTPClient},
			func(o *s3.Options) {
				o.EndpointOptions.DisableHTTPS = true
				// This caller drives the session itself, so the SDK must not
				// establish one of its own: DisableS3ExpressSessionAuth is the
				// option Amazon's SDK offers for exactly that, and without it
				// the client would sign with a session it created while
				// carrying the token of the one under test.
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

// withSessionTokenHeader applies a session's token to the request header, which
// is how Amazon S3 documents authorizing a Zonal endpoint call made with
// credentials the caller obtained from CreateSession itself: sign with the
// temporary credentials, and carry the session token in x-amz-s3session-token.
// The SDK does this for the sessions it manages; a caller driving a session by
// hand — which is what the two scope assertions below need, since the SDK only
// ever manages a ReadWrite session for the bucket being addressed — does it
// like this, against Amazon S3 exactly as here.
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

// TestS3_TheSDKEstablishesTheSessionItself covers the path a client actually
// takes. Addressing a directory bucket with an ordinary client is enough: the
// SDK recognises the bucket, calls CreateSession on its own, signs with what
// comes back and carries the session token, and the object round-trips.
func TestS3_TheSDKEstablishesTheSessionItself(t *testing.T) {
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
