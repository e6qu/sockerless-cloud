package gcp_sdk_test

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket("sdk-test-bucket").Create(ctx, "test-project", nil)
	require.NoError(t, err)

	attrs, err := client.Bucket("sdk-test-bucket").Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, "sdk-test-bucket", attrs.Name)
}

func TestGCS_UploadAndDownload(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket("upload-bucket").Create(ctx, "test-project", nil)
	require.NoError(t, err)

	// Upload
	w := client.Bucket("upload-bucket").Object("hello.txt").NewWriter(ctx)
	_, err = w.Write([]byte("hello world"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// Download
	r, err := client.Bucket("upload-bucket").Object("hello.txt").NewReader(ctx)
	require.NoError(t, err)
	defer r.Close()

	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
}

func TestGCS_ResumableWriterFollowsCustomEndpoint(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket("resumable-sdk-bucket").Create(ctx, "test-project", nil)
	require.NoError(t, err)

	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	raw := make([]byte, 512*1024)
	_, err = rand.Read(raw)
	require.NoError(t, err)
	_, err = gz.Write(raw)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	w := client.Bucket("resumable-sdk-bucket").Object("workspace.tar.gz").NewWriter(ctx)
	w.ChunkSize = 256 * 1024
	w.ContentType = "application/x-tar+gzip"
	_, err = w.Write(payload.Bytes())
	require.NoError(t, err)
	require.NoError(t, w.Close())

	r, err := client.Bucket("resumable-sdk-bucket").Object("workspace.tar.gz").NewReader(ctx)
	require.NoError(t, err)
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, payload.Bytes(), got)
}

func TestGCS_JSONAPIObjectGetAltMedia(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket("json-api-media-bucket")
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
	resp, err := svc.Objects.Get("json-api-media-bucket", "workspace/exec.tar.gz").Download()
	require.NoError(t, err)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestGCS_ListObjects(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket("list-obj-bucket").Create(ctx, "test-project", nil)
	require.NoError(t, err)

	for _, name := range []string{"b.txt", "a.txt", "c.txt"} {
		w := client.Bucket("list-obj-bucket").Object(name).NewWriter(ctx)
		_, err := w.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	var names []string
	it := client.Bucket("list-obj-bucket").Objects(ctx, nil)
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
	client := storageClient(t)
	defer client.Close()

	require.NoError(t, client.Bucket("paged-obj-bucket").Create(ctx, "test-project", nil))

	for _, name := range []string{"o1", "o2", "o3", "o4", "o5"} {
		w := client.Bucket("paged-obj-bucket").Object(name).NewWriter(ctx)
		_, err := w.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	it := client.Bucket("paged-obj-bucket").Objects(ctx, nil)
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
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket("copy-obj-bucket")
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
	client := storageClient(t)
	defer client.Close()

	bucket := client.Bucket("copy-invalid-metadata-bucket")
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
	client := storageClient(t)
	defer client.Close()

	err := client.Bucket("del-obj-bucket").Create(ctx, "test-project", nil)
	require.NoError(t, err)

	w := client.Bucket("del-obj-bucket").Object("temp.txt").NewWriter(ctx)
	w.Write([]byte("temp"))
	w.Close()

	err = client.Bucket("del-obj-bucket").Object("temp.txt").Delete(ctx)
	require.NoError(t, err)
}
