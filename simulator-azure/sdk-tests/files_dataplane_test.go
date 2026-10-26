package azure_sdk_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/directory"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/lease"
	fileservice "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/service"
	fileshare "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/share"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// filesSDKService builds an azfile service client for one account at the
// simulator's endpoint.
func filesSDKService(t *testing.T, account string) *fileservice.Client {
	t.Helper()
	client, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
		&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	return client
}

// TestFilesSDK_NestedDirectoryTreeEndToEnd is the path an Azure Container Apps
// or Azure Functions volume depends on: create a tree, write at depth, list,
// rename and remove, with the share's backing directory, which a mounting
// workload sees, agreeing at each step.
func TestFilesSDK_NestedDirectoryTreeEndToEnd(t *testing.T) {
	t.Parallel()
	const (
		account = "sdkfilenested"
		share   = "sdk-nested-share"
	)
	payload := []byte("bytes written at depth")

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	shareClient := serviceClient.NewShareClient(share)
	buildDir := shareClient.NewDirectoryClient("build")
	_, err = buildDir.Create(ctx, &directory.CreateOptions{
		Metadata: map[string]*string{"stage": to.Ptr("compile")},
	})
	require.NoError(t, err)

	// A directory is created only inside one that exists.
	orphan := shareClient.NewDirectoryClient("missing").NewSubdirectoryClient("child")
	_, err = orphan.Create(ctx, nil)
	require.Error(t, err, "Create Directory must fail when its parent does not exist")

	artifactsDir := buildDir.NewSubdirectoryClient("artifacts")
	_, err = artifactsDir.Create(ctx, nil)
	require.NoError(t, err)

	// The directory exists in the backing tree a mounting workload walks.
	onDisk := filepath.Join(azureFilesDataDir, account, share, "build", "artifacts")
	info, err := os.Stat(onDisk)
	require.NoError(t, err, "Create Directory must materialize %s", onDisk)
	require.True(t, info.IsDir())

	props, err := buildDir.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "compile", storageMetadataValue(props.Metadata, "stage"),
		"Get Directory Properties must report the metadata Create Directory set")

	_, err = buildDir.SetMetadata(ctx, &directory.SetMetadataOptions{
		Metadata: map[string]*string{"stage": to.Ptr("package")},
	})
	require.NoError(t, err)
	props, err = buildDir.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "package", storageMetadataValue(props.Metadata, "stage"))

	_, err = buildDir.SetProperties(ctx, nil)
	require.NoError(t, err)

	fileClient := artifactsDir.NewFileClient("bundle.tar")
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	nested := filepath.Join(onDisk, "bundle.tar")
	written, err := os.ReadFile(nested)
	require.NoError(t, err, "a file created at depth must land at %s", nested)
	assert.Equal(t, payload, written)

	pager := artifactsDir.NewListFilesAndDirectoriesPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotNil(t, page.Segment)
	require.Len(t, page.Segment.Files, 1)
	require.NotNil(t, page.Segment.Files[0].Name)
	assert.Equal(t, "bundle.tar", *page.Segment.Files[0].Name)
	require.NotNil(t, page.DirectoryPath)
	assert.Equal(t, "build/artifacts", *page.DirectoryPath)

	parentPager := buildDir.NewListFilesAndDirectoriesPager(nil)
	parentPage, err := parentPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotNil(t, parentPage.Segment)
	require.Len(t, parentPage.Segment.Directories, 1)
	require.NotNil(t, parentPage.Segment.Directories[0].Name)
	assert.Equal(t, "artifacts", *parentPage.Segment.Directories[0].Name)
	assert.Empty(t, parentPage.Segment.Files)

	renamed, err := fileClient.Rename(ctx, "build/artifacts/bundle-v2.tar", nil)
	require.NoError(t, err)
	require.NotNil(t, renamed.ETag)
	_, err = fileClient.GetProperties(ctx, nil)
	require.Error(t, err, "the renamed source must be gone")

	movedClient := artifactsDir.NewFileClient("bundle-v2.tar")
	buf := make([]byte, len(payload))
	n, err := movedClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n)
	assert.Equal(t, payload, buf, "rename must move the bytes, not copy or lose them")

	// Azure Files refuses to delete a directory holding anything.
	_, err = artifactsDir.Delete(ctx, nil)
	require.Error(t, err, "Delete Directory must refuse a directory that is not empty")

	_, err = movedClient.Delete(ctx, nil)
	require.NoError(t, err)
	_, err = artifactsDir.Delete(ctx, nil)
	require.NoError(t, err)

	_, err = buildDir.Rename(ctx, "release", nil)
	require.NoError(t, err)
	releaseDir := shareClient.NewDirectoryClient("release")
	releaseProps, err := releaseDir.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "package", storageMetadataValue(releaseProps.Metadata, "stage"),
		"a renamed directory keeps its metadata")
	_, err = releaseDir.Delete(ctx, nil)
	require.NoError(t, err)
}

