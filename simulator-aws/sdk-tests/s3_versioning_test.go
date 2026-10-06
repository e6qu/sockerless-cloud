package aws_sdk_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3VersionedBucket creates a bucket and sets its versioning state.
func s3VersionedBucket(t *testing.T, c *s3.Client, prefix string, status types.BucketVersioningStatus) string {
	t.Helper()
	bucket := uniqueName(prefix)
	s3CreateBucket(t, c, bucket)
	s3SetVersioning(t, c, bucket, status)
	return bucket
}

func s3SetVersioning(t *testing.T, c *s3.Client, bucket string, status types.BucketVersioningStatus) {
	t.Helper()
	_, err := c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(bucket),
		VersioningConfiguration: &types.VersioningConfiguration{Status: status}})
	require.NoError(t, err)
}

func s3Put(t *testing.T, c *s3.Client, bucket, key, body string) *s3.PutObjectOutput {
	t.Helper()
	out, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body)})
	require.NoError(t, err)
	return out
}

// s3Read reads the object version names ("" for the current object).
func s3Read(t *testing.T, c *s3.Client, bucket, key, version string) (string, error) {
	t.Helper()
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if version != "" {
		in.VersionId = aws.String(version)
	}
	out, err := c.GetObject(ctx, in)
	if err != nil {
		return "", err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	return string(data), nil
}

type s3ListedVersion struct {
	key, version   string
	latest, marker bool
}

// s3ListVersions lists every version and delete marker in bucket, in the
// order ListObjectVersions reports them within each kind.
func s3ListVersions(t *testing.T, c *s3.Client, bucket string) (versions, markers []s3ListedVersion) {
	t.Helper()
	out, err := c.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	for _, v := range out.Versions {
		versions = append(versions, s3ListedVersion{aws.ToString(v.Key), aws.ToString(v.VersionId), aws.ToBool(v.IsLatest), false})
	}
	for _, m := range out.DeleteMarkers {
		markers = append(markers, s3ListedVersion{aws.ToString(m.Key), aws.ToString(m.VersionId), aws.ToBool(m.IsLatest), true})
	}
	return versions, markers
}

// TestS3_VersioningKeepsEveryVersion writes, reads and deletes an object in a
// versioning-enabled bucket: every write is a version of its own, a delete
// without a version leaves a delete marker, and deleting a version by id
// makes the next newest current.
func TestS3_VersioningKeepsEveryVersion(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "versioned", types.BucketVersioningStatusEnabled)

	first := aws.ToString(s3Put(t, c, bucket, "report", "first").VersionId)
	second := aws.ToString(s3Put(t, c, bucket, "report", "second").VersionId)
	require.NotEmpty(t, first)
	require.NotEmpty(t, second)
	assert.NotEqual(t, "null", first)
	assert.NotEqual(t, first, second)

	body, err := s3Read(t, c, bucket, "report", "")
	require.NoError(t, err)
	assert.Equal(t, "second", body)
	body, err = s3Read(t, c, bucket, "report", first)
	require.NoError(t, err)
	assert.Equal(t, "first", body, "a version id reads that version")
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("report"), VersionId: aws.String(first)})
	require.NoError(t, err)
	assert.Equal(t, first, aws.ToString(head.VersionId))
	assert.Equal(t, int64(len("first")), aws.ToInt64(head.ContentLength))
	_, err = s3Read(t, c, bucket, "report", "3sL4kqtJlcpXroDTDmJ.rmSpXd3dIbrH")
	assert.Equal(t, "NoSuchVersion", errCodeOf(err))

	versions, markers := s3ListVersions(t, c, bucket)
	assert.Equal(t, []s3ListedVersion{{"report", second, true, false}, {"report", first, false, false}}, versions)
	assert.Empty(t, markers)

	deleted, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("report")})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(deleted.DeleteMarker))
	marker := aws.ToString(deleted.VersionId)
	require.NotEmpty(t, marker)

	_, err = s3Read(t, c, bucket, "report", "")
	assert.Equal(t, "NoSuchKey", errCodeOf(err), "a delete marker hides the key")
	_, err = s3Read(t, c, bucket, "report", marker)
	assert.Equal(t, "MethodNotAllowed", errCodeOf(err), "a delete marker has no contents to read")
	listed, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Empty(t, listed.Contents)
	versions, markers = s3ListVersions(t, c, bucket)
	assert.Equal(t, []s3ListedVersion{{"report", second, false, false}, {"report", first, false, false}}, versions)
	assert.Equal(t, []s3ListedVersion{{"report", marker, true, true}}, markers)
	_, err = c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	assert.Equal(t, "BucketNotEmpty", errCodeOf(err), "noncurrent versions keep a bucket from being deleted")

	removed, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("report"), VersionId: aws.String(marker)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(removed.DeleteMarker))
	assert.Equal(t, marker, aws.ToString(removed.VersionId))
	body, err = s3Read(t, c, bucket, "report", "")
	require.NoError(t, err)
	assert.Equal(t, "second", body, "removing the delete marker brings the newest version back")

	removed, err = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("report"), VersionId: aws.String(second)})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(removed.DeleteMarker))
	body, err = s3Read(t, c, bucket, "report", "")
	require.NoError(t, err)
	assert.Equal(t, "first", body, "removing the current version makes the previous one current")

	_, err = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("report"), VersionId: aws.String(first)})
	require.NoError(t, err)
	versions, markers = s3ListVersions(t, c, bucket)
	assert.Empty(t, versions)
	assert.Empty(t, markers)
	_, err = c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

