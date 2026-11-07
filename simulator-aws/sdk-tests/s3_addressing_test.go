package aws_sdk_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostRecorder keeps the Host of every request the SDK sends, so a test sees
// the addressing style the SDK chose rather than inferring it.
type hostRecorder struct {
	mu    sync.Mutex
	hosts []string
	next  aws.HTTPClient
}

func (h *hostRecorder) Do(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.hosts = append(h.hosts, req.URL.Host)
	h.mu.Unlock()
	return h.next.Do(req)
}

func (h *hostRecorder) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hosts[len(h.hosts)-1]
}

// A general-purpose bucket is addressed virtual-hosted style, as the SDK
// addresses it against AWS: under the s3.<host> service endpoint and under a
// bare endpoint URL, where the SDK puts the bucket in front of the URL's host.
func TestS3_VirtualHostedAddressing(t *testing.T) {
	t.Parallel()
	bucket := uniquename.New("vhost")
	recorder := &hostRecorder{next: sdkHTTPClient}
	byService := s3.NewFromConfig(sdkConfig(), simS3, func(o *s3.Options) { o.HTTPClient = recorder })

	_, err := byService.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%s.s3.localhost:%d", bucket, simPort), recorder.last())
	for _, key := range []string{"photos/", "photos/a.txt"} {
		_, err = byService.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(key)})
		require.NoError(t, err)
	}
	listed, err := byService.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	var keys []string
	for _, o := range listed.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	assert.ElementsMatch(t, []string{"photos/", "photos/a.txt"}, keys, "a key ending in a slash keeps it")

	byURL := s3.NewFromConfig(sdkConfig(), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("http://localhost:%d", simPort))
		o.HTTPClient = recorder
	})
	got, err := byURL.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("photos/a.txt")})
	require.NoError(t, err)
	defer got.Body.Close()
	assert.Equal(t, fmt.Sprintf("%s.localhost:%d", bucket, simPort), recorder.last())
	head, err := byURL.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("photos/")})
	require.NoError(t, err)
	assert.Equal(t, int64(len("photos/")), aws.ToInt64(head.ContentLength))
}

// CreateBucket answers by the request's Region and the caller's account: the
// owner re-creating its bucket gets 200 in us-east-1 and
// BucketAlreadyOwnedByYou elsewhere, another account gets BucketAlreadyExists,
// and a name the general purpose naming rules refuse is InvalidBucketName.
func TestS3_CreateBucketByRegionOwnerAndName(t *testing.T) {
	t.Parallel()
	bucket := uniquename.New("create-rules")
	east := s3Client()
	_, err := east.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = east.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err, "the owner re-creating its bucket in us-east-1 gets 200")

	westConfig := sdkConfig()
	westConfig.Region = "us-west-2"
	_, err = s3.NewFromConfig(westConfig, simS3).CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	assert.Equal(t, "BucketAlreadyOwnedByYou", errCodeOf(err))

	member := createMemberAccount(t, "s3-create", "")
	other := s3.NewFromConfig(sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole")), simS3)
	_, err = other.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	assert.Equal(t, "BucketAlreadyExists", errCodeOf(err))
	owned := uniquename.New("member-owned")
	_, err = other.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(owned)})
	require.NoError(t, err)
	mine, err := other.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	var names []string
	for _, b := range mine.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	assert.Contains(t, names, owned)
	assert.NotContains(t, names, bucket, "ListBuckets lists the caller's own buckets")
	assert.Equal(t, member, aws.ToString(mine.Owner.ID))

	for _, name := range []string{"ab", "Upper-case", "under_score", "two..dots", "192.168.5.4", "-leading", "xn--prefix", "sthree-prefix", "amzn-s3-demo-x", "name-s3alias", "name--ol-s3", "name.mrap", "name--table-s3"} {
		_, err = east.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
		assert.Equal(t, "InvalidBucketName", errCodeOf(err), name)
	}
}

// simGet fetches a URL a client was handed, a presigned one among them,
// through the suite's transport, which resolves the simulator's names.
func simGet(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	return simHTTPClient.Do(req)
}