// TestFilesSDK_DirectoryListingPagesWithPrefixAndMarker covers listing a
// directory that holds more than one page.
func TestFilesSDK_DirectoryListingPagesWithPrefixAndMarker(t *testing.T) {
	t.Parallel()
	const (
		account = "sdkfilepaging"
		share   = "sdk-paging-share"
	)
	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	root := serviceClient.NewShareClient(share).NewRootDirectoryClient()
	for _, name := range []string{"keep-a.txt", "keep-b.txt", "keep-c.txt", "other.txt"} {
		f := root.NewFileClient(name)
		_, err := f.Create(ctx, 1, nil)
		require.NoError(t, err)
	}

	// Prefix narrows, MaxResults bounds each page, and the pager follows
	// NextMarker to the end.
	pager := root.NewListFilesAndDirectoriesPager(&directory.ListFilesAndDirectoriesOptions{
		Prefix:     to.Ptr("keep-"),
		MaxResults: to.Ptr(int32(2)),
	})
	var names []string
	pages := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pages++
		require.NotNil(t, page.Segment)
		for _, f := range page.Segment.Files {
			require.NotNil(t, f.Name)
			names = append(names, *f.Name)
		}
	}
	assert.Equal(t, []string{"keep-a.txt", "keep-b.txt", "keep-c.txt"}, names,
		"the prefix must exclude other.txt and the marker must not repeat or skip an entry")
	assert.Greater(t, pages, 1, "MaxResults=2 over three entries must produce more than one page")
}

// TestFilesSDK_ShareLeaseGuardsWrites: a write without the share lease's ID is
// refused with the service's precondition failure.
func TestFilesSDK_ShareLeaseGuardsWrites(t *testing.T) {
	t.Parallel()
	const (
		account = "sdkfilesharelease"
		share   = "sdk-share-lease"
	)
	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	shareClient := serviceClient.NewShareClient(share)

	leaseClient, err := lease.NewShareClient(shareClient, &lease.ShareClientOptions{
		LeaseID: to.Ptr("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
	})
	require.NoError(t, err)
	acquired, err := leaseClient.Acquire(ctx, -1, nil)
	require.NoError(t, err)
	require.NotNil(t, acquired.LeaseID)
	assert.Equal(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", *acquired.LeaseID)

	props, err := shareClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.LeaseState)
	assert.Equal(t, "leased", string(*props.LeaseState))

	_, err = shareClient.SetMetadata(ctx, &fileshare.SetMetadataOptions{
		Metadata: map[string]*string{"owner": to.Ptr("intruder")},
	})
	require.Error(t, err, "Set Share Metadata must be refused while the share is leased")
	_, err = shareClient.Delete(ctx, nil)
	require.Error(t, err, "Delete Share must be refused while the share is leased")

	_, err = shareClient.SetMetadata(ctx, &fileshare.SetMetadataOptions{
		Metadata:              map[string]*string{"owner": to.Ptr("sockerless")},
		LeaseAccessConditions: &fileshare.LeaseAccessConditions{LeaseID: acquired.LeaseID},
	})
	require.NoError(t, err)
	props, err = shareClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "sockerless", storageMetadataValue(props.Metadata, "owner"))

	// Renew, change and release all move the lease the service holds.
	_, err = leaseClient.Renew(ctx, nil)
	require.NoError(t, err)
	changed, err := leaseClient.Change(ctx, "11111111-2222-3333-4444-555555555555", nil)
	require.NoError(t, err)
	require.NotNil(t, changed.LeaseID)
	assert.Equal(t, "11111111-2222-3333-4444-555555555555", *changed.LeaseID)
	_, err = leaseClient.Release(ctx, nil)
	require.NoError(t, err)

	_, err = shareClient.Delete(ctx, nil)
	require.NoError(t, err)
}