// TestS3_VersioningSuspendedWritesTheNullVersion covers a suspended bucket:
// a write replaces the null version and leaves the versions written while
// versioning was enabled, and a delete puts a null delete marker in its place.
func TestS3_VersioningSuspendedWritesTheNullVersion(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "suspended", types.BucketVersioningStatusEnabled)
	kept := aws.ToString(s3Put(t, c, bucket, "doc", "enabled").VersionId)
	s3SetVersioning(t, c, bucket, types.BucketVersioningStatusSuspended)

	assert.Equal(t, "null", aws.ToString(s3Put(t, c, bucket, "doc", "suspended one").VersionId))
	assert.Equal(t, "null", aws.ToString(s3Put(t, c, bucket, "doc", "suspended two").VersionId))
	body, err := s3Read(t, c, bucket, "doc", "null")
	require.NoError(t, err)
	assert.Equal(t, "suspended two", body, "a suspended write replaces the null version")
	versions, _ := s3ListVersions(t, c, bucket)
	assert.Equal(t, []s3ListedVersion{{"doc", "null", true, false}, {"doc", kept, false, false}}, versions)

	deleted, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc")})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(deleted.DeleteMarker))
	assert.Equal(t, "null", aws.ToString(deleted.VersionId))
	versions, markers := s3ListVersions(t, c, bucket)
	assert.Equal(t, []s3ListedVersion{{"doc", kept, false, false}}, versions, "the null delete marker replaces the null version")
	assert.Equal(t, []s3ListedVersion{{"doc", "null", true, true}}, markers)
	body, err = s3Read(t, c, bucket, "doc", kept)
	require.NoError(t, err)
	assert.Equal(t, "enabled", body)
}

// TestS3_DeleteObjectsVersionedEntries deletes by version and by key in one
// DeleteObjects request and reads back what each entry did.
func TestS3_DeleteObjectsVersionedEntries(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "batch-versions", types.BucketVersioningStatusEnabled)
	old := aws.ToString(s3Put(t, c, bucket, "a", "a1").VersionId)
	s3Put(t, c, bucket, "a", "a2")
	s3Put(t, c, bucket, "b", "b1")

	out, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &types.Delete{
		Objects: []types.ObjectIdentifier{{Key: aws.String("a"), VersionId: aws.String(old)}, {Key: aws.String("b")}}}})
	require.NoError(t, err)
	require.Empty(t, out.Errors)
	require.Len(t, out.Deleted, 2)
	byKey := map[string]types.DeletedObject{}
	for _, d := range out.Deleted {
		byKey[aws.ToString(d.Key)] = d
	}
	assert.Equal(t, old, aws.ToString(byKey["a"].VersionId))
	assert.False(t, aws.ToBool(byKey["a"].DeleteMarker))
	assert.True(t, aws.ToBool(byKey["b"].DeleteMarker))
	assert.NotEmpty(t, aws.ToString(byKey["b"].DeleteMarkerVersionId))

	versions, markers := s3ListVersions(t, c, bucket)
	require.Len(t, versions, 2)
	assert.Equal(t, s3ListedVersion{"b", aws.ToString(byKey["b"].DeleteMarkerVersionId), true, true}, markers[0])
	_, err = s3Read(t, c, bucket, "a", old)
	assert.Equal(t, "NoSuchVersion", errCodeOf(err))
}

// TestS3_CopyObjectReadsTheNamedSourceVersion copies a noncurrent version.
func TestS3_CopyObjectReadsTheNamedSourceVersion(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "copy-versions", types.BucketVersioningStatusEnabled)
	first := aws.ToString(s3Put(t, c, bucket, "src", "original").VersionId)
	s3Put(t, c, bucket, "src", "rewritten")

	out, err := c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("dst"),
		CopySource: aws.String(bucket + "/src?versionId=" + first)})
	require.NoError(t, err)
	assert.Equal(t, first, aws.ToString(out.CopySourceVersionId))
	assert.NotEmpty(t, aws.ToString(out.VersionId))
	body, err := s3Read(t, c, bucket, "dst", "")
	require.NoError(t, err)
	assert.Equal(t, "original", body)

	deleted, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("src")})
	require.NoError(t, err)
	_, err = c.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("dst"),
		CopySource: aws.String(bucket + "/src?versionId=" + aws.ToString(deleted.VersionId))})
	assert.Equal(t, "InvalidRequest", errCodeOf(err), "a copy may not name a delete marker")
}

