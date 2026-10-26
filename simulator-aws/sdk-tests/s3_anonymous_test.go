package aws_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3AnonymousClient is the SDK's anonymous mode: aws.AnonymousCredentials
// sends every request unsigned.
func s3AnonymousClient() *s3.Client {
	cfg := sdkConfig()
	cfg.Credentials = aws.AnonymousCredentials{}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.UsePathStyle = true
	})
}

func requireS3AccessDenied(t *testing.T, err error, what string) {
	t.Helper()
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "%s is refused: %v", what, err)
	assert.Equal(t, "AccessDenied", apiErr.ErrorCode(), what)
	var responseErr interface{ HTTPStatusCode() int }
	require.True(t, errors.As(err, &responseErr), what)
	assert.Equal(t, http.StatusForbidden, responseErr.HTTPStatusCode(), what)
}

// TestS3_AnonymousRequestsNeedABucketGrant holds an anonymous caller to what a
// bucket grants everyone. With no grant, S3 refuses its CreateBucket,
// ListBuckets, PutObject, GetObject and ListObjectsV2; a bucket policy
// statement for Principal "*" opens exactly the action it names; Block Public
// Access's RestrictPublicBuckets withdraws that grant again.
func TestS3_AnonymousRequestsNeedABucketGrant(t *testing.T) {
	t.Parallel()
	owner, anonymous := s3Client(), s3AnonymousClient()
	bucket := uniquename.New("anonymous-policy")

	_, err := anonymous.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	requireS3AccessDenied(t, err, "an anonymous CreateBucket")

	_, err = owner.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = owner.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
		_, _ = owner.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	_, err = anonymous.ListBuckets(ctx, &s3.ListBucketsInput{})
	requireS3AccessDenied(t, err, "an anonymous ListBuckets")

	_, err = anonymous.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html"), Body: strings.NewReader("anonymous")})
	requireS3AccessDenied(t, err, "an anonymous PutObject")

	_, err = owner.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html"), Body: strings.NewReader("<h1>public</h1>")})
	require.NoError(t, err)

	_, err = anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
	requireS3AccessDenied(t, err, "an anonymous GetObject without a grant")
	_, err = anonymous.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	requireS3AccessDenied(t, err, "an anonymous ListObjectsV2 without a grant")

	_, err = owner.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(`{
		"Version": "2012-10-17",
		"Statement": [{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::` + bucket + `/*"}]
	}`)})
	require.NoError(t, err)

	read, err := anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
	require.NoError(t, err, "the public-read policy admits an anonymous GetObject")
	body, err := io.ReadAll(read.Body)
	_ = read.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, "<h1>public</h1>", string(body))

	_, err = anonymous.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html"), Body: strings.NewReader("anonymous")})
	requireS3AccessDenied(t, err, "an anonymous PutObject the policy does not grant")
	_, err = anonymous.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	requireS3AccessDenied(t, err, "an anonymous ListObjectsV2 the policy does not grant")

	_, err = owner.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{RestrictPublicBuckets: aws.Bool(true)}})
	require.NoError(t, err)
	_, err = anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
	requireS3AccessDenied(t, err, "an anonymous GetObject once RestrictPublicBuckets is on")

	_, err = owner.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("index.html")})
	require.NoError(t, err, "the signed owner still reads the object")
}

// TestS3_AnonymousReadThroughAPublicACL grants AllUsers READ on one object with
// a canned ACL, which admits an anonymous GetObject of that object alone, until
// IgnorePublicAcls or Object Ownership's BucketOwnerEnforced disables the
// grant.
func TestS3_AnonymousReadThroughAPublicACL(t *testing.T) {
	t.Parallel()
	owner, anonymous := s3Client(), s3AnonymousClient()
	bucket := uniquename.New("anonymous-acl")
	_, err := owner.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, key := range []string{"public.txt", "private.txt"} {
			_, _ = owner.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		}
		_, _ = owner.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})
	for _, key := range []string{"public.txt", "private.txt"} {
		_, err = owner.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(key)})
		require.NoError(t, err)
	}
	_, err = owner.PutObjectAcl(ctx, &s3.PutObjectAclInput{Bucket: aws.String(bucket), Key: aws.String("public.txt"), ACL: s3types.ObjectCannedACLPublicRead})
	require.NoError(t, err)

	acl, err := owner.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String(bucket), Key: aws.String("public.txt")})
	require.NoError(t, err)
	var allUsersRead bool
	for _, grant := range acl.Grants {
		if grant.Grantee != nil && aws.ToString(grant.Grantee.URI) == "http://acs.amazonaws.com/groups/global/AllUsers" && grant.Permission == s3types.PermissionRead {
			allUsersRead = true
		}
	}
	assert.True(t, allUsersRead, "the public-read canned ACL grants AllUsers READ")

	_, err = anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("public.txt")})
	require.NoError(t, err, "the object's public-read ACL admits an anonymous GetObject")
	_, err = anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("private.txt")})
	requireS3AccessDenied(t, err, "an anonymous GetObject of an object without a public ACL")

	_, err = owner.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{IgnorePublicAcls: aws.Bool(true)}})
	require.NoError(t, err)
	_, err = anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("public.txt")})
	requireS3AccessDenied(t, err, "an anonymous GetObject once IgnorePublicAcls is on")

	_, err = owner.DeletePublicAccessBlock(ctx, &s3.DeletePublicAccessBlockInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = owner.PutBucketOwnershipControls(ctx, &s3.PutBucketOwnershipControlsInput{Bucket: aws.String(bucket),
		OwnershipControls: &s3types.OwnershipControls{Rules: []s3types.OwnershipControlsRule{{ObjectOwnership: s3types.ObjectOwnershipBucketOwnerEnforced}}}})
	require.NoError(t, err)
	_, err = anonymous.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("public.txt")})
	requireS3AccessDenied(t, err, "an anonymous GetObject once BucketOwnerEnforced disables ACLs")
}

// TestS3_RawUnsignedAndForgedRequests sends the wire forms an SDK cannot: an
// unsigned GET of the service root, and a forged signature for the account's
// access key.
func TestS3_RawUnsignedAndForgedRequests(t *testing.T) {
	t.Parallel()
	resp, err := http.Get(baseURL + "/") //nolint:noctx // the request is unsigned on purpose
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "an anonymous ListBuckets is refused: %s", body)
	assert.Contains(t, string(body), "<Code>AccessDenied</Code>")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Header.Set("X-Amz-Date", "20260101T000000Z")
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=deadbeef")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a forged signature is refused: %s", body)
	assert.Contains(t, string(body), "<Code>SignatureDoesNotMatch</Code>")
}
