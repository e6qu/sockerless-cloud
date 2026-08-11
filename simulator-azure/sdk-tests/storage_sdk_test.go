package azure_sdk_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/directory"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
	fileservice "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/service"
	fileshare "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/share"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type storageSDKTransport struct {
	inner *http.Client
}

func (t storageSDKTransport) Do(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	u := *req.URL
	rewritten.Host = req.URL.Host
	u.Host = strings.TrimPrefix(baseURL, "http://")
	u.Scheme = "http"
	rewritten.URL = &u
	client := t.inner
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(rewritten)
}

func storageSDKURL(t *testing.T, account, service string) string {
	t.Helper()
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	return "http://" + account + "." + service + ".localhost:" + port + "/"
}

func storageSDKOptions() azcore.ClientOptions {
	return azcore.ClientOptions{Transport: storageSDKTransport{}}
}

func storageAdvertisedEndpoint(t *testing.T, account, service string) string {
	t.Helper()
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	return fmt.Sprintf("http://%s.%s.shim.localhost:%s/", account, service, port)
}

func storageRawRequest(t *testing.T, method, account, service, target string) *http.Response {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	_, port, ok := strings.Cut(u.Host, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+target, nil)
	require.NoError(t, err)
	req.Host = fmt.Sprintf("%s.%s.localhost:%s", account, service, port)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestStorageSDK_BlobLifecycleAndPagedLists(t *testing.T) {
	account := "sdkblobacct"
	container := "sdk-blob-container"
	blobName := "one.txt"
	payload := []byte("hello from azblob")

	client, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = client.CreateContainer(ctx, container, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, container, nil) })

	_, err = client.UploadBuffer(ctx, container, blobName, payload, nil)
	require.NoError(t, err)

	download, err := client.DownloadStream(ctx, container, blobName, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, payload, got)

	blobPager := client.NewListBlobsFlatPager(container, &azblob.ListBlobsFlatOptions{MaxResults: to.Ptr(int32(1))})
	blobPage, err := blobPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotNil(t, blobPage.Segment)
	require.Len(t, blobPage.Segment.BlobItems, 1)
	require.NotNil(t, blobPage.Segment.BlobItems[0].Name)
	assert.Equal(t, blobName, *blobPage.Segment.BlobItems[0].Name)

	rawBlobList := storageRawRequest(t, http.MethodGet, account, "blob", "/"+container+"?restype=container&comp=list&maxresults=1")
	require.Equal(t, http.StatusOK, rawBlobList.StatusCode)
	rawBlobListBody, _ := io.ReadAll(rawBlobList.Body)
	rawBlobList.Body.Close()
	assert.True(t, strings.HasPrefix(string(rawBlobListBody), "<?xml"), string(rawBlobListBody))
	assert.Contains(t, string(rawBlobListBody), `ServiceEndpoint="`+storageAdvertisedEndpoint(t, account, "blob")+`"`)
	assert.Contains(t, string(rawBlobListBody), `ContainerName="`+container+`"`)
	assert.Contains(t, string(rawBlobListBody), "<NextMarker>")

	containerPager := client.NewListContainersPager(&azblob.ListContainersOptions{MaxResults: to.Ptr(int32(1))})
	containerPage, err := containerPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, containerPage.ContainerItems, 1)
	require.NotNil(t, containerPage.ContainerItems[0].Name)
	assert.Equal(t, container, *containerPage.ContainerItems[0].Name)

	rawContainerList := storageRawRequest(t, http.MethodGet, account, "blob", "/?comp=list&maxresults=1")
	require.Equal(t, http.StatusOK, rawContainerList.StatusCode)
	rawContainerListBody, _ := io.ReadAll(rawContainerList.Body)
	rawContainerList.Body.Close()
	assert.True(t, strings.HasPrefix(string(rawContainerListBody), "<?xml"), string(rawContainerListBody))
	assert.Contains(t, string(rawContainerListBody), `ServiceEndpoint="`+storageAdvertisedEndpoint(t, account, "blob")+`"`)
	assert.Contains(t, string(rawContainerListBody), "<NextMarker>")

	_, err = client.DeleteBlob(ctx, container, blobName, nil)
	require.NoError(t, err)
}