// TestFilesSDK_FileLeaseGuardsWrites: an unleased write to a leased file is
// refused and leaves the bytes alone.
func TestFilesSDK_FileLeaseGuardsWrites(t *testing.T) {
	t.Parallel()
	const (
		account  = "sdkfilefilelease"
		share    = "sdk-file-lease"
		fileName = "leased.txt"
	)
	payload := []byte("payload the lease holder wrote")

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	fileClient := serviceClient.NewShareClient(share).NewRootDirectoryClient().NewFileClient(fileName)
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	leaseClient, err := lease.NewFileClient(fileClient, nil)
	require.NoError(t, err)
	acquired, err := leaseClient.Acquire(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, acquired.LeaseID)

	replacement := []byte("bytes that an intruder wrote!!")
	require.Len(t, replacement, len(payload), "the replacement must fill the same range")
	err = fileClient.UploadBuffer(ctx, replacement, nil)
	require.Error(t, err, "Upload Range must be refused while the file is leased")
	_, err = fileClient.Delete(ctx, nil)
	require.Error(t, err, "Delete File must be refused while the file is leased")

	buf := make([]byte, len(payload))
	_, err = fileClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, payload, buf, "a refused write must leave the file untouched")

	_, err = fileClient.UploadRange(ctx, 0, streaming.NopCloser(bytes.NewReader(replacement)),
		&file.UploadRangeOptions{LeaseAccessConditions: &file.LeaseAccessConditions{LeaseID: acquired.LeaseID}})
	require.NoError(t, err)

	_, err = leaseClient.Release(ctx, nil)
	require.NoError(t, err)
	_, err = fileClient.Delete(ctx, nil)
	require.NoError(t, err)
}

func TestFilesSDK_FilePropertiesRangesAndHandles(t *testing.T) {
	t.Parallel()
	const (
		account  = "sdkfileranges"
		share    = "sdk-range-share"
		fileName = "ranged.bin"
	)
	payload := []byte("0123456789abcdef")

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	root := serviceClient.NewShareClient(share).NewRootDirectoryClient()
	fileClient := root.NewFileClient(fileName)
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)

	// An allocated file holds no data, so it lists no ranges; after a write the
	// written range is reported.
	empty, err := fileClient.GetRangeList(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty.Ranges, "a freshly allocated file has no valid ranges")

	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))
	listed, err := fileClient.GetRangeList(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, listed.Ranges, "List Ranges must report the range the write filled")
	require.NotNil(t, listed.Ranges[0].Start)
	require.NotNil(t, listed.Ranges[0].End)
	assert.Equal(t, int64(0), *listed.Ranges[0].Start)
	assert.GreaterOrEqual(t, *listed.Ranges[0].End, int64(len(payload)-1))
	require.NotNil(t, listed.FileContentLength)
	assert.Equal(t, int64(len(payload)), *listed.FileContentLength)

	_, err = fileClient.SetHTTPHeaders(ctx, &file.SetHTTPHeadersOptions{
		HTTPHeaders: &file.HTTPHeaders{ContentType: to.Ptr("application/octet-stream")},
	})
	require.NoError(t, err)
	props, err := fileClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ContentType)
	assert.Equal(t, "application/octet-stream", *props.ContentType)

	_, err = fileClient.SetMetadata(ctx, &file.SetMetadataOptions{
		Metadata: map[string]*string{"origin": to.Ptr("sdk")},
	})
	require.NoError(t, err)
	props, err = fileClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "sdk", storageMetadataValue(props.Metadata, "origin"))

	_, err = fileClient.Resize(ctx, 8, nil)
	require.NoError(t, err)
	props, err = fileClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ContentLength)
	assert.Equal(t, int64(8), *props.ContentLength)

	_, err = fileClient.ClearRange(ctx, file.HTTPRange{Offset: 0, Count: 4}, nil)
	require.NoError(t, err)
	buf := make([]byte, 8)
	_, err = fileClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte{0, 0, 0, 0}, buf[:4], "Clear Range must zero the range it names")
	assert.Equal(t, payload[4:8], buf[4:])

	// Nothing opens SMB or NFS sessions against the share, so no handles exist.
	handles, err := fileClient.ListHandles(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, handles.Handles)
	closed, err := fileClient.ForceCloseHandles(ctx, "*", nil)
	require.NoError(t, err)
	require.NotNil(t, closed.NumberOfHandlesClosed)
	assert.Equal(t, int32(0), *closed.NumberOfHandlesClosed)

	dirHandles, err := root.ListHandles(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, dirHandles.Handles)
	dirClosed, err := root.ForceCloseHandles(ctx, "*", nil)
	require.NoError(t, err)
	require.NotNil(t, dirClosed.NumberOfHandlesClosed)
	assert.Equal(t, int32(0), *dirClosed.NumberOfHandlesClosed)
}

