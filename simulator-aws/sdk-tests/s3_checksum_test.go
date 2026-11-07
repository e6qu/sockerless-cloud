package aws_sdk_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// S3 validates the additional checksum an upload states and keeps it with the
// object, returning it to a reader that asks with ChecksumMode; a checksum that
// does not match the payload is refused with BadDigest. An upload that names no
// algorithm gets the CRC64NVME checksum S3 computes itself.
func TestS3_AdditionalChecksums(t *testing.T) {
	t.Parallel()
	c := s3Client()
	bucket := uniquename.New("checksums")
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	body := []byte("checksummed payload")
	sum := crc32.ChecksumIEEE(body)
	want := base64.StdEncoding.EncodeToString([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)})
	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("crc32"),
		Body: bytes.NewReader(body), ChecksumAlgorithm: s3types.ChecksumAlgorithmCrc32})
	require.NoError(t, err)
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("crc32"), ChecksumMode: s3types.ChecksumModeEnabled})
	require.NoError(t, err)
	assert.Equal(t, want, aws.ToString(head.ChecksumCRC32))
	assert.Equal(t, s3types.ChecksumTypeFullObject, head.ChecksumType)
	got, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("crc32"), ChecksumMode: s3types.ChecksumModeEnabled})
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, got.Body)
	_ = got.Body.Close()
	assert.Equal(t, want, aws.ToString(got.ChecksumCRC32))

	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("wrong"),
		Body: bytes.NewReader(body), ChecksumCRC32: aws.String("AAAAAA==")})
	assert.Equal(t, "BadDigest", errCodeOf(err))
	_, err = c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("wrong")})
	require.Error(t, err, "a refused upload stores nothing")

	upload, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("parts"),
		ChecksumAlgorithm: s3types.ChecksumAlgorithmSha256})
	require.NoError(t, err)
	var completed []s3types.CompletedPart
	for i, part := range [][]byte{bytes.Repeat([]byte("a"), 5<<20), []byte("tail")} {
		out, err := c.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("parts"), UploadId: upload.UploadId,
			PartNumber: aws.Int32(int32(i + 1)), Body: bytes.NewReader(part), ChecksumAlgorithm: s3types.ChecksumAlgorithmSha256})
		require.NoError(t, err)
		digest := sha256.Sum256(part)
		assert.Equal(t, base64.StdEncoding.EncodeToString(digest[:]), aws.ToString(out.ChecksumSHA256))
		completed = append(completed, s3types.CompletedPart{PartNumber: aws.Int32(int32(i + 1)), ETag: out.ETag, ChecksumSHA256: out.ChecksumSHA256})
	}
	_, err = c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("parts"), UploadId: upload.UploadId,
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: completed}})
	require.NoError(t, err)
	head, err = c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("parts"), ChecksumMode: s3types.ChecksumModeEnabled})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(aws.ToString(head.ChecksumSHA256), "-2"), "a composite checksum: %q", aws.ToString(head.ChecksumSHA256))
	assert.Equal(t, s3types.ChecksumTypeComposite, head.ChecksumType)
}

// A signed streaming upload's chunks chain their signatures from the request's
// own; S3 stores the payload when every chunk verifies and refuses a chunk whose
// bytes were changed after signing.
func TestS3_SignedStreamingUploadVerifiesEachChunk(t *testing.T) {
	t.Parallel()
	c := s3Client()
	bucket := uniquename.New("streaming")
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	put := func(key string, chunks [][]byte, tamper bool) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s/%s", baseURL, bucket, key), nil)
		require.NoError(t, err)
		signStreamingUpload(t, req, chunks, tamper)
		resp, err := sdkHTTPClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	ok := put("intact", [][]byte{[]byte("first chunk "), []byte("second chunk")}, false)
	_ = ok.Body.Close()
	require.Equal(t, http.StatusOK, ok.StatusCode)
	got, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("intact")})
	require.NoError(t, err)
	data, _ := io.ReadAll(got.Body)
	_ = got.Body.Close()
	assert.Equal(t, "first chunk second chunk", string(data))

	refused := put("tampered", [][]byte{[]byte("first chunk "), []byte("second chunk")}, true)
	defer func() { _ = refused.Body.Close() }()
	body, _ := io.ReadAll(refused.Body)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode)
	assert.Contains(t, string(body), "SignatureDoesNotMatch")
	_, err = c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("tampered")})
	require.Error(t, err, "a refused upload stores nothing")
}

// signStreamingUpload signs req as a STREAMING-AWS4-HMAC-SHA256-PAYLOAD upload
// and sets its aws-chunked body: each chunk's signature chains from the
// previous one, starting at the request's own, over the chunk's SHA-256
// (aws-c-auth-sigv4-signing.c). With tamper the chunks' bytes change after
// signing.
func signStreamingUpload(t *testing.T, req *http.Request, chunks [][]byte, tamper bool) {
	t.Helper()
	decoded := 0
	for _, chunk := range chunks {
		decoded += len(chunk)
	}
	const payload = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	req.Header.Set("x-amz-content-sha256", payload)
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("x-amz-decoded-content-length", strconv.Itoa(decoded))
	now := time.Now().UTC()
	creds := aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}
	require.NoError(t, v4.NewSigner().SignHTTP(context.Background(), creds, req, payload, "s3", "us-east-1", now))
	seed := req.Header.Get("Authorization")
	seed = seed[strings.LastIndex(seed, "Signature=")+len("Signature="):]

	date := now.Format("20060102")
	key := hmacSum(hmacSum(hmacSum(hmacSum([]byte("AWS4test"), date), "us-east-1"), "s3"), "aws4_request")
	scope := date + "/us-east-1/s3/aws4_request"
	previous := seed
	var framed bytes.Buffer
	emptyHash := sha256.Sum256(nil)
	for _, chunk := range append(chunks, nil) {
		chunkHash := sha256.Sum256(chunk)
		stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256-PAYLOAD", now.Format("20060102T150405Z"), scope, previous,
			hex.EncodeToString(emptyHash[:]), hex.EncodeToString(chunkHash[:])}, "\n")
		previous = hex.EncodeToString(hmacSum(key, stringToSign))
		sent := chunk
		if tamper && len(chunk) > 0 {
			sent = bytes.ToUpper(chunk)
		}
		fmt.Fprintf(&framed, "%x;chunk-signature=%s\r\n%s\r\n", len(sent), previous, sent)
	}
	req.Body = io.NopCloser(bytes.NewReader(framed.Bytes()))
	req.ContentLength = int64(framed.Len())
}

func hmacSum(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