func TestStorageDataPlane_XMLErrors(t *testing.T) {
	account := "sdkstorageerrors"

	blobResp := storageRawRequest(t, http.MethodGet, account, "blob", "/missing-container/missing-blob.txt")
	assertStorageXMLError(t, blobResp, http.StatusNotFound, "BlobNotFound")

	fileResp := storageRawRequest(t, http.MethodGet, account, "file", "/missing-share?restype=share")
	assertStorageXMLError(t, fileResp, http.StatusNotFound, "ShareNotFound")

	queueResp := storageRawRequest(t, http.MethodGet, account, "queue", "/missingqueue/messages")
	assertStorageXMLError(t, queueResp, http.StatusNotFound, "QueueNotFound")
}

func assertStorageXMLError(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, status, resp.StatusCode, string(body))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/xml")
	assert.Equal(t, code, resp.Header.Get("x-ms-error-code"))
	assert.True(t, strings.HasPrefix(string(body), "<?xml"), string(body))
	assert.Contains(t, string(body), "<Error>")
	assert.Contains(t, string(body), "<Code>"+code+"</Code>")
}

func TestBlobDataPlane_Pagination(t *testing.T) {
	account := "sdkblobpaginate"
	container := "paginate-container"

	client, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = client.CreateContainer(ctx, container, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, container, nil) })

	// Upload 3 blobs with sorted names so pagination order is deterministic.
	blobs := []string{"aaa.txt", "bbb.txt", "ccc.txt"}
	for _, b := range blobs {
		_, err = client.UploadBuffer(ctx, container, b, []byte(b), nil)
		require.NoError(t, err)
	}

	// Page through with maxresults=1 via raw XML — verifies the wire shape directly.
	var gotBlobs []string
	marker := ""
	for {
		target := "/" + container + "?restype=container&comp=list&maxresults=1"
		if marker != "" {
			target += "&marker=" + marker
		}
		resp := storageRawRequest(t, http.MethodGet, account, "blob", target)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result struct {
			Blobs []struct {
				Name string `xml:"Name"`
			} `xml:"Blobs>Blob"`
			NextMarker string `xml:"NextMarker"`
		}
		require.NoError(t, xml.Unmarshal(body, &result), string(body))
		require.Len(t, result.Blobs, 1, "each page must have exactly 1 blob with maxresults=1")
		gotBlobs = append(gotBlobs, result.Blobs[0].Name)

		if result.NextMarker == "" {
			break
		}
		marker = result.NextMarker
	}
	require.Equal(t, blobs, gotBlobs, "all blobs must be returned across pages in sorted order")

	// Also verify via the SDK pager.
	pager := client.NewListBlobsFlatPager(container, &azblob.ListBlobsFlatOptions{MaxResults: to.Ptr(int32(2))})
	var sdkBlobs []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, b := range page.Segment.BlobItems {
			sdkBlobs = append(sdkBlobs, *b.Name)
		}
	}
	require.Equal(t, blobs, sdkBlobs)
}