// TestFilesSDK_ClearRangeDeallocatesTheRange: a cleared range stops existing,
// so List Ranges no longer reports it, rather than reading as zeros.
func TestFilesSDK_ClearRangeDeallocatesTheRange(t *testing.T) {
	t.Parallel()
	const (
		account  = "sdkfileclear"
		share    = "sdk-clear-share"
		fileName = "cleared.bin"
		// Clear whole filesystem allocation units on any filesystem the simulator runs
		// on.
		size      = 4 << 20
		clearFrom = 1 << 20
		clearTo   = 3 << 20
	)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i%251) + 1 // never zero, so a cleared byte shows
	}

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	fileClient := serviceClient.NewShareClient(share).NewRootDirectoryClient().NewFileClient(fileName)
	_, err = fileClient.Create(ctx, size, nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	full, err := fileClient.GetRangeList(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, full.Ranges, "the written file must report the range it holds")

	_, err = fileClient.ClearRange(ctx, file.HTTPRange{Offset: clearFrom, Count: clearTo - clearFrom}, nil)
	require.NoError(t, err)

	cleared, err := fileClient.GetRangeList(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, cleared.Ranges, "the ranges outside the cleared window must survive")
	for _, rg := range cleared.Ranges {
		require.NotNil(t, rg.Start)
		require.NotNil(t, rg.End)
		assert.False(t, *rg.Start < clearTo && *rg.End >= clearFrom,
			"range %d-%d overlaps the cleared window %d-%d: Clear Range must deallocate, not zero-fill",
			*rg.Start, *rg.End, clearFrom, clearTo-1)
	}

	// The file keeps its size, the cleared bytes read as zeros, and the bytes on
	// either side are untouched.
	props, err := fileClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ContentLength)
	assert.Equal(t, int64(size), *props.ContentLength)

	buf := make([]byte, size)
	n, err := fileClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(size), n)
	assert.Equal(t, payload[:clearFrom], buf[:clearFrom], "bytes before the cleared window must survive")
	assert.Equal(t, payload[clearTo:], buf[clearTo:], "bytes after the cleared window must survive")
	assert.Equal(t, make([]byte, clearTo-clearFrom), buf[clearFrom:clearTo],
		"the cleared window must read as zeros")

	// Clearing the whole file leaves no ranges at all.
	_, err = fileClient.ClearRange(ctx, file.HTTPRange{Offset: 0, Count: size}, nil)
	require.NoError(t, err)
	empty, err := fileClient.GetRangeList(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty.Ranges, "a fully cleared file must report no ranges")
}

