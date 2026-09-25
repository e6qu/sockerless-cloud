package azure_sdk_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/stretchr/testify/require"
)

// Conditional headers are what a client builds optimistic concurrency on: a
// read that moves no bytes while its copy is current, a create that loses to a
// blob already there, a replace that loses to any write since the version read.

func etagCondition(etag *azcore.ETag) *blob.AccessConditions {
	return &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfMatch: etag}}
}

func absentCondition() *blob.AccessConditions {
	return &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: to.Ptr(azcore.ETagAny)}}
}

func TestStorageSDK_BlobConditionalReads(t *testing.T) {
	account, containerName := "sdkcondreadacct", "cond-read"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	blobClient := containerClient.NewBlockBlobClient("object")

	uploaded, err := blobClient.UploadBuffer(ctx, []byte("first"), nil)
	require.NoError(t, err)
	held := uploaded.ETag

	// The SDK reports a 304 as a response with no body, not as an error.
	current, err := blobClient.DownloadStream(ctx, &blob.DownloadStreamOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: held}},
	})
	require.NoError(t, err)
	require.Nil(t, current.Body, "a read at the version held must move no body")

	_, err = blobClient.GetProperties(ctx, &blob.GetPropertiesOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: held}},
	})
	var refused *azcore.ResponseError
	require.True(t, errors.As(err, &refused), "Get Blob Properties at the version held: %v", err)
	require.Equal(t, http.StatusNotModified, refused.StatusCode)

	_, err = blobClient.DownloadStream(ctx, &blob.DownloadStreamOptions{
		AccessConditions: etagCondition(to.Ptr(azcore.ETag(`"0x0000000000000000"`))),
	})
	requireBlobErrorCode(t, err, string(bloberror.ConditionNotMet), "a read whose If-Match names another version")

	after := time.Now().Add(time.Hour)
	unmodified, err := blobClient.DownloadStream(ctx, &blob.DownloadStreamOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfModifiedSince: &after}},
	})
	require.NoError(t, err)
	require.Nil(t, unmodified.Body, "a read of a blob not modified since the time given must move no body")

	_, err = blobClient.UploadBuffer(ctx, []byte("second"), nil)
	require.NoError(t, err)
	changed, err := blobClient.DownloadStream(ctx, &blob.DownloadStreamOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: held}},
	})
	require.NoError(t, err)
	require.NotNil(t, changed.Body, "a read at a version since replaced must move the new body")
	body, err := readAllAndClose(changed.Body)
	require.NoError(t, err)
	require.Equal(t, "second", body)
}

func TestStorageSDK_BlobConditionalWrites(t *testing.T) {
	account, containerName := "sdkcondwriteacct", "cond-write"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	// Put Blob.
	single := containerClient.NewBlockBlobClient("single")
	first, err := single.Upload(ctx, streamOf("one"), &blockblob.UploadOptions{AccessConditions: absentCondition()})
	require.NoError(t, err)
	_, err = single.Upload(ctx, streamOf("two"), &blockblob.UploadOptions{AccessConditions: absentCondition()})
	requireBlobErrorCode(t, err, string(bloberror.BlobAlreadyExists), "Put Blob if absent over a blob")
	_, err = single.Upload(ctx, streamOf("two"), &blockblob.UploadOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: first.ETag}},
	})
	requireBlobErrorCode(t, err, string(bloberror.ConditionNotMet), "Put Blob whose If-None-Match names the current version")
	second, err := single.Upload(ctx, streamOf("two"), &blockblob.UploadOptions{AccessConditions: etagCondition(first.ETag)})
	require.NoError(t, err)
	_, err = single.Upload(ctx, streamOf("three"), &blockblob.UploadOptions{AccessConditions: etagCondition(first.ETag)})
	requireBlobErrorCode(t, err, string(bloberror.ConditionNotMet), "Put Blob at a version since replaced")

	// Put Block List: the commit is the write, so the conditions are its.
	blocks := containerClient.NewBlockBlobClient("blocks")
	commit := func(content string, conditions *blob.AccessConditions) (blockblob.CommitBlockListResponse, error) {
		id := blockID(content)
		_, err := blocks.StageBlock(ctx, id, streamOf(content), nil)
		require.NoError(t, err)
		return blocks.CommitBlockList(ctx, []string{id}, &blockblob.CommitBlockListOptions{AccessConditions: conditions})
	}
	committed, err := commit("alpha", absentCondition())
	require.NoError(t, err)
	_, err = commit("beta", absentCondition())
	requireBlobErrorCode(t, err, string(bloberror.BlobAlreadyExists), "Put Block List if absent over a blob")
	_, err = commit("gamma", etagCondition(second.ETag))
	requireBlobErrorCode(t, err, string(bloberror.ConditionNotMet), "Put Block List at another blob's version")
	_, err = commit("delta", etagCondition(committed.ETag))
	require.NoError(t, err)
	body, err := downloadString(blocks.BlobClient())
	require.NoError(t, err)
	require.Equal(t, "delta", body, "only the commits whose conditions held may have landed")

	// Copy Blob: the conditions are the destination's.
	source := containerClient.NewBlobClient("single")
	_, err = blocks.StartCopyFromURL(ctx, source.URL(), &blob.StartCopyFromURLOptions{AccessConditions: absentCondition()})
	requireBlobErrorCode(t, err, string(bloberror.BlobAlreadyExists), "Copy Blob if absent over a blob")

	// Delete Blob.
	_, err = single.Delete(ctx, &blob.DeleteOptions{AccessConditions: etagCondition(first.ETag)})
	requireBlobErrorCode(t, err, string(bloberror.ConditionNotMet), "Delete Blob at a version since replaced")
	_, err = single.Delete(ctx, &blob.DeleteOptions{AccessConditions: etagCondition(second.ETag)})
	require.NoError(t, err)
}