func TestStorageSDK_BlobStartCopyFromURL(t *testing.T) {
	account := "sdkcopyblobacct"
	container := "sdk-copy-container"
	srcName := "nested/source blob.txt"
	dstName := "copied/dest blob.txt"
	payload := []byte("copied through Azure Blob Copy Blob")
	contentType := "text/plain"

	client, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = client.CreateContainer(ctx, container, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, container, nil) })

	_, err = client.UploadBuffer(ctx, container, srcName, payload, &azblob.UploadBufferOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &contentType},
		Metadata: map[string]*string{
			"origin": to.Ptr("source"),
		},
	})
	require.NoError(t, err)

	base := storageSDKURL(t, account, "blob")
	srcURL := base + container + "/" + url.PathEscape(srcName)
	dstClient, err := blob.NewClientWithNoCredential(base+container+"/"+url.PathEscape(dstName),
		&blob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	copyResp, err := dstClient.StartCopyFromURL(ctx, srcURL, &blob.StartCopyFromURLOptions{
		Metadata: map[string]*string{
			"origin": to.Ptr("dest"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, copyResp.CopyID)
	require.NotNil(t, copyResp.CopyStatus)
	assert.Equal(t, "success", string(*copyResp.CopyStatus))

	download, err := client.DownloadStream(ctx, container, dstName, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, payload, got)

	props, err := dstClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.CopyStatus)
	assert.Equal(t, "success", string(*props.CopyStatus))
	require.NotNil(t, props.Metadata["Origin"])
	assert.Equal(t, "dest", *props.Metadata["Origin"])
	require.NotNil(t, props.ContentType)
	assert.Equal(t, contentType, *props.ContentType)

	pathStyleSrc := strings.TrimRight(baseURL, "/") + "/" + account + "/" + container + "/" + url.PathEscape(srcName)
	inheritName := "copied/inherit metadata.txt"
	inheritClient, err := blob.NewClientWithNoCredential(base+container+"/"+url.PathEscape(inheritName),
		&blob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = inheritClient.StartCopyFromURL(ctx, pathStyleSrc, nil)
	require.NoError(t, err)

	inheritProps, err := inheritClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, inheritProps.Metadata["Origin"])
	assert.Equal(t, "source", *inheritProps.Metadata["Origin"])

	missingClient, err := blob.NewClientWithNoCredential(base+container+"/"+url.PathEscape("copied/missing.txt"),
		&blob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = missingClient.StartCopyFromURL(ctx, base+container+"/"+url.PathEscape("missing-source.txt"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CannotVerifyCopySource")
}

func TestStorageSDK_BlobBlockStaging(t *testing.T) {
	account := "sdkblockblobacct"
	container := "sdk-block-container"
	blobName := "staged.txt"
	payloadA := []byte("hello ")
	payloadB := []byte("from blocks")

	client, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = client.CreateContainer(ctx, container, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, container, nil) })

	blobClient, err := blockblob.NewClientWithNoCredential(
		storageSDKURL(t, account, "blob")+container+"/"+blobName,
		&blockblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	blockA := base64.StdEncoding.EncodeToString([]byte("block-000001"))
	blockB := base64.StdEncoding.EncodeToString([]byte("block-000002"))
	_, err = blobClient.StageBlock(ctx, blockA, streaming.NopCloser(bytes.NewReader(payloadA)), nil)
	require.NoError(t, err)
	_, err = blobClient.StageBlock(ctx, blockB, streaming.NopCloser(bytes.NewReader(payloadB)), nil)
	require.NoError(t, err)

	uncommitted, err := blobClient.GetBlockList(ctx, blockblob.BlockListTypeUncommitted, nil)
	require.NoError(t, err)
	require.Len(t, uncommitted.UncommittedBlocks, 2)
	require.NotNil(t, uncommitted.UncommittedBlocks[0].Name)
	require.NotNil(t, uncommitted.UncommittedBlocks[1].Name)
	assert.ElementsMatch(t,
		[]string{blockA, blockB},
		[]string{*uncommitted.UncommittedBlocks[0].Name, *uncommitted.UncommittedBlocks[1].Name})

	_, err = blobClient.CommitBlockList(ctx, []string{blockA, blockB}, nil)
	require.NoError(t, err)

	allBlocks, err := blobClient.GetBlockList(ctx, blockblob.BlockListTypeAll, nil)
	require.NoError(t, err)
	require.Len(t, allBlocks.CommittedBlocks, 2)
	require.Empty(t, allBlocks.UncommittedBlocks)
	require.NotNil(t, allBlocks.CommittedBlocks[0].Name)
	require.NotNil(t, allBlocks.CommittedBlocks[1].Name)
	assert.Equal(t, blockA, *allBlocks.CommittedBlocks[0].Name)
	assert.Equal(t, blockB, *allBlocks.CommittedBlocks[1].Name)

	download, err := client.DownloadStream(ctx, container, blobName, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, append(payloadA, payloadB...), got)
}

func TestStorageSDK_FileLifecycleAndPagedLists(t *testing.T) {
	account := "sdkfileacct"
	share := "sdk-file-share"
	fileName := "one.txt"
	payload := []byte("hello from azfile")

	serviceClient, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
		&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	fileClient, err := file.NewClientWithNoCredential(storageSDKURL(t, account, "file")+share+"/"+fileName,
		&file.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	buf := make([]byte, len(payload))
	n, err := fileClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n)
	assert.Equal(t, payload, buf)

	sharePager := serviceClient.NewListSharesPager(&fileservice.ListSharesOptions{MaxResults: to.Ptr(int32(1))})
	sharePage, err := sharePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, sharePage.Shares, 1)
	require.NotNil(t, sharePage.Shares[0].Name)
	assert.Equal(t, share, *sharePage.Shares[0].Name)

	rootPager := serviceClient.NewShareClient(share).NewRootDirectoryClient().NewListFilesAndDirectoriesPager(nil)
	rootPage, err := rootPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotNil(t, rootPage.Segment)
	require.Len(t, rootPage.Segment.Files, 1)
	require.NotNil(t, rootPage.Segment.Files[0].Name)
	assert.Equal(t, fileName, *rootPage.Segment.Files[0].Name)

	_, err = fileClient.Delete(ctx, nil)
	require.NoError(t, err)
}

func TestStorageSDK_FileShareStoredAccessPolicy(t *testing.T) {
	const (
		account = "sdkfileaclacct"
		name    = "sdk-file-acl-share"
	)
	serviceClient, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
		&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = serviceClient.CreateShare(ctx, name, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, name, nil) })

	start := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	expiry := start.Add(24 * time.Hour)
	permissions := fileshare.AccessPolicyPermission{
		Read: true, Write: true, Create: true, Delete: true, List: true,
	}.String()
	shareClient := serviceClient.NewShareClient(name)
	_, err = shareClient.SetAccessPolicy(ctx, &fileshare.SetAccessPolicyOptions{
		ShareACL: []*fileshare.SignedIdentifier{{
			ID: to.Ptr("sdk-policy"),
			AccessPolicy: &fileshare.AccessPolicy{
				Start:      &start,
				Expiry:     &expiry,
				Permission: &permissions,
			},
		}},
	})
	require.NoError(t, err)

	got, err := shareClient.GetAccessPolicy(ctx, nil)
	require.NoError(t, err)
	require.Len(t, got.SignedIdentifiers, 1)
	require.NotNil(t, got.SignedIdentifiers[0].ID)
	require.NotNil(t, got.SignedIdentifiers[0].AccessPolicy)
	require.NotNil(t, got.SignedIdentifiers[0].AccessPolicy.Permission)
	assert.Equal(t, "sdk-policy", *got.SignedIdentifiers[0].ID)
	assert.Equal(t, permissions, *got.SignedIdentifiers[0].AccessPolicy.Permission)
	assert.Equal(t, start, *got.SignedIdentifiers[0].AccessPolicy.Start)
	assert.Equal(t, expiry, *got.SignedIdentifiers[0].AccessPolicy.Expiry)
}

func TestStorageSDK_QueueLifecycleAndPagedLists(t *testing.T) {
	account := "sdkqueueacct"
	queueName := "sdkqueue"
	message := "hello from azqueue"

	serviceClient, err := azqueue.NewServiceClientWithNoCredential(storageSDKURL(t, account, "queue"),
		&azqueue.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

	queueClient := serviceClient.NewQueueClient(queueName)
	_, err = queueClient.EnqueueMessage(ctx, message, nil)
	require.NoError(t, err)

	propsResp := storageRawRequest(t, http.MethodGet, account, "queue", "/?restype=service&comp=properties")
	require.Equal(t, http.StatusOK, propsResp.StatusCode)
	propsBody, err := io.ReadAll(propsResp.Body)
	require.NoError(t, err)
	propsResp.Body.Close()
	assert.Contains(t, propsResp.Header.Get("Content-Type"), "application/xml")
	assert.True(t, strings.HasPrefix(string(propsBody), "<?xml"), string(propsBody))
	assert.Contains(t, string(propsBody), "<StorageServiceProperties>")
	assert.Contains(t, string(propsBody), "<HourMetrics>")

	dequeued, err := queueClient.DequeueMessage(ctx, nil)
	require.NoError(t, err)
	require.Len(t, dequeued.Messages, 1)
	require.NotNil(t, dequeued.Messages[0].MessageText)
	assert.Equal(t, message, *dequeued.Messages[0].MessageText)
	require.NotNil(t, dequeued.Messages[0].MessageID)
	require.NotNil(t, dequeued.Messages[0].PopReceipt)

	_, err = queueClient.DeleteMessage(ctx, *dequeued.Messages[0].MessageID, *dequeued.Messages[0].PopReceipt, nil)
	require.NoError(t, err)

	pager := serviceClient.NewListQueuesPager(&azqueue.ListQueuesOptions{MaxResults: to.Ptr(int32(1))})
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Queues, 1)
	require.NotNil(t, page.Queues[0].Name)
	assert.Equal(t, queueName, *page.Queues[0].Name)
}

func TestStorageSDK_TableLifecycleAndPagedLists(t *testing.T) {
	account := "sdktableacct"
	tableName := "SdkTable"

	serviceClient, err := aztables.NewServiceClientWithNoCredential(storageSDKURL(t, account, "table"),
		&aztables.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateTable(ctx, tableName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteTable(ctx, tableName, nil) })

	tableClient := serviceClient.NewClient(tableName)
	entity := map[string]any{
		"PartitionKey": "p1",
		"RowKey":       "r1",
		"Value":        "hello from aztables",
	}
	body, err := json.Marshal(entity)
	require.NoError(t, err)
	_, err = tableClient.AddEntity(ctx, body, nil)
	require.NoError(t, err)

	got, err := tableClient.GetEntity(ctx, "p1", "r1", nil)
	require.NoError(t, err)
	assert.Contains(t, string(got.Value), "hello from aztables")

	entityPager := tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{Top: to.Ptr(int32(1))})
	entityPage, err := entityPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, entityPage.Entities, 1)
	assert.Contains(t, string(entityPage.Entities[0]), "hello from aztables")

	tablePager := serviceClient.NewListTablesPager(&aztables.ListTablesOptions{Top: to.Ptr(int32(1))})
	tablePage, err := tablePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, tablePage.Tables, 1)
	require.NotNil(t, tablePage.Tables[0].Name)
	assert.Equal(t, tableName, *tablePage.Tables[0].Name)

	_, err = tableClient.DeleteEntity(ctx, "p1", "r1", nil)
	require.NoError(t, err)
}

