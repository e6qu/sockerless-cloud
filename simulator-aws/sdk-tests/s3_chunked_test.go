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

// TestS3_AWSChunkedPutObject_RoundTrip exercises the aws-chunked
// upload path end-to-end against the sim, regression-guarding
// the known-bad shape ("S3 sim stores aws-chunked envelope verbatim").
//
// The aws-sdk-go-v2 SDK refuses streaming-signed payloads over plain
// HTTP, so reproducing the SDK's exact wire shape requires TLS. The
// sim's default listener is HTTP and the test harness does not yet
// provision TLS, so this test instead drives the same wire shape
// directly via net/http — hand-crafting the chunked-encoded body
// the SDK would send. The decoder unit tests in awschunked_test.go
// cover the framing variants the SDK can put on the wire; this test
// confirms the decoder is wired into handleS3PutObject and that the
// stored object round-trips correctly.
func TestS3_AWSChunkedPutObject_RoundTrip(t *testing.T) {
	client := s3Client()
	bucket := "chunked-test-bucket"
	key := "chunked-key"
	payload := "hello world"

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort cleanup; if the bucket is still populated
		// the SDK will surface it on the destroy.
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{
			Bucket: aws.String(bucket),
		})
	})

	// Hand-craft the chunked-encoded body that the SDK emits when
	// the payload is non-seekable + streaming-signed:
	//   "<hex-size>;chunk-signature=<sig>\r\n<payload>\r\n"
	//   "0;chunk-signature=<sig>\r\n\r\n"
	sig := strings.Repeat("0", 64) // sim auth-passthrough; signature ignored
	body := fmt.Sprintf("%x;chunk-signature=%s\r\n%s\r\n0;chunk-signature=%s\r\n\r\n",
		len(payload), sig, payload, sig)

	url := baseURL + "/" + bucket + "/" + key
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("x-amz-content-sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
	req.Header.Set("x-amz-decoded-content-length", fmt.Sprintf("%d", len(payload)))
	// Sign the seed SigV4 signature the S3 authentication gate verifies. The
	// per-chunk signatures inside the body remain placeholders — the sim
	// authenticates only the request (seed) signature, exactly as the real
	// S3 front end does for a streaming upload. The declared payload hash is
	// the streaming sentinel, so the framed body is not hashed.
	signRawSigV4(t, req, "s3", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("chunked PUT returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// GET back via the SDK using canonical config; the stored object
	// must be the decoded payload bytes, not the chunk envelope.
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

	// ETag should be md5 of the decoded payload (not the framed bytes).
	hash := md5.Sum([]byte(payload))
	wantETag := fmt.Sprintf("\"%s\"", hex.EncodeToString(hash[:]))
	if out.ETag == nil || *out.ETag != wantETag {
		t.Errorf("ETag mismatch: got %v, want %s (md5 of decoded payload)",
			out.ETag, wantETag)
	}
}