// TestFilesSDK_CopyAndUploadRangeFromURL copies from a source in the same
// share.
func TestFilesSDK_CopyAndUploadRangeFromURL(t *testing.T) {
	t.Parallel()
	const (
		account = "sdkfilecopy"
		share   = "sdk-copy-share"
	)
	payload := []byte("source bytes for a copy")

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	root := serviceClient.NewShareClient(share).NewRootDirectoryClient()
	source := root.NewFileClient("source.txt")
	_, err = source.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, source.UploadBuffer(ctx, payload, nil))

	destination := root.NewFileClient("copy.txt")
	copied, err := destination.StartCopyFromURL(ctx, source.URL(), nil)
	require.NoError(t, err)
	require.NotNil(t, copied.CopyStatus)
	assert.Equal(t, "success", string(*copied.CopyStatus))

	buf := make([]byte, len(payload))
	n, err := destination.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n)
	assert.Equal(t, payload, buf, "Copy File must copy the source bytes")

	// The copy finished inside the request that started it, so there is no
	// pending copy to abort, and the service says so.
	require.NotNil(t, copied.CopyID)
	_, err = destination.AbortCopy(ctx, *copied.CopyID, nil)
	require.Error(t, err, "Abort Copy must report that no copy is pending")

	fromURL := root.NewFileClient("from-url.txt")
	_, err = fromURL.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	_, err = fromURL.UploadRangeFromURL(ctx, source.URL(), 0, 0, int64(len(payload)), nil)
	require.NoError(t, err)
	buf = make([]byte, len(payload))
	_, err = fromURL.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, payload, buf, "Upload Range from URL must write the source range")
}

// TestFilesSDK_HardAndSymbolicLinks: the links are real links in the share's
// directory tree.
func TestFilesSDK_HardAndSymbolicLinks(t *testing.T) {
	t.Parallel()
	const (
		account = "sdkfilelinks"
		share   = "sdk-link-share"
	)
	payload := []byte("linked bytes")

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	root := serviceClient.NewShareClient(share).NewRootDirectoryClient()
	target := root.NewFileClient("target.txt")
	_, err = target.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, target.UploadBuffer(ctx, payload, nil))

	hard := root.NewFileClient("hard.txt")
	_, err = hard.CreateHardLink(ctx, "target.txt", nil)
	require.NoError(t, err)
	buf := make([]byte, len(payload))
	_, err = hard.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, payload, buf, "a hard link must read the target's bytes")

	// A hard link is a second name for one inode, so the backing filesystem counts
	// two links.
	targetOnDisk := filepath.Join(azureFilesDataDir, account, share, "target.txt")
	hardOnDisk := filepath.Join(azureFilesDataDir, account, share, "hard.txt")
	targetInfo, err := os.Stat(targetOnDisk)
	require.NoError(t, err)
	hardInfo, err := os.Stat(hardOnDisk)
	require.NoError(t, err)
	assert.True(t, os.SameFile(targetInfo, hardInfo), "the hard link and its target must be one file")

	symbolic := root.NewFileClient("link.txt")
	_, err = symbolic.CreateSymbolicLink(ctx, "target.txt", nil)
	require.NoError(t, err)
	link, err := symbolic.GetSymbolicLink(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, link.LinkText)
	assert.Equal(t, "target.txt", *link.LinkText)

	linkOnDisk := filepath.Join(azureFilesDataDir, account, share, "link.txt")
	linkInfo, err := os.Lstat(linkOnDisk)
	require.NoError(t, err)
	assert.NotZero(t, linkInfo.Mode()&os.ModeSymlink, "Create Symbolic Link must make a real symbolic link")

	// A link target outside the share is refused.
	escaping := root.NewFileClient("escape.txt")
	_, err = escaping.CreateSymbolicLink(ctx, "../../../etc/passwd", nil)
	require.Error(t, err, "a symbolic link must not be a way out of the share")
}

