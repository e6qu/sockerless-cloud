package aws_sdk_test

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

// TestS3_AWSChunkedPutObject_RoundTrip expects an aws-chunked upload to store
// the decoded payload rather than the chunk envelope. The SDK refuses
// streaming-signed payloads over plain HTTP, so the test sends the SDK's wire
// shape through net/http; awschunked_test.go covers the framing variants.
func TestS3_AWSChunkedPutObject_RoundTrip(t *testing.T) {
	t.Parallel()
	client := s3Client()
	bucket := "chunked-test-bucket"
	key := "chunked-key"
	payload := "hello world"

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{
			Bucket: aws.String(bucket),
		})
	})

	// The body the SDK sends for a non-seekable, streaming-signed payload:
	//   "<hex-size>;chunk-signature=<sig>\r\n<payload>\r\n"
	//   "0;chunk-signature=<sig>\r\n\r\n"
	sig := strings.Repeat("0", 64)
	body := fmt.Sprintf("%x;chunk-signature=%s\r\n%s\r\n0;chunk-signature=%s\r\n\r\n",
		len(payload), sig, payload, sig)

	url := baseURL + "/" + bucket + "/" + key
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("x-amz-content-sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
	req.Header.Set("x-amz-decoded-content-length", fmt.Sprintf("%d", len(payload)))
	// Sign the seed signature the S3 authentication gate verifies. The declared
	// payload hash is the streaming sentinel, so the framed body is not hashed;
	// the per-chunk signatures stay placeholders.
	signRawSigV4(t, req, "s3", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("chunked PUT returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// The stored object is the decoded payload, not the chunk envelope.
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	defer out.Body.Close()
	got, err := io.ReadAll(out.Body)
	require.NoError(t, err)

	if string(got) != payload {
		t.Errorf("aws-chunked round-trip failed:\n  PUT  payload   = %q (len %d)\n  GET  stored    = %q (len %d)\n"+
			"This is the known-bad shape — the sim stored the chunk envelope verbatim instead of the decoded payload.",
			payload, len(payload), string(got), len(got))
	}

	// The ETag is the MD5 of the decoded payload.
	hash := md5.Sum([]byte(payload))
	wantETag := fmt.Sprintf("\"%s\"", hex.EncodeToString(hash[:]))
	if out.ETag == nil || *out.ETag != wantETag {
		t.Errorf("ETag mismatch: got %v, want %s (md5 of decoded payload)",
			out.ETag, wantETag)
	}
}
