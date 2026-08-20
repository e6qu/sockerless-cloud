package azure_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	blobsas "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	filesas "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/service"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	queuesas "github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue/sas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Queue and File services verify the signatures their own SDKs produce.
//
// Each storage service signs a different string — the Queue service eight
// fields, the File service thirteen, the Blob service a version-layered
// sixteen — and the only sound check of the simulator's reconstruction is a
// signature Microsoft's implementation built. The Blob layout is held by the
// App Service backup tests through azblob's GetSASURL; these two hold the
// other services the same way, so the three layouts cannot drift apart from
// what real clients sign.

func TestQueueSDK_ServiceSASSignedByTheSDKAuthorizes(t *testing.T) {
	const account, queueName = "sdkqueuesasacct", "sdk-sas-queue"
	key := storageAccountPrimaryKey(account)
	require.NotEmpty(t, key, "the account must serve a key to sign with")

	credential, err := azqueue.NewSharedKeyCredential(account, key)
	require.NoError(t, err)
	serviceClient, err := azqueue.NewServiceClientWithSharedKeyCredential(
		storageSDKURL(t, account, "queue"), credential,
		&azqueue.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

	// The SDK's own signer builds the eight-field queue signature.
	values := queuesas.QueueSignatureValues{
		Protocol:    queuesas.ProtocolHTTPSandHTTP,
		ExpiryTime:  time.Now().UTC().Add(time.Hour),
		Permissions: (&queuesas.QueuePermissions{Read: true, Add: true, Process: true}).String(),
		QueueName:   queueName,
	}
	params, err := values.SignWithSharedKey(credential)
	require.NoError(t, err)

	// The signed URL alone — no other credential — enqueues and reads.
	enqueue := `<QueueMessage><MessageText>signed-by-the-sdk</MessageText></QueueMessage>`
	target := "/" + queueName + "/messages?" + params.Encode()
	resp := storageRawRequestSASWithBody(t, http.MethodPost, account, "queue", target, enqueue)
	require.Equal(t, http.StatusCreated, resp.StatusCode, storageReadBody(t, resp))

	read := storageRawRequestSASWithBody(t, http.MethodGet, account, "queue", target, "")
	require.Equal(t, http.StatusOK, read.StatusCode)
	assert.Contains(t, storageReadBody(t, read), "signed-by-the-sdk")

	// The same signature does not delete the queue: neither the permissions
	// nor the method survive tampering, and delete was never granted.
	del := storageRawRequestSASWithBody(t, http.MethodDelete, account, "queue",
		"/"+queueName+"?"+params.Encode(), "")
	require.Equal(t, http.StatusForbidden, del.StatusCode)
}

func TestFilesSDK_ServiceSASSignedByTheSDKAuthorizes(t *testing.T) {
	const account, share, fileName = "sdkfilesasacct", "sdk-sas-share", "signed.txt"
	key := storageAccountPrimaryKey(account)
	require.NotEmpty(t, key, "the account must serve a key to sign with")

	credential, err := service.NewSharedKeyCredential(account, key)
	require.NoError(t, err)
	serviceClient, err := service.NewClientWithSharedKeyCredential(
		storageSDKURL(t, account, "file"), credential,
		&service.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	shareClient := serviceClient.NewShareClient(share)
	_, err = shareClient.Create(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = shareClient.Delete(ctx, nil) })

	// Seed a file through the Shared Key client.
	fileClient := shareClient.NewRootDirectoryClient().NewFileClient(fileName)
	payload := "thirteen fields, signed by azfile"
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	err = fileClient.UploadBuffer(ctx, []byte(payload), nil)
	require.NoError(t, err)

	// The SDK's own signer builds the thirteen-field share signature.
	params, err := filesas.SignatureValues{
		Protocol:    filesas.ProtocolHTTPSandHTTP,
		ExpiryTime:  time.Now().UTC().Add(time.Hour),
		Permissions: (&filesas.SharePermissions{Read: true, List: true}).String(),
		ShareName:   share,
	}.SignWithSharedKey(credential)
	require.NoError(t, err)

	// The signed URL alone reads the file back.
	resp := storageRawRequestSASWithBody(t, http.MethodGet, account, "file",
		"/"+share+"/"+fileName+"?"+params.Encode(), "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, payload, storageReadBody(t, resp))

	// A read signature does not write.
	write := storageRawRequestSASWithBody(t, http.MethodPut, account, "file",
		"/"+share+"/other.txt?"+params.Encode(), "")
	require.Equal(t, http.StatusForbidden, write.StatusCode)
}

// storageRawRequestSASWithBody issues one request whose only credential is the
// SAS already in the target. The general raw helpers would add a Shared Key
// header; a SAS request must stand on the signature alone.
func storageRawRequestSASWithBody(t *testing.T, method, account, service_, target, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+target, reader)
	require.NoError(t, err)
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	req.Host = account + "." + service_ + ".localhost:" + port
	req.Header.Set("x-ms-version", "2025-01-05")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestBlobSDK_AccountSASSignedByTheSDKAuthorizes holds the account Shared
// Access Signature — the shape `data.azurerm_storage_account_sas` emits, and
// the one the azurerm provider's own App Service backup example feeds into
// `storage_account_url` — to azblob's own account signer, ten fields and the
// terminating newline the account form alone carries.
func TestBlobSDK_AccountSASSignedByTheSDKAuthorizes(t *testing.T) {
	const account, containerName, blobName = "sdkacctsasacct", "acct-sas-container", "signed.txt"
	client := newBlobTestClient(t, account)
	newBlobTestContainer(t, client, containerName)
	payload := "ten fields and a terminating newline"
	_, err := client.UploadBuffer(ctx, containerName, blobName, []byte(payload), nil)
	require.NoError(t, err)

	credential, err := azblob.NewSharedKeyCredential(account, storageAccountPrimaryKey(account))
	require.NoError(t, err)
	params, err := blobsas.AccountSignatureValues{
		Protocol:      blobsas.ProtocolHTTPSandHTTP,
		ExpiryTime:    time.Now().UTC().Add(time.Hour),
		Permissions:   (&blobsas.AccountPermissions{Read: true, List: true}).String(),
		ResourceTypes: (&blobsas.AccountResourceTypes{Container: true, Object: true}).String(),
	}.SignWithSharedKey(credential)
	require.NoError(t, err)

	// The signed URL alone reads the blob back.
	resp := storageRawRequestSASWithBody(t, http.MethodGet, account, "blob",
		"/"+containerName+"/"+blobName+"?"+params.Encode(), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "account SAS read")
	assert.Equal(t, payload, storageReadBody(t, resp))

	// A read-only account signature does not write.
	write := storageRawRequestSASWithBody(t, http.MethodPut, account, "blob",
		"/"+containerName+"/other.txt?"+params.Encode(), "")
	require.Equal(t, http.StatusForbidden, write.StatusCode)
}