// TestFilesSDK_SharePermissionsStatisticsAndProperties drives the share-level
// administration operations: security descriptors, usage statistics, quota and
// metadata.
func TestFilesSDK_SharePermissionsStatisticsAndProperties(t *testing.T) {
	t.Parallel()
	const (
		account = "sdkfileshareops"
		share   = "sdk-shareops-share"
	)
	payload := []byte("bytes that count toward the share usage")

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })
	shareClient := serviceClient.NewShareClient(share)

	const sddl = "O:S-1-5-21-1G:S-1-5-21-1D:(A;;FA;;;SY)(A;;FA;;;BA)"
	created, err := shareClient.CreatePermission(ctx, sddl, nil)
	require.NoError(t, err)
	require.NotNil(t, created.FilePermissionKey)
	permissionKey := *created.FilePermissionKey

	// The same descriptor maps to the same key, which lets a client cache it.
	again, err := shareClient.CreatePermission(ctx, sddl, nil)
	require.NoError(t, err)
	require.NotNil(t, again.FilePermissionKey)
	assert.Equal(t, permissionKey, *again.FilePermissionKey)

	fetched, err := shareClient.GetPermission(ctx, permissionKey, nil)
	require.NoError(t, err)
	require.NotNil(t, fetched.Permission)
	assert.Equal(t, sddl, *fetched.Permission)

	_, err = shareClient.SetProperties(ctx, &fileshare.SetPropertiesOptions{Quota: to.Ptr(int32(64))})
	require.NoError(t, err)
	props, err := shareClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.Quota)
	assert.Equal(t, int32(64), *props.Quota, "Set Share Properties must change the quota")

	// Share statistics measure what is stored.
	stats, err := shareClient.GetStatistics(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, stats.ShareUsageBytes)
	assert.Equal(t, int64(0), *stats.ShareUsageBytes, "an empty share uses no bytes")

	fileClient := shareClient.NewRootDirectoryClient().NewFileClient("usage.txt")
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))
	stats, err = shareClient.GetStatistics(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, stats.ShareUsageBytes)
	assert.Equal(t, int64(len(payload)), *stats.ShareUsageBytes,
		"Get Share Statistics must report the bytes the share actually holds")
}

// TestFilesSDK_ShareSnapshotFreezesContents: a snapshot keeps answering with
// the bytes the share held when it was taken.
func TestFilesSDK_ShareSnapshotFreezesContents(t *testing.T) {
	t.Parallel()
	const (
		account  = "sdkfilesnapshot"
		share    = "sdk-snapshot-share"
		fileName = "versioned.txt"
	)
	original := []byte("first generation")
	replacement := []byte("second generation")
	require.Len(t, replacement, len(original)+1)

	serviceClient := filesSDKService(t, account)
	_, err := serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })
	shareClient := serviceClient.NewShareClient(share)

	fileClient := shareClient.NewRootDirectoryClient().NewFileClient(fileName)
	_, err = fileClient.Create(ctx, int64(len(original)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, original, nil))

	snapshot, err := shareClient.CreateSnapshot(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Snapshot)

	_, err = fileClient.Create(ctx, int64(len(replacement)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, replacement, nil))

	live := make([]byte, len(replacement))
	_, err = fileClient.DownloadBuffer(ctx, live, nil)
	require.NoError(t, err)
	assert.Equal(t, replacement, live)

	// The snapshot keeps the earlier bytes.
	snapshotShare, err := shareClient.WithSnapshot(*snapshot.Snapshot)
	require.NoError(t, err)
	snapshotFile := snapshotShare.NewRootDirectoryClient().NewFileClient(fileName)
	frozen := make([]byte, len(original))
	_, err = snapshotFile.DownloadBuffer(ctx, frozen, nil)
	require.NoError(t, err)
	assert.Equal(t, original, frozen, "a snapshot must keep the bytes it froze")

	pager := serviceClient.NewListSharesPager(&fileservice.ListSharesOptions{
		Include: fileservice.ListSharesInclude{Snapshots: true},
	})
	var snapshots []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Shares {
			if s.Snapshot != nil {
				snapshots = append(snapshots, *s.Snapshot)
			}
		}
	}
	assert.Contains(t, snapshots, *snapshot.Snapshot)

	_, err = snapshotShare.Delete(ctx, nil)
	require.NoError(t, err)
	_, err = fileClient.GetProperties(ctx, nil)
	require.NoError(t, err, "deleting a snapshot must not touch the share")
}