// TestS3_ObjectTagsBelongToTheirVersion tags two versions differently and
// reads each back by version.
func TestS3_ObjectTagsBelongToTheirVersion(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "tag-versions", types.BucketVersioningStatusEnabled)
	first, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("k"),
		Body: strings.NewReader("1"), Tagging: aws.String("stage=draft")})
	require.NoError(t, err)
	s3Put(t, c, bucket, "k", "2")
	_, err = c.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("k"),
		Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("stage"), Value: aws.String("final")}}}})
	require.NoError(t, err)

	tags := func(version string) string {
		in := &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("k")}
		if version != "" {
			in.VersionId = aws.String(version)
		}
		out, err := c.GetObjectTagging(ctx, in)
		require.NoError(t, err)
		require.Len(t, out.TagSet, 1)
		return aws.ToString(out.TagSet[0].Value)
	}
	assert.Equal(t, "final", tags(""))
	assert.Equal(t, "draft", tags(aws.ToString(first.VersionId)), "a noncurrent version keeps its own tags")
}

// TestS3_ListObjectVersionsPages pages through a listing one entry at a time
// with the key and version-id markers each page returns.
func TestS3_ListObjectVersionsPages(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "page-versions", types.BucketVersioningStatusEnabled)
	a1 := aws.ToString(s3Put(t, c, bucket, "a", "1").VersionId)
	a2 := aws.ToString(s3Put(t, c, bucket, "a", "2").VersionId)
	b1 := aws.ToString(s3Put(t, c, bucket, "b", "1").VersionId)

	var seen []string
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), MaxKeys: aws.Int32(1)}
	for {
		out, err := c.ListObjectVersions(ctx, in)
		require.NoError(t, err)
		for _, v := range out.Versions {
			seen = append(seen, aws.ToString(v.Key)+"@"+aws.ToString(v.VersionId))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
	assert.Equal(t, []string{"a@" + a2, "a@" + a1, "b@" + b1}, seen)
}

// TestS3_DeleteObjectsRefusesEachEntryOnItsOwn deletes two keys as a caller
// that may delete only one: the request succeeds, the other entry comes back
// as an AccessDenied error, and its object stays.
func TestS3_DeleteObjectsRefusesEachEntryOnItsOwn(t *testing.T) {
	admin := s3Client()
	bucket := uniqueName("delete-objects-partial")
	s3CreateBucket(t, admin, bucket)
	s3Put(t, admin, bucket, "scratch/tmp", "x")
	s3Put(t, admin, bucket, "records/keep", "y")

	akid, secret := restrictedCredential(t, "s3-delete-scratch",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:DeleteObject",
		  "Resource":"arn:aws:s3:::`+bucket+`/scratch/*"}]}`)
	restricted := s3.NewFromConfig(keyConfig(akid, secret), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.UsePathStyle = true
	})
	out, err := restricted.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &types.Delete{
		Objects: []types.ObjectIdentifier{{Key: aws.String("scratch/tmp")}, {Key: aws.String("records/keep")}}}})
	require.NoError(t, err, "a refused entry does not refuse the request")
	require.Len(t, out.Deleted, 1)
	assert.Equal(t, "scratch/tmp", aws.ToString(out.Deleted[0].Key))
	require.Len(t, out.Errors, 1)
	assert.Equal(t, "records/keep", aws.ToString(out.Errors[0].Key))
	assert.Equal(t, "AccessDenied", aws.ToString(out.Errors[0].Code))

	_, err = s3Read(t, admin, bucket, "scratch/tmp", "")
	assert.Equal(t, "NoSuchKey", errCodeOf(err))
	body, err := s3Read(t, admin, bucket, "records/keep", "")
	require.NoError(t, err)
	assert.Equal(t, "y", body)
}

// TestLambda_CreateFunctionReadsTheNamedS3ObjectVersion deploys a function
// from a noncurrent version of its deployment package: AWS Lambda reads the
// version S3ObjectVersion names, not the key's current object.
func TestLambda_CreateFunctionReadsTheNamedS3ObjectVersion(t *testing.T) {
	c := s3Client()
	bucket := s3VersionedBucket(t, c, "lambda-code-versions", types.BucketVersioningStatusEnabled)
	deployed := lambdaNodeDeploymentZip(t, "event")
	put := func(zip []byte) string {
		out, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("function.zip"), Body: bytes.NewReader(zip)})
		require.NoError(t, err)
		return aws.ToString(out.VersionId)
	}
	version := put(deployed)
	put(lambdaNodeDeploymentZip(t, "'superseded'"))

	name := uniqueName("versioned-code")
	created, err := lambdaClient().CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{S3Bucket: aws.String(bucket), S3Key: aws.String("function.zip"),
			S3ObjectVersion: aws.String(version)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaClient().DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})
	sum := sha256.Sum256(deployed)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sum[:]), aws.ToString(created.CodeSha256))
}
