package aws_sdk_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signedAt hands the SDK's SigV4 presigner a fixed signing time, so a URL can
// be signed in the past instead of waiting for one to age.
type signedAt struct {
	signer *v4.Signer
	at     time.Time
}

func (s signedAt) PresignHTTP(ctx context.Context, credentials aws.Credentials, r *http.Request,
	payloadHash, service, region string, _ time.Time, optFns ...func(*v4.SignerOptions),
) (string, http.Header, error) {
	return s.signer.PresignHTTP(ctx, credentials, r, payloadHash, service, region, s.at, optFns...)
}

// TestS3_PresignedURLExpires follows a presigned GetObject URL across the
// lifetime it was signed for: Amazon S3 serves it within X-Amz-Expires of
// X-Amz-Date and refuses it with 403 AccessDenied, "Request has expired",
// after.
func TestS3_PresignedURLExpires(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("s3-presign-expiry")
	sc := s3Client()
	_, err := sc.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = sc.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("payload")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
		_, _ = sc.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	get := func(signingTime time.Time) (int, string) {
		t.Helper()
		presigned, err := s3.NewPresignClient(sc, func(o *s3.PresignOptions) {
			o.Expires = 15 * time.Minute
			o.Presigner = signedAt{signer: v4.NewSigner(), at: signingTime}
		}).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
		require.NoError(t, err)
		resp, err := http.Get(presigned.URL) //nolint:noctx // the presigned URL carries its own auth
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	status, body := get(time.Now().UTC().Add(-10 * time.Minute))
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "payload", body)

	status, body = get(time.Now().UTC().Add(-20 * time.Minute))
	require.Equal(t, http.StatusForbidden, status, body)
	assert.Contains(t, body, "<Code>AccessDenied</Code>")
	assert.Contains(t, body, "<Message>Request has expired</Message>")
}