// TestFilesSDK_ServicePropertiesAndUserDelegationKey drives the service-level
// operations beyond listing shares.
func TestFilesSDK_ServicePropertiesAndUserDelegationKey(t *testing.T) {
	t.Parallel()
	const account = "sdkfileservice"
	serviceClient := filesSDKService(t, account)

	_, err := serviceClient.SetProperties(ctx, &fileservice.SetPropertiesOptions{
		HourMetrics: &fileservice.Metrics{
			Version: to.Ptr("1.0"),
			Enabled: to.Ptr(true),
			RetentionPolicy: &fileservice.RetentionPolicy{
				Enabled: to.Ptr(true),
				Days:    to.Ptr(int32(9)),
			},
		},
	})
	require.NoError(t, err)

	props, err := serviceClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.HourMetrics)
	require.NotNil(t, props.HourMetrics.Enabled)
	assert.True(t, *props.HourMetrics.Enabled, "Get Service Properties must read back what Set wrote")
	require.NotNil(t, props.HourMetrics.RetentionPolicy)
	require.NotNil(t, props.HourMetrics.RetentionPolicy.Days)
	assert.Equal(t, int32(9), *props.HourMetrics.RetentionPolicy.Days)

	// Get User Delegation Key is OAuth-only on Azure, so the probe carries a token
	// for the storage resource instead of Shared Key; the key is bound to the
	// window asked for.
	start := time.Now().UTC().Truncate(time.Second)
	expiry := start.Add(time.Hour)
	storageBearer, _, err := fetchSimAccessToken("https://storage.azure.com/.default")
	require.NoError(t, err)
	resp := storageRawBodyAuthorized(t, http.MethodPost, account, "file", "/?restype=service&comp=userdelegationkey",
		"<KeyInfo><Start>"+start.Format(time.RFC3339)+"</Start><Expiry>"+expiry.Format(time.RFC3339)+"</Expiry></KeyInfo>",
		"Bearer "+storageBearer)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Contains(t, string(body), "<UserDelegationKey>")
	assert.Contains(t, string(body), "<SignedExpiry>"+expiry.Format(time.RFC3339)+"</SignedExpiry>")
	assert.Contains(t, string(body), "<SignedService>f</SignedService>")
}

// TestFilesSDK_ShareSoftDeleteAndRestore: with a share delete-retention policy
// on the File service, Delete Share keeps the share and Restore Share brings it
// back with its contents.
func TestFilesSDK_ShareSoftDeleteAndRestore(t *testing.T) {
	t.Parallel()
	const (
		account  = "sdkfilesoftdel"
		share    = "sdk-softdelete-share"
		fileName = "restored.txt"
	)
	payload := []byte("bytes that must survive a soft delete")

	// Enable share soft delete on the account's File service, as on Azure.
	const rgName = "sdk-files-softdelete-rg"
	createStorageAccountForARM(t, rgName, account)
	fsClient, err := armstorage.NewFileServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = fsClient.SetServiceProperties(ctx, rgName, account, armstorage.FileServiceProperties{
		FileServiceProperties: &armstorage.FileServicePropertiesProperties{
			ShareDeleteRetentionPolicy: &armstorage.DeleteRetentionPolicy{
				Enabled: to.Ptr(true),
				Days:    to.Ptr(int32(7)),
			},
		},
	}, nil)
	require.NoError(t, err)

	serviceClient := filesSDKService(t, account)
	_, err = serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	shareClient := serviceClient.NewShareClient(share)
	fileClient := shareClient.NewRootDirectoryClient().NewFileClient(fileName)
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	_, err = shareClient.Delete(ctx, nil)
	require.NoError(t, err)
	_, err = shareClient.GetProperties(ctx, nil)
	require.Error(t, err, "a soft-deleted share is not readable until it is restored")

	// Listing deleted shares gives the version Restore Share needs.
	pager := serviceClient.NewListSharesPager(&fileservice.ListSharesOptions{
		Include: fileservice.ListSharesInclude{Deleted: true},
	})
	var version string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Shares {
			if s.Name != nil && *s.Name == share && s.Deleted != nil && *s.Deleted && s.Version != nil {
				version = *s.Version
			}
		}
	}
	require.NotEmpty(t, version, "a soft-deleted share must be listed with its version")

	_, err = shareClient.Restore(ctx, version, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	buf := make([]byte, len(payload))
	n, err := fileClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n)
	assert.Equal(t, payload, buf, "Restore Share must bring the share's contents back")
}

// storageRawBodyAuthorized sends one storage data-plane request with a body to
// `<account>.<service>.<host>`.
func storageRawBodyAuthorized(t *testing.T, method, account, service, target, body, authorization string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+target, strings.NewReader(body))
	require.NoError(t, err)
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	req.Host = account + "." + service + ".localhost:" + port
	req.Header.Set("x-ms-version", "2025-11-05")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	// A request with no credential of its own presents the account key.
	storageSignSharedKey(req, req.Host)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
