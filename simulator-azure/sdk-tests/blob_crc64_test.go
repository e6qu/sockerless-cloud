package azure_sdk_test

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"hash/crc64"
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/appendblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/pageblob"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// azureCRC64 is the CRC64 the Azure Storage SDKs compute, spelled as
// x-ms-content-crc64 carries it.
func azureCRC64(data []byte) []byte {
	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, crc64.Checksum(data, crc64.MakeTable(0x9A6C9329AC4BC9B5)))
	return out
}

// wrongCRC64 is a CRC64 that no content of the tests below has.
func wrongCRC64(data []byte) uint64 {
	return binary.LittleEndian.Uint64(azureCRC64(data)) ^ 1
}

func requireBlobStatus(t *testing.T, err error, status int, what string) {
	t.Helper()
	require.Error(t, err, "%s: expected the service to refuse the request", what)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "%s: want *azcore.ResponseError, got %T: %v", what, err, err)
	require.Equal(t, status, respErr.StatusCode, "%s: %v", what, err)
}

func readerOf(data []byte) io.ReadSeekCloser {
	return streaming.NopCloser(bytes.NewReader(data))
}

// The azblob client computes a CRC64 of every body it sends with
// TransferValidationTypeComputeCRC64, and the service checks it against what
// arrived and answers with its own.
func TestStorageSDK_BlobBodyWritesValidateCRC64(t *testing.T) {
	t.Parallel()
	account, containerName := "sdkcrc64acct", "crc64-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	payload := []byte("the CRC64 the Azure Storage SDKs compute")
	want := azureCRC64(payload)

	// Put Blob.
	blockBlob := containerClient.NewBlockBlobClient("put-blob.txt")
	uploaded, err := blockBlob.Upload(ctx, readerOf(payload), &blockblob.UploadOptions{
		TransactionalValidation: blob.TransferValidationTypeComputeCRC64(),
	})
	require.NoError(t, err)
	assert.Equal(t, want, uploaded.ContentCRC64, "Put Blob answers a block blob with its CRC64")
	_, err = blockBlob.Upload(ctx, readerOf(payload), &blockblob.UploadOptions{
		TransactionalValidation: blob.TransferValidationTypeCRC64(wrongCRC64(payload)),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Put Blob with a wrong CRC64")
	payloadMD5 := md5.Sum(payload)
	_, err = blockBlob.Upload(ctx, readerOf(payload), &blockblob.UploadOptions{
		TransactionalContentMD5: payloadMD5[:],
		TransactionalValidation: blob.TransferValidationTypeComputeCRC64(),
	})
	requireBlobStatus(t, err, http.StatusBadRequest, "Put Blob stating both Content-MD5 and x-ms-content-crc64")

	// Put Block and Put Block List.
	staged := containerClient.NewBlockBlobClient("staged.txt")
	const blockID = "YmxvY2stMDAwMDAwMQ=="
	stageResp, err := staged.StageBlock(ctx, blockID, readerOf(payload), &blockblob.StageBlockOptions{
		TransactionalValidation: blob.TransferValidationTypeComputeCRC64(),
	})
	require.NoError(t, err)
	assert.Equal(t, want, stageResp.ContentCRC64)
	assert.Nil(t, stageResp.ContentMD5, "Put Block answers with Content-MD5 only when the request states one")
	_, err = staged.StageBlock(ctx, blockID, readerOf(payload), &blockblob.StageBlockOptions{
		TransactionalValidation: blob.TransferValidationTypeCRC64(wrongCRC64(payload)),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Put Block with a wrong CRC64")
	stageMD5, err := staged.StageBlock(ctx, blockID, readerOf(payload), &blockblob.StageBlockOptions{
		TransactionalValidation: blob.TransferValidationTypeMD5(payloadMD5[:]),
	})
	require.NoError(t, err)
	assert.Equal(t, payloadMD5[:], stageMD5.ContentMD5)
	assert.Equal(t, want, stageMD5.ContentCRC64,
		"from 2026-10-06 Put Block answers with its CRC64 even when the request states an MD5")
	committed, err := staged.CommitBlockList(ctx, []string{blockID}, nil)
	require.NoError(t, err)
	assert.Len(t, committed.ContentCRC64, 8, "Put Block List answers with the CRC64 of its block list")
	assert.Nil(t, committed.ContentMD5)

	// Append Block.
	appendBlob := containerClient.NewAppendBlobClient("append.log")
	_, err = appendBlob.Create(ctx, nil)
	require.NoError(t, err)
	appended, err := appendBlob.AppendBlock(ctx, readerOf(payload), &appendblob.AppendBlockOptions{
		TransactionalValidation: blob.TransferValidationTypeComputeCRC64(),
	})
	require.NoError(t, err)
	assert.Equal(t, want, appended.ContentCRC64)
	_, err = appendBlob.AppendBlock(ctx, readerOf(payload), &appendblob.AppendBlockOptions{
		TransactionalValidation: blob.TransferValidationTypeCRC64(wrongCRC64(payload)),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Append Block with a wrong CRC64")

	// Put Page.
	pages := bytes.Repeat([]byte("p"), 512)
	pageBlob := containerClient.NewPageBlobClient("disk.vhd")
	_, err = pageBlob.Create(ctx, 1024, nil)
	require.NoError(t, err)
	written, err := pageBlob.UploadPages(ctx, readerOf(pages), blob.HTTPRange{Offset: 0, Count: 512},
		&pageblob.UploadPagesOptions{TransactionalValidation: blob.TransferValidationTypeComputeCRC64()})
	require.NoError(t, err)
	assert.Equal(t, azureCRC64(pages), written.ContentCRC64)
	_, err = pageBlob.UploadPages(ctx, readerOf(pages), blob.HTTPRange{Offset: 512, Count: 512},
		&pageblob.UploadPagesOptions{TransactionalValidation: blob.TransferValidationTypeCRC64(wrongCRC64(pages))})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Put Page with a wrong CRC64")

	// Set Blob Tags checks the CRC64 its tag document states.
	_, err = blockBlob.SetTags(ctx, map[string]string{"k": "v"}, &blob.SetTagsOptions{
		TransactionalContentCRC64: azureCRC64([]byte("not the tag document")),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Set Blob Tags with a wrong CRC64")

	// Nothing a refused write carried landed.
	download, err := pageBlob.DownloadStream(ctx, nil)
	require.NoError(t, err)
	var got bytes.Buffer
	_, err = got.ReadFrom(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, make([]byte, 512), got.Bytes()[512:], "a write refused for its CRC64 must not land")
}

// The from-URL writes check the CRC64 a caller states for the source range and
// answer with the CRC64 of what they read.
func TestStorageSDK_BlobFromURLWritesValidateSourceCRC64(t *testing.T) {
	t.Parallel()
	account, containerName := "sdkcrc64urlacct", "crc64-url-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	payload := bytes.Repeat([]byte("s"), 512)
	want := azureCRC64(payload)
	_, err := client.UploadBuffer(ctx, containerName, "source.bin", payload, nil)
	require.NoError(t, err)
	sourceURL := containerClient.NewBlobClient("source.bin").URL()

	// Put Block From URL.
	staged := containerClient.NewBlockBlobClient("staged.bin")
	const blockID = "YmxvY2stMDAwMDAwMQ=="
	stageResp, err := staged.StageBlockFromURL(ctx, blockID, sourceURL, &blockblob.StageBlockFromURLOptions{
		SourceContentValidation: blob.SourceContentValidationTypeCRC64(want),
	})
	require.NoError(t, err)
	assert.Equal(t, want, stageResp.ContentCRC64)
	_, err = staged.StageBlockFromURL(ctx, blockID, sourceURL, &blockblob.StageBlockFromURLOptions{
		SourceContentValidation: blob.SourceContentValidationTypeCRC64(azureCRC64([]byte("other"))),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Put Block From URL with a wrong source CRC64")

	// Append Block From URL.
	appendBlob := containerClient.NewAppendBlobClient("append.bin")
	_, err = appendBlob.Create(ctx, nil)
	require.NoError(t, err)
	appended, err := appendBlob.AppendBlockFromURL(ctx, sourceURL, &appendblob.AppendBlockFromURLOptions{
		SourceContentValidation: blob.SourceContentValidationTypeCRC64(want),
	})
	require.NoError(t, err)
	assert.Equal(t, want, appended.ContentCRC64)
	_, err = appendBlob.AppendBlockFromURL(ctx, sourceURL, &appendblob.AppendBlockFromURLOptions{
		SourceContentValidation: blob.SourceContentValidationTypeCRC64(azureCRC64([]byte("other"))),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Append Block From URL with a wrong source CRC64")

	// Put Page From URL.
	pageBlob := containerClient.NewPageBlobClient("disk.vhd")
	_, err = pageBlob.Create(ctx, 512, nil)
	require.NoError(t, err)
	written, err := pageBlob.UploadPagesFromURL(ctx, sourceURL, 0, 0, 512, &pageblob.UploadPagesFromURLOptions{
		SourceContentValidation: blob.SourceContentValidationTypeCRC64(want),
	})
	require.NoError(t, err)
	assert.Equal(t, want, written.ContentCRC64)
	_, err = pageBlob.UploadPagesFromURL(ctx, sourceURL, 0, 0, 512, &pageblob.UploadPagesFromURLOptions{
		SourceContentValidation: blob.SourceContentValidationTypeCRC64(azureCRC64([]byte("other"))),
	})
	requireBlobErrorCode(t, err, "Crc64Mismatch", "Put Page From URL with a wrong source CRC64")

	// Copy Blob From URL answers with the CRC64 of what it copied.
	copied, err := containerClient.NewBlobClient("copy.bin").CopyFromURL(ctx, sourceURL, nil)
	require.NoError(t, err)
	assert.Equal(t, want, copied.ContentCRC64)
	assert.Nil(t, copied.ContentMD5, "Copy Blob From URL answers with Content-MD5 only when the source MD5 is stated")
}