// TestStorageSDK_TableQueryFilterAndPaging verifies the Tables Query Entities
// endpoint honors server-side $filter and pages results via $top + the
// x-ms-continuation-* tokens, the way aztables drives the data plane.
func TestStorageSDK_TableQueryFilterAndPaging(t *testing.T) {
	account := "sdktablequeryacct"
	tableName := "QueryTable"

	serviceClient, err := aztables.NewServiceClientWithNoCredential(storageSDKURL(t, account, "table"),
		&aztables.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateTable(ctx, tableName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteTable(ctx, tableName, nil) })

	tableClient := serviceClient.NewClient(tableName)

	// Five entities across two partitions.
	type row struct{ pk, rk, color string }
	rows := []row{
		{"pA", "r1", "red"},
		{"pA", "r2", "blue"},
		{"pA", "r3", "red"},
		{"pB", "r1", "green"},
		{"pB", "r2", "red"},
	}
	for _, rw := range rows {
		body, err := json.Marshal(map[string]any{
			"PartitionKey": rw.pk,
			"RowKey":       rw.rk,
			"Color":        rw.color,
		})
		require.NoError(t, err)
		_, err = tableClient.AddEntity(ctx, body, nil)
		require.NoError(t, err)
	}

	// $filter on a system property (PartitionKey) — only pA's 3 rows.
	pager := tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: to.Ptr("PartitionKey eq 'pA'"),
	})
	var pkFiltered int
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pkFiltered += len(page.Entities)
	}
	require.Equal(t, 3, pkFiltered, "PartitionKey eq 'pA' should match exactly 3 entities")

	// $filter on a user property (Color) — the 3 red rows across partitions.
	pager = tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: to.Ptr("Color eq 'red'"),
	})
	var redFiltered int
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		redFiltered += len(page.Entities)
	}
	require.Equal(t, 3, redFiltered, "Color eq 'red' should match exactly 3 entities")

	// $top=2 must page: first page has 2, and continuation returns the rest.
	pager = tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{Top: to.Ptr(int32(2))})
	first, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, first.Entities, 2, "first $top=2 page must hold exactly 2 entities")
	require.True(t, pager.More(), "more pages must remain after the first $top=2 page")

	var total int
	pager = tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{Top: to.Ptr(int32(2))})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		total += len(page.Entities)
	}
	require.Equal(t, len(rows), total, "paging over $top=2 must visit every entity exactly once")
}

