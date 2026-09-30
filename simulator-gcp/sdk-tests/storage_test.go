package gcp_sdk_test

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	storageapi "google.golang.org/api/storage/v1"
)

func storageClient(t *testing.T) *storage.Client {
	t.Helper()
	// Use STORAGE_EMULATOR_HOST for proper download URL construction, and a
	// real simulator-minted token so the client authenticates against the data
	// plane the way it does against Google.
	host := strings.TrimPrefix(baseURL, "http://")
	t.Setenv("STORAGE_EMULATOR_HOST", host)
	client, err := storage.NewClient(ctx, option.WithHTTPClient(simAuthHTTPClient()))
	require.NoError(t, err)
	return client
}

func TestGCS_CreateBucket(t *testing.T) {
	bucketSdkTestBucket := uniqueName("sdk-test-bucket")
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket(bucketSdkTestBucket).Create(ctx, "test-project", nil)
	require.NoError(t, err)

	attrs, err := client.Bucket(bucketSdkTestBucket).Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, bucketSdkTestBucket, attrs.Name)
}

func TestGCS_UploadAndDownload(t *testing.T) {
	bucketUploadBucket := uniqueName("upload-bucket")
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket(bucketUploadBucket).Create(ctx, "test-project", nil)
	require.NoError(t, err)

	// Upload
	w := client.Bucket(bucketUploadBucket).Object("hello.txt").NewWriter(ctx)
	_, err = w.Write([]byte("hello world"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// Download
	r, err := client.Bucket(bucketUploadBucket).Object("hello.txt").NewReader(ctx)
	require.NoError(t, err)
	defer r.Close()

	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
}

func TestGCS_ResumableWriterFollowsCustomEndpoint(t *testing.T) {
	bucketResumableSdkBucket := uniqueName("resumable-sdk-bucket")
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket(bucketResumableSdkBucket).Create(ctx, "test-project", nil)
	require.NoError(t, err)

	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	raw := make([]byte, 512*1024)
	_, err = rand.Read(raw)
	require.NoError(t, err)
	_, err = gz.Write(raw)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	w := client.Bucket(bucketResumableSdkBucket).Object("workspace.tar.gz").NewWriter(ctx)
	w.ChunkSize = 256 * 1024
	w.ContentType = "application/x-tar+gzip"
	_, err = w.Write(payload.Bytes())
	require.NoError(t, err)
	require.NoError(t, w.Close())

	r, err := client.Bucket(bucketResumableSdkBucket).Object("workspace.tar.gz").NewReader(ctx)
	require.NoError(t, err)
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, payload.Bytes(), got)
}

func TestGCS_JSONAPIObjectGetAltMedia(t *testing.T) {
	bucketJsonApiMediaBucket := uniqueName("json-api-media-bucket")
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket(bucketJsonApiMediaBucket)
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	payload := []byte{0x1f, 0x8b, 0x08, 0x00, 0x73, 0x6f, 0x63, 0x6b}
	w := bucket.Object("workspace/exec.tar.gz").NewWriter(ctx)
	w.ContentType = "application/x-tar+gzip"
	_, err := w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	svc, err := storageapi.NewService(ctx,
		option.WithEndpoint(baseURL+"/storage/v1/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	resp, err := svc.Objects.Get(bucketJsonApiMediaBucket, "workspace/exec.tar.gz").Download()
	require.NoError(t, err)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestGCS_ListObjects(t *testing.T) {
	bucketListObjBucket := uniqueName("list-obj-bucket")
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket(bucketListObjBucket).Create(ctx, "test-project", nil)
	require.NoError(t, err)

	for _, name := range []string{"b.txt", "a.txt", "c.txt"} {
		w := client.Bucket(bucketListObjBucket).Object(name).NewWriter(ctx)
		_, err := w.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	var names []string
	it := client.Bucket(bucketListObjBucket).Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, attrs.Name)
	}
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, names)
}

// TestGCS_ListObjectsPaged verifies the GCS object list honors the page-size
// limit the storage client sends as the "maxResults" query parameter. A single
// page must be capped at the requested size, and paging through must still yield
// every object exactly once.
func TestGCS_ListObjectsPaged(t *testing.T) {
	bucketPagedObjBucket := uniqueName("paged-obj-bucket")
	client := storageClient(t)
	defer client.Close()

	require.NoError(t, client.Bucket(bucketPagedObjBucket).Create(ctx, "test-project", nil))

	for _, name := range []string{"o1", "o2", "o3", "o4", "o5"} {
		w := client.Bucket(bucketPagedObjBucket).Object(name).NewWriter(ctx)
		_, err := w.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	it := client.Bucket(bucketPagedObjBucket).Objects(ctx, nil)
	pager := iterator.NewPager(it, 2, "")

	var firstPage []*storage.ObjectAttrs
	nextTok, err := pager.NextPage(&firstPage)
	require.NoError(t, err)
	// maxResults=2 must cap the first page; if the sim ignored it (read
	// "pageSize") the whole listing would come back in one page.
	require.Len(t, firstPage, 2, "first page must honor maxResults=2")
	require.NotEmpty(t, nextTok, "more objects remain, expected a nextPageToken")

	all := append([]*storage.ObjectAttrs{}, firstPage...)
	for nextTok != "" {
		var page []*storage.ObjectAttrs
		nextTok, err = pager.NextPage(&page)
		require.NoError(t, err)
		all = append(all, page...)
	}

	var names []string
	for _, a := range all {
		names = append(names, a.Name)
	}
	assert.Equal(t, []string{"o1", "o2", "o3", "o4", "o5"}, names)
}

func TestGCS_CopierFromRewriteTo(t *testing.T) {
	bucketCopyObjBucket := uniqueName("copy-obj-bucket")
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket(bucketCopyObjBucket)
	err := bucket.Create(ctx, "test-project", nil)
	require.NoError(t, err)

	src := bucket.Object("dir/source file.txt")
	w := src.NewWriter(ctx)
	w.ContentType = "text/plain"
	w.CacheControl = "public, max-age=60"
	w.ContentDisposition = `inline; filename="source.txt"`
	w.ContentLanguage = "en"
	w.Metadata = map[string]string{
		"replace": "source",
		"source":  "kept",
	}
	_, err = w.Write([]byte("copied through rewriteTo"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	dst := bucket.Object("copied/dest file.txt")
	copier := dst.CopierFrom(src)
	copier.ContentType = "application/x-dest"
	copier.CacheControl = "no-cache"
	copier.Metadata = map[string]string{
		"dest":    "yes",
		"replace": "dest",
	}
	attrs, err := copier.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, "copied/dest file.txt", attrs.Name)
	assert.Equal(t, int64(len("copied through rewriteTo")), attrs.Size)
	assert.Equal(t, "application/x-dest", attrs.ContentType)
	assert.Equal(t, "no-cache", attrs.CacheControl)
	assert.Equal(t, `inline; filename="source.txt"`, attrs.ContentDisposition)
	assert.Equal(t, "en", attrs.ContentLanguage)
	assert.Equal(t, map[string]string{"dest": "yes", "replace": "dest"}, attrs.Metadata)

	r, err := dst.NewReader(ctx)
	require.NoError(t, err)
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "copied through rewriteTo", string(got))
}

func TestGCS_CopierFromRewriteToRejectsInvalidMetadata(t *testing.T) {
	bucketCopyInvalidMetadataBucket := uniqueName("copy-invalid-metadata-bucket")
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket(bucketCopyInvalidMetadataBucket)
	err := bucket.Create(ctx, "test-project", nil)
	require.NoError(t, err)

	src := bucket.Object("source.txt")
	w := src.NewWriter(ctx)
	_, err = w.Write([]byte("copied through rewriteTo"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	copier := bucket.Object("bad-dest.txt").CopierFrom(src)
	copier.ContentLanguage = strings.Repeat("x", 101)
	_, err = copier.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contentLanguage")
}

func TestGCS_DeleteObject(t *testing.T) {
	bucketDelObjBucket := uniqueName("del-obj-bucket")
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket(bucketDelObjBucket).Create(ctx, "test-project", nil)
	require.NoError(t, err)

	object := client.Bucket(bucketDelObjBucket).Object("temp.txt")
	w := object.NewWriter(ctx)
	_, err = w.Write([]byte("temp"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// The object exists before the delete, so the delete below has something to
	// remove.
	attrs, err := object.Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(len("temp")), attrs.Size)

	require.NoError(t, object.Delete(ctx))

	// The delete removed the object: a read of its metadata reports it gone.
	_, err = object.Attrs(ctx)
	assert.ErrorIs(t, err, storage.ErrObjectNotExist)

	// A second delete has nothing left to remove.
	assert.ErrorIs(t, object.Delete(ctx), storage.ErrObjectNotExist)
}

// TestGCS_CreateBucketTwiceConflicts pins the answer Cloud Storage gives when a
// bucket is created a second time: 409 ALREADY_EXISTS, not a silent success.
// The build helpers treat that conflict as "the bucket is there", so the
// simulator has to keep reporting it for that reading to stay true.
func TestGCS_CreateBucketTwiceConflicts(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	name := uniqueName("sdk-duplicate-bucket")
	require.NoError(t, client.Bucket(name).Create(ctx, "test-project", nil))
	t.Cleanup(func() { assert.NoError(t, client.Bucket(name).Delete(ctx)) })

	err := client.Bucket(name).Create(ctx, "test-project", nil)
	require.Error(t, err, "creating an existing bucket conflicts")
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code)
}

// TestGCS_DeleteNonEmptyBucketConflicts pins that Cloud Storage refuses to
// delete a bucket that still holds a live object, and keeps both, while a
// bucket whose only objects are soft-deleted deletes.
func TestGCS_DeleteNonEmptyBucketConflicts(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket(uniqueName("sdk-nonempty-bucket"))
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	object := bucket.Object("keep.txt")
	_, err := writeObject(t, object, "kept")
	require.NoError(t, err)

	err = bucket.Delete(ctx)
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr, "deleting a bucket that holds an object fails")
	assert.Equal(t, http.StatusConflict, apiErr.Code)
	assert.Equal(t, "The bucket you tried to delete is not empty.", apiErr.Message)
	require.Len(t, apiErr.Errors, 1)
	assert.Equal(t, "conflict", apiErr.Errors[0].Reason)

	_, err = bucket.Attrs(ctx)
	require.NoError(t, err, "the refused delete keeps the bucket")
	_, err = object.Attrs(ctx)
	require.NoError(t, err, "the refused delete keeps the object")

	require.NoError(t, object.Delete(ctx))
	require.NoError(t, bucket.Delete(ctx), "a bucket whose objects are only soft-deleted deletes")
	_, err = bucket.Attrs(ctx)
	assert.ErrorIs(t, err, storage.ErrBucketNotExist)
}
