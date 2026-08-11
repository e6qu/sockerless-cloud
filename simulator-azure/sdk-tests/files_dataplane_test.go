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
// simulator's coordinate. Everything below drives the real azfile clients — the
// same code and identifiers a client uses against Azure Files, differing only in
// the endpoint they are pointed at.
func filesSDKService(t *testing.T, account string) *fileservice.Client {
	t.Helper()
	client, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
		&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	return client
}

// TestFilesSDK_NestedDirectoryTreeEndToEnd is the path an Azure Container Apps
// or Azure Functions volume depends on: a directory tree is created, a file is
// written at depth, the tree is listed at that depth, the file is renamed, and
// everything is removed — and the whole time the share's backing directory,
// which a mounting workload sees, agrees with what the data plane reports.
func TestFilesSDK_NestedDirectoryTreeEndToEnd(t *testing.T) {
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

	// A directory is only created inside a directory that already exists.
	orphan := shareClient.NewDirectoryClient("missing").NewSubdirectoryClient("child")
	_, err = orphan.Create(ctx, nil)
	require.Error(t, err, "Create Directory must fail when its parent does not exist")

	artifactsDir := buildDir.NewSubdirectoryClient("artifacts")
	_, err = artifactsDir.Create(ctx, nil)
	require.NoError(t, err)

	// The directory really exists in the share's backing tree — the tree a
	// workload with the share mounted walks.
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

	// Listing at depth enumerates the directory's own contents.
	pager := artifactsDir.NewListFilesAndDirectoriesPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotNil(t, page.Segment)
	require.Len(t, page.Segment.Files, 1)
	require.NotNil(t, page.Segment.Files[0].Name)
	assert.Equal(t, "bundle.tar", *page.Segment.Files[0].Name)
	require.NotNil(t, page.DirectoryPath)
	assert.Equal(t, "build/artifacts", *page.DirectoryPath)

	// The parent lists the subdirectory, not the file inside it.
	parentPager := buildDir.NewListFilesAndDirectoriesPager(nil)
	parentPage, err := parentPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotNil(t, parentPage.Segment)
	require.Len(t, parentPage.Segment.Directories, 1)
	require.NotNil(t, parentPage.Segment.Directories[0].Name)
	assert.Equal(t, "artifacts", *parentPage.Segment.Directories[0].Name)
	assert.Empty(t, parentPage.Segment.Files)

	// Rename moves the file within the tree.
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

	// A directory holding anything cannot be deleted.
	_, err = artifactsDir.Delete(ctx, nil)
	require.Error(t, err, "Delete Directory must refuse a directory that is not empty")

	_, err = movedClient.Delete(ctx, nil)
	require.NoError(t, err)
	_, err = artifactsDir.Delete(ctx, nil)
	require.NoError(t, err)

	// Rename works on directories too.
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

// TestFilesSDK_DirectoryListingPagesWithPrefixAndMarker drives the paging
// contract a client depends on when a directory holds more entries than one
// page.
func TestFilesSDK_DirectoryListingPagesWithPrefixAndMarker(t *testing.T) {
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

	// Prefix narrows the enumeration; MaxResults bounds each page; the pager
	// follows NextMarker until the enumeration is exhausted.
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

// TestFilesSDK_ShareLeaseGuardsWrites: a share lease is a lock, and a write
// without the matching lease id is refused with the service's own precondition
// failure.
func TestFilesSDK_ShareLeaseGuardsWrites(t *testing.T) {
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

	// The share reports the lock.
	props, err := shareClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.LeaseState)
	assert.Equal(t, "leased", string(*props.LeaseState))

	// A write without the lease id is refused, and the share survives it.
	_, err = shareClient.SetMetadata(ctx, &fileshare.SetMetadataOptions{
		Metadata: map[string]*string{"owner": to.Ptr("intruder")},
	})
	require.Error(t, err, "Set Share Metadata must be refused while the share is leased")
	_, err = shareClient.Delete(ctx, nil)
	require.Error(t, err, "Delete Share must be refused while the share is leased")

	// The holder writes.
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

	// With the lease gone the share is everyone's again.
	_, err = shareClient.Delete(ctx, nil)
	require.NoError(t, err)
}

// TestFilesSDK_FileLeaseGuardsWrites is the file-level lock: an unleased write
// is refused and leaves the bytes alone.
func TestFilesSDK_FileLeaseGuardsWrites(t *testing.T) {
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

// TestFilesSDK_FilePropertiesRangesAndHandles drives Set File Properties,
// Resize, Clear Range, List Ranges and the handle operations.
func TestFilesSDK_FilePropertiesRangesAndHandles(t *testing.T) {
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

	// A file that has only been allocated holds no data yet, so it lists no
	// ranges; after a write the range covering the written bytes is reported.
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

	// Set File Properties records the content type, and Get File Properties
	// reads it back.
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

	// Resize really changes the file's size.
	_, err = fileClient.Resize(ctx, 8, nil)
	require.NoError(t, err)
	props, err = fileClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ContentLength)
	assert.Equal(t, int64(8), *props.ContentLength)

	// Clear Range zeroes the bytes it names.
	_, err = fileClient.ClearRange(ctx, file.HTTPRange{Offset: 0, Count: 4}, nil)
	require.NoError(t, err)
	buf := make([]byte, 8)
	_, err = fileClient.DownloadBuffer(ctx, buf, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte{0, 0, 0, 0}, buf[:4], "Clear Range must zero the range it names")
	assert.Equal(t, payload[4:8], buf[4:])

	// No SMB or NFS session can be open against a simulator share, so the
	// handle enumeration is empty and closing handles closes none.
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

// TestFilesSDK_ClearRangeDeallocatesTheRange: clearing a range must make the
// range stop existing, not merely read as zeros. List Ranges reports the
// extents the file actually holds, so a cleared range that stayed allocated
// would keep being reported — the caller would be told a range it cleared is
// still there.
func TestFilesSDK_ClearRangeDeallocatesTheRange(t *testing.T) {
	const (
		account  = "sdkfileclear"
		share    = "sdk-clear-share"
		fileName = "cleared.bin"
		// Large enough, and cleared on boundaries large enough, that the
		// cleared window spans whole filesystem allocation units on any
		// filesystem the simulator runs on.
		size      = 4 << 20
		clearFrom = 1 << 20
		clearTo   = 3 << 20
	)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i%251) + 1 // never zero, so a cleared byte is visible
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

	// The file keeps its size, the cleared bytes read as zeros, and the bytes
	// on either side are untouched.
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

	// Clearing the whole file leaves it holding nothing at all.
	_, err = fileClient.ClearRange(ctx, file.HTTPRange{Offset: 0, Count: size}, nil)
	require.NoError(t, err)
	empty, err := fileClient.GetRangeList(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty.Ranges, "a fully cleared file must report no ranges")
}

// TestFilesSDK_CopyAndUploadRangeFromURL drives Copy File and Upload Range from
// URL against a source in the same share.
func TestFilesSDK_CopyAndUploadRangeFromURL(t *testing.T) {
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

	// A copy on the simulator finishes inside the request that starts it, so
	// there is never a pending copy to abort and the service says exactly that.
	require.NotNil(t, copied.CopyID)
	_, err = destination.AbortCopy(ctx, *copied.CopyID, nil)
	require.Error(t, err, "Abort Copy must report that no copy is pending")

	// Upload Range from URL fills a destination range from the source's.
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

// TestFilesSDK_HardAndSymbolicLinks drives Create Hard Link, Create Symbolic
// Link and Read Symbolic Link, which are real links in the share's directory
// tree.
func TestFilesSDK_HardAndSymbolicLinks(t *testing.T) {
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

	// A hard link is a second name for one inode, so the filesystem the share
	// is backed by counts two links to it.
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

	// A link text that would leave the share is refused.
	escaping := root.NewFileClient("escape.txt")
	_, err = escaping.CreateSymbolicLink(ctx, "../../../etc/passwd", nil)
	require.Error(t, err, "a symbolic link must not be a way out of the share")
}

// TestFilesSDK_SharePermissionsStatisticsAndProperties drives the share-level
// operations an administrator uses: stored security descriptors, usage
// statistics, quota and the metadata round trip.
func TestFilesSDK_SharePermissionsStatisticsAndProperties(t *testing.T) {
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

	// The same descriptor always maps to the same key, which is what lets a
	// client cache it.
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

	// Share statistics measure what is actually stored.
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

// TestFilesSDK_ShareSnapshotFreezesContents: a snapshot is a real copy, so it
// keeps answering with the bytes the share held when it was taken.
func TestFilesSDK_ShareSnapshotFreezesContents(t *testing.T) {
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

	// The share moves on.
	_, err = fileClient.Create(ctx, int64(len(replacement)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, replacement, nil))

	live := make([]byte, len(replacement))
	_, err = fileClient.DownloadBuffer(ctx, live, nil)
	require.NoError(t, err)
	assert.Equal(t, replacement, live)

	// The snapshot does not.
	snapshotShare, err := shareClient.WithSnapshot(*snapshot.Snapshot)
	require.NoError(t, err)
	snapshotFile := snapshotShare.NewRootDirectoryClient().NewFileClient(fileName)
	frozen := make([]byte, len(original))
	_, err = snapshotFile.DownloadBuffer(ctx, frozen, nil)
	require.NoError(t, err)
	assert.Equal(t, original, frozen, "a snapshot must keep the bytes it froze")

	// The snapshot is listed beside the share it was taken of.
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

	// Deleting a snapshot leaves the share alone.
	_, err = snapshotShare.Delete(ctx, nil)
	require.NoError(t, err)
	_, err = fileClient.GetProperties(ctx, nil)
	require.NoError(t, err, "deleting a snapshot must not touch the share")
}

// TestFilesSDK_ServicePropertiesAndUserDelegationKey drives the two
// service-level operations beyond listing shares.
func TestFilesSDK_ServicePropertiesAndUserDelegationKey(t *testing.T) {
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

	// Get User Delegation Key issues a key bound to the window it is asked for.
	start := time.Now().UTC().Truncate(time.Second)
	expiry := start.Add(time.Hour)
	resp := storageRawBody(t, http.MethodPost, account, "file", "/?restype=service&comp=userdelegationkey",
		"<KeyInfo><Start>"+start.Format(time.RFC3339)+"</Start><Expiry>"+expiry.Format(time.RFC3339)+"</Expiry></KeyInfo>")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Contains(t, string(body), "<UserDelegationKey>")
	assert.Contains(t, string(body), "<SignedExpiry>"+expiry.Format(time.RFC3339)+"</SignedExpiry>")
	assert.Contains(t, string(body), "<SignedService>f</SignedService>")
}

// TestFilesSDK_ShareSoftDeleteAndRestore: with the account's File service
// carrying a share delete-retention policy, Delete Share keeps the share and
// Restore Share brings it back with its contents.
func TestFilesSDK_ShareSoftDeleteAndRestore(t *testing.T) {
	const (
		account  = "sdkfilesoftdel"
		share    = "sdk-softdelete-share"
		fileName = "restored.txt"
	)
	payload := []byte("bytes that must survive a soft delete")

	// An administrator enables share soft delete on the storage account's File
	// service, the way the policy is configured on real Azure.
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

	// The deleted share is enumerable, which is how a client learns the version
	// Restore Share needs.
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

// storageRawBody issues one storage data-plane request carrying a body at the
// `<account>.<service>.<host>` coordinate.
func storageRawBody(t *testing.T, method, account, service, target, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+target, strings.NewReader(body))
	require.NoError(t, err)
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	req.Host = account + "." + service + ".localhost:" + port
	req.Header.Set("x-ms-version", "2025-11-05")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