// storageMetadataValue reads one x-ms-meta-* value case-insensitively. Azure
// Storage metadata names are case-insensitive on read, and Go canonicalizes
// header names on both the request and the response, so the case a client sent
// never survives the round trip through the simulator.
func storageMetadataValue(metadata map[string]*string, name string) string {
	for k, v := range metadata {
		if strings.EqualFold(k, name) && v != nil {
			return *v
		}
	}
	return ""
}

// TestStorageDataPlane_UnservedOperationsDeclareGaps drives the operations the
// simulator's Files / Queues dispatchers do NOT implement through the real
// Azure SDK clients. Each must fail with the declared gap, and — the part that
// matters — must leave the resource exactly as it was: a dispatcher that
// selected a handler by `comp` and fell through for an unrecognized value would
// run Create File where Create Directory was asked for, and Create Queue where
// Set Queue ACL was. The Blob plane serves its whole documented surface, so it
// has no arm here; its operations are covered in blob_dataplane_test.go.
func TestStorageDataPlane_UnservedOperationsDeclareGaps(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		account, share, fileName := "sdkgapfile", "gap-share", "kept.txt"
		payload := []byte("kept files payload")

		serviceClient, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
			&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
		require.NoError(t, err)
		_, err = serviceClient.CreateShare(ctx, share, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

		fileClient, err := file.NewClientWithNoCredential(storageSDKURL(t, account, "file")+share+"/"+fileName,
			&file.ClientOptions{ClientOptions: storageSDKOptions()})
		require.NoError(t, err)
		_, err = fileClient.Create(ctx, int64(len(payload)), nil)
		require.NoError(t, err)
		require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

		// Create Directory used to land on Create File and answer 201 for a
		// directory that was never created; it now creates a real directory,
		// and the file beside it is untouched.
		dirClient, err := directory.NewClientWithNoCredential(storageSDKURL(t, account, "file")+share+"/subdir",
			&directory.ClientOptions{ClientOptions: storageSDKOptions()})
		require.NoError(t, err)
		_, err = dirClient.Create(ctx, nil)
		require.NoError(t, err)
		_, err = fileClient.SetMetadata(ctx, nil)
		require.NoError(t, err)
		_, err = fileClient.SetHTTPHeaders(ctx, nil)
		require.NoError(t, err)

		// A `comp` the Files data plane does not define must still be refused
		// rather than fall through to Create File.
		resp := storageRawRequest(t, http.MethodPut, account, "file", "/"+share+"/"+fileName+"?comp=tier")
		require.Equal(t, http.StatusNotImplemented, resp.StatusCode)
		require.NoError(t, resp.Body.Close())

		buf := make([]byte, len(payload))
		n, err := fileClient.DownloadBuffer(ctx, buf, nil)
		require.NoError(t, err)
		assert.Equal(t, int64(len(payload)), n)
		assert.Equal(t, payload, buf, "the file must survive every refused operation untouched")

		_, err = fileClient.Delete(ctx, nil)
		require.NoError(t, err)
		_, err = dirClient.Delete(ctx, nil)
		require.NoError(t, err)
	})

	t.Run("queue", func(t *testing.T) {
		account, queueName := "sdkgapqueue", "gapqueue"

		serviceClient, err := azqueue.NewServiceClientWithNoCredential(storageSDKURL(t, account, "queue"),
			&azqueue.ClientOptions{ClientOptions: storageSDKOptions()})
		require.NoError(t, err)
		_, err = serviceClient.CreateQueue(ctx, queueName, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

		queueClient := serviceClient.NewQueueClient(queueName)

		// Set Queue Metadata IS served, and Get Queue Properties reads back
		// exactly what it set.
		_, err = queueClient.SetMetadata(ctx, &azqueue.SetMetadataOptions{
			Metadata: map[string]*string{"owner": to.Ptr("sockerless")},
		})
		require.NoError(t, err)
		props, err := queueClient.GetProperties(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, "sockerless", storageMetadataValue(props.Metadata, "owner"))

		// Set Queue ACL used to land on Create Queue: for a name that did not
		// exist it created the queue and answered 201. It now stores the
		// policies, and Get Queue ACL reads back exactly what was set.
		_, err = queueClient.SetAccessPolicy(ctx, &azqueue.SetAccessPolicyOptions{
			QueueACL: []*azqueue.SignedIdentifier{{
				ID: to.Ptr("gap-policy"),
				AccessPolicy: &azqueue.AccessPolicy{
					Permission: to.Ptr("rap"),
				},
			}},
		})
		require.NoError(t, err)
		acl, err := queueClient.GetAccessPolicy(ctx, nil)
		require.NoError(t, err)
		require.Len(t, acl.SignedIdentifiers, 1)
		require.NotNil(t, acl.SignedIdentifiers[0].ID)
		assert.Equal(t, "gap-policy", *acl.SignedIdentifiers[0].ID)

		// The access-policy calls must not have disturbed the queue.
		props, err = queueClient.GetProperties(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, "sockerless", storageMetadataValue(props.Metadata, "owner"))

		// An operation on a queue that does not exist must not create it.
		absent := serviceClient.NewQueueClient("neveraqueue")
		_, err = absent.SetAccessPolicy(ctx, nil)
		require.Error(t, err, "Set Queue ACL on a queue that does not exist must fail")
		_, err = absent.GetProperties(ctx, nil)
		require.Error(t, err, "the failed Set Queue ACL must not have created the queue")
	})
}