// TestStorageSDK_BlobConditionalWritesArbitrate races writers that each require
// the state they read. Azure evaluates a write's conditions and applies it as
// one step, so exactly one writer of each round succeeds.
func TestStorageSDK_BlobConditionalWritesArbitrate(t *testing.T) {
	account, containerName := "sdkcondraceacct", "cond-race"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	const writers = 16

	race := func(write func(i int) error) (won int, lost []error) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := write(i)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					won++
				} else {
					lost = append(lost, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		return won, lost
	}

	target := containerClient.NewBlockBlobClient("contended")
	won, lost := race(func(i int) error {
		_, err := target.Upload(ctx, streamOf(fmt.Sprint(i)), &blockblob.UploadOptions{AccessConditions: absentCondition()})
		return err
	})
	require.Equal(t, 1, won, "conditional creates that succeeded")
	for _, err := range lost {
		requireBlobErrorCode(t, err, string(bloberror.BlobAlreadyExists), "a create that lost")
	}

	properties, err := target.GetProperties(ctx, nil)
	require.NoError(t, err)
	won, lost = race(func(i int) error {
		_, err := target.Upload(ctx, streamOf(fmt.Sprint("replace", i)), &blockblob.UploadOptions{AccessConditions: etagCondition(properties.ETag)})
		return err
	})
	require.Equal(t, 1, won, "conditional replaces of one version that succeeded")
	for _, err := range lost {
		requireBlobErrorCode(t, err, string(bloberror.ConditionNotMet), "a replace that lost")
	}
}

func TestStorageSDK_BlobCopyOfAMissingSourceNamesTheSourceFailure(t *testing.T) {
	account, containerName := "sdkcopymissacct", "copy-missing"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	_, err := containerClient.NewBlobClient("destination").StartCopyFromURL(ctx, containerClient.NewBlobClient("absent").URL(), nil)
	var refused *azcore.ResponseError
	require.True(t, errors.As(err, &refused), "copy of a missing source: %v", err)
	require.Equal(t, string(bloberror.CannotVerifyCopySource), refused.ErrorCode)
	require.Equal(t, http.StatusNotFound, refused.StatusCode)
	require.Equal(t, "404", refused.RawResponse.Header.Get("x-ms-copy-source-status-code"))
	require.Equal(t, string(bloberror.BlobNotFound), refused.RawResponse.Header.Get("x-ms-copy-source-error-code"))
}

func streamOf(content string) io.ReadSeekCloser {
	return streaming.NopCloser(strings.NewReader(content))
}

// blockID names a block by its content, padded so every ID in a blob has one
// length, which the service requires.
func blockID(content string) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%-16s", content)))
}

func readAllAndClose(body io.ReadCloser) (string, error) {
	defer func() { _ = body.Close() }()
	content, err := io.ReadAll(body)
	return string(content), err
}

func downloadString(client *blob.Client) (string, error) {
	downloaded, err := client.DownloadStream(ctx, nil)
	if err != nil {
		return "", err
	}
	return readAllAndClose(downloaded.Body)
}
