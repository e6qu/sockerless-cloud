package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Azure Files data plane — directories, files, handles, ranges and links.
//
// Every directory below a share is a real directory and every file a real file
// in the share's backing tree, so a nested path created here is a nested path a
// workload with the share mounted walks, and vice versa. What a POSIX tree
// cannot express — user metadata, security-descriptor keys, leases — lives in
// the simulator's persistent stores.

// ── Entry dispatch ──────────────────────────────────────────────────

// handleFilesEntryOperation serves every operation addressed below a share:
// the share's root directory (entryPath == "") and every directory and file
// under it. `restype` selects the kind of resource the request names, exactly
// as the REST API defines it.
func handleFilesEntryOperation(w http.ResponseWriter, r *http.Request, account, share, entryPath, restype, comp string) {
	switch restype {
	case "directory":
		handleFilesDirectoryOperation(w, r, account, share, entryPath, comp)
	case "hardlink":
		if r.Method == http.MethodPut && comp == "" && entryPath != "" {
			handleFilesCreateHardLink(w, r, account, share, entryPath)
			return
		}
		writeStorageOperationNotImplemented(w, r, "Files")
	case "symboliclink":
		if comp != "" || entryPath == "" {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		switch r.Method {
		case http.MethodPut:
			handleFilesCreateSymbolicLink(w, r, account, share, entryPath)
		case http.MethodGet:
			handleFilesGetSymbolicLink(w, r, account, share, entryPath)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case "":
		// List Handles and Force Close Handles are spelled the same way for a
		// directory and for a file; the resource the path names decides which,
		// exactly as it does on the service.
		switch comp {
		case "listhandles":
			if r.Method != http.MethodGet {
				writeStorageOperationNotImplemented(w, r, "Files")
				return
			}
			handleFilesListHandles(w, r, account, share, entryPath)
			return
		case "forceclosehandles":
			if r.Method != http.MethodPut {
				writeStorageOperationNotImplemented(w, r, "Files")
				return
			}
			handleFilesForceCloseHandles(w, r, account, share, entryPath)
			return
		}
		if entryPath == "" {
			// The share root is addressed as a share or as a directory, never
			// as a file.
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesFileOperation(w, r, account, share, entryPath, comp)
	default:
		writeStorageOperationNotImplemented(w, r, "Files")
	}
}

func handleFilesDirectoryOperation(w http.ResponseWriter, r *http.Request, account, share, dirPath, comp string) {
	switch r.Method {
	case http.MethodPut:
		switch comp {
		case "":
			if dirPath == "" {
				// The share root is created by Create Share.
				writeStorageOperationNotImplemented(w, r, "Files")
				return
			}
			handleFilesCreateDirectory(w, r, account, share, dirPath)
		case "metadata":
			handleFilesSetDirectoryMetadata(w, r, account, share, dirPath)
		case "properties":
			handleFilesSetDirectoryProperties(w, r, account, share, dirPath)
		case "rename":
			handleFilesRenameEntry(w, r, account, share, dirPath, true)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case http.MethodGet, http.MethodHead:
		switch comp {
		case "list":
			handleFilesListDirectory(w, r, account, share, dirPath)
		case "":
			handleFilesGetDirectoryProperties(w, r, account, share, dirPath)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case http.MethodDelete:
		if comp != "" {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesDeleteDirectory(w, r, account, share, dirPath)
	default:
		writeStorageOperationNotImplemented(w, r, "Files")
	}
}

func handleFilesFileOperation(w http.ResponseWriter, r *http.Request, account, share, filePath, comp string) {
	switch r.Method {
	case http.MethodPut:
		switch comp {
		case "":
			// Copy File is the bare PUT carrying x-ms-copy-source, the spelling
			// every client sends; Create File is the same PUT without it.
			if r.Header.Get("x-ms-copy-source") != "" {
				handleFilesStartCopy(w, r, account, share, filePath)
				return
			}
			handleFilesCreateFile(w, r, account, share, filePath)
		case "range":
			// Upload Range writes the request body; Upload Range From URL names
			// its source in x-ms-copy-source.
			if r.Header.Get("x-ms-copy-source") != "" {
				handleFilesUploadRangeFromURL(w, r, account, share, filePath)
				return
			}
			handleFilesUploadRange(w, r, account, share, filePath)
		case "properties":
			handleFilesSetFileHTTPHeaders(w, r, account, share, filePath)
		case "metadata":
			handleFilesSetFileMetadata(w, r, account, share, filePath)
		case "lease":
			handleFilesFileLease(w, r, account, share, filePath)
		case "rename":
			handleFilesRenameEntry(w, r, account, share, filePath, false)
		case "copy":
			// Abort Copy names the copy it is aborting in the copyid query
			// parameter; Start Copy carries no copyid.
			if r.URL.Query().Has("copyid") {
				handleFilesAbortCopy(w, r, account, share, filePath)
				return
			}
			handleFilesStartCopy(w, r, account, share, filePath)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case http.MethodGet:
		switch comp {
		case "":
			handleFilesGetFile(w, r, account, share, filePath)
		case "rangelist":
			handleFilesGetRangeList(w, r, account, share, filePath)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case http.MethodHead:
		if comp != "" {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesHeadFile(w, r, account, share, filePath)
	case http.MethodDelete:
		if comp != "" {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesDeleteFile(w, r, account, share, filePath)
	default:
		writeStorageOperationNotImplemented(w, r, "Files")
	}
}

// ── Directories ─────────────────────────────────────────────────────

// filesStatDirectory resolves a share-relative directory path to its on-disk
// path and the filesystem's record of it, writing the Azure error and reporting
// false when the share, the name or the directory does not hold up.
func filesStatDirectory(w http.ResponseWriter, r *http.Request, account, share, dirPath string) (string, os.FileInfo, bool) {
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return "", nil, false
	}
	hostPath, ok := filesEntryHostPath(root, dirPath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return "", nil, false
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		if !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return "", nil, false
		}
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return "", nil, false
	}
	if !info.IsDir() {
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return "", nil, false
	}
	return hostPath, info, true
}

// handleFilesCreateDirectory is Create Directory: it makes a real directory
// inside the share, which is the directory a mounting workload then sees.
func handleFilesCreateDirectory(w http.ResponseWriter, r *http.Request, account, share, dirPath string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	hostPath, ok := fileShareHostPath(account, share, dirPath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	// Azure Files creates a directory only inside a directory that already
	// exists; the parent is made by its own Create Directory.
	if parent, err := os.Stat(filepath.Dir(hostPath)); err != nil || !parent.IsDir() {
		if err != nil && !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		writeStorageError(w, "ParentNotFound",
			"The specified parent path does not exist.", http.StatusNotFound)
		return
	}
	if _, err := os.Lstat(hostPath); err == nil {
		writeStorageError(w, "ResourceAlreadyExists",
			"The specified resource already exists.", http.StatusConflict)
		return
	}
	if err := os.Mkdir(hostPath, 0o777); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	// A real Azure Files share is mounted with CIFS dir_mode 0777, so a
	// non-root workload can write inside a directory the data plane created;
	// chmod past the process umask so the materialized directory matches.
	if err := os.Chmod(hostPath, 0o777); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if err := filesApplyLastWriteTime(r, hostPath); err != nil {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-file-last-write-time.",
			http.StatusBadRequest)
		return
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	entry := FileDirectoryData{
		Account: account, Share: share, Path: dirPath,
		Metadata:      collectMetadata(r),
		PermissionKey: filesStorePermissionFromRequest(r, account, share),
		Created:       info.ModTime().UTC().Format(azureFileTimeFormat),
	}
	fileDirectories.Put(fileDirectoryKey(account, share, dirPath), entry)
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	w.Header().Set("x-ms-file-file-type", "Directory")
	writeFileSMBHeaders(w, share, dirPath, info, entry.Created, entry.PermissionKey)
	w.WriteHeader(http.StatusCreated)
}

func handleFilesGetDirectoryProperties(w http.ResponseWriter, r *http.Request, account, share, dirPath string) {
	_, info, ok := filesStatDirectory(w, r, account, share, dirPath)
	if !ok {
		return
	}
	entry, _ := fileDirectories.Get(fileDirectoryKey(account, share, dirPath))
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-server-encrypted", "false")
	w.Header().Set("x-ms-file-file-type", "Directory")
	for k, v := range entry.Metadata {
		w.Header().Set("x-ms-meta-"+k, v)
	}
	writeFileSMBHeaders(w, share, dirPath, info, entry.Created, entry.PermissionKey)
	w.WriteHeader(http.StatusOK)
}

// handleFilesDeleteDirectory is Delete Directory: it removes the directory,
// which the service permits only when nothing is left in it.
func handleFilesDeleteDirectory(w http.ResponseWriter, r *http.Request, account, share, dirPath string) {
	if dirPath == "" {
		writeStorageError(w, "InvalidUri",
			"The requested URI does not represent any resource on the server.", http.StatusBadRequest)
		return
	}
	hostPath, _, ok := filesStatDirectory(w, r, account, share, dirPath)
	if !ok {
		return
	}
	entries, err := os.ReadDir(hostPath)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if len(entries) > 0 {
		writeStorageError(w, "DirectoryNotEmpty",
			"The specified directory is not empty.", http.StatusConflict)
		return
	}
	if err := os.Remove(hostPath); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	fileDirectories.Delete(fileDirectoryKey(account, share, dirPath))
	w.WriteHeader(http.StatusAccepted)
}

func handleFilesSetDirectoryMetadata(w http.ResponseWriter, r *http.Request, account, share, dirPath string) {
	_, info, ok := filesStatDirectory(w, r, account, share, dirPath)
	if !ok {
		return
	}
	key := fileDirectoryKey(account, share, dirPath)
	entry, _ := fileDirectories.Get(key)
	entry.Account, entry.Share, entry.Path = account, share, dirPath
	entry.Metadata = collectMetadata(r)
	if entry.Created == "" {
		entry.Created = info.ModTime().UTC().Format(azureFileTimeFormat)
	}
	fileDirectories.Put(key, entry)
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	w.WriteHeader(http.StatusOK)
}

func handleFilesSetDirectoryProperties(w http.ResponseWriter, r *http.Request, account, share, dirPath string) {
	hostPath, _, ok := filesStatDirectory(w, r, account, share, dirPath)
	if !ok {
		return
	}
	if err := filesApplyLastWriteTime(r, hostPath); err != nil {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-file-last-write-time.",
			http.StatusBadRequest)
		return
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	key := fileDirectoryKey(account, share, dirPath)
	entry, _ := fileDirectories.Get(key)
	entry.Account, entry.Share, entry.Path = account, share, dirPath
	if permissionKey := filesStorePermissionFromRequest(r, account, share); permissionKey != "" {
		entry.PermissionKey = permissionKey
	}
	if raw := r.Header.Get("x-ms-file-creation-time"); raw != "" && !strings.EqualFold(raw, "preserve") {
		entry.Created = raw
	}
	if entry.Created == "" {
		entry.Created = info.ModTime().UTC().Format(azureFileTimeFormat)
	}
	fileDirectories.Put(key, entry)
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	writeFileSMBHeaders(w, share, dirPath, info, entry.Created, entry.PermissionKey)
	w.WriteHeader(http.StatusOK)
}

// ── Listing ─────────────────────────────────────────────────────────

type filesListProperties struct {
	ContentLength *int64 `xml:"Content-Length,omitempty"`
	LastModified  string `xml:"Last-Modified,omitempty"`
	ETag          string `xml:"Etag,omitempty"`
}

type filesListEntry struct {
	Name       string              `xml:"Name"`
	FileID     string              `xml:"FileId,omitempty"`
	Properties filesListProperties `xml:"Properties"`
}

type filesListSegment struct {
	Directories []filesListEntry `xml:"Directory"`
	Files       []filesListEntry `xml:"File"`
}

type filesListResponse struct {
	XMLName         xml.Name         `xml:"EnumerationResults"`
	ServiceEndpoint string           `xml:"ServiceEndpoint,attr"`
	ShareName       string           `xml:"ShareName,attr"`
	ShareSnapshot   string           `xml:"ShareSnapshot,attr,omitempty"`
	DirectoryPath   string           `xml:"DirectoryPath,attr"`
	Prefix          string           `xml:"Prefix"`
	Marker          string           `xml:"Marker,omitempty"`
	MaxResults      *int32           `xml:"MaxResults,omitempty"`
	DirectoryID     string           `xml:"DirectoryId,omitempty"`
	Segment         filesListSegment `xml:"Entries"`
	NextMarker      string           `xml:"NextMarker"`
}

// handleFilesListDirectory is List Directories and Files at any depth. It
// enumerates the directory's real contents, so it names both what was written
// through this data plane and what a workload with the share mounted created,
// and it pages the enumeration the way the service does: `prefix` filters,
// `marker` resumes after the last entry the previous page returned, and
// `maxresults` bounds the page.
func handleFilesListDirectory(w http.ResponseWriter, r *http.Request, account, share, dirPath string) {
	hostPath, _, ok := filesStatDirectory(w, r, account, share, dirPath)
	if !ok {
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	marker := q.Get("marker")
	maxResults := 0
	if raw := q.Get("maxresults"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeStorageError(w, "OutOfRangeQueryParameterValue",
				"One of the query parameters specified in the request URI is outside the permissible range: maxresults.",
				http.StatusBadRequest)
			return
		}
		maxResults = n
	}
	entries, err := os.ReadDir(hostPath)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	out := filesListResponse{
		ServiceEndpoint: azureStorageEndpointURL(r, account, "file"),
		ShareName:       share,
		ShareSnapshot:   q.Get("sharesnapshot"),
		DirectoryPath:   dirPath,
		Prefix:          prefix,
		Marker:          marker,
		DirectoryID:     filesEntryID(share, dirPath),
	}
	if maxResults > 0 {
		n := int32(maxResults)
		out.MaxResults = &n
	}
	count := 0
	for _, entry := range filesSortedNames(entries) {
		name := entry.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if marker != "" && name <= marker {
			continue
		}
		if maxResults > 0 && count == maxResults {
			out.NextMarker = marker
			break
		}
		info, err := entry.Info()
		if err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		listed := filesListEntry{
			Name:   name,
			FileID: filesEntryID(share, filesJoinPath(dirPath, name)),
			Properties: filesListProperties{
				LastModified: info.ModTime().UTC().Format(http.TimeFormat),
				ETag:         fileETag(info),
			},
		}
		if entry.IsDir() {
			out.Segment.Directories = append(out.Segment.Directories, listed)
		} else {
			size := info.Size()
			listed.Properties.ContentLength = &size
			out.Segment.Files = append(out.Segment.Files, listed)
		}
		marker = name
		count++
	}
	writeStorageXML(w, http.StatusOK, out)
}

// ── Handles ─────────────────────────────────────────────────────────

// handleFilesListHandles is List Handles. A handle is an open SMB or NFS
// session against the share; the simulator's shares are reached over this REST
// surface and as a host bind mount, neither of which opens a Files session, so
// the enumeration is the true one: no session holds this entry open.
func handleFilesListHandles(w http.ResponseWriter, r *http.Request, account, share, entryPath string) {
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return
	}
	hostPath, ok := filesEntryHostPath(root, entryPath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(hostPath); err != nil {
		if !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	type handleList struct {
		XMLName    xml.Name `xml:"EnumerationResults"`
		Entries    struct{} `xml:"Entries"`
		NextMarker string   `xml:"NextMarker"`
	}
	writeStorageXML(w, http.StatusOK, handleList{})
}

// handleFilesForceCloseHandles is Force Close Handles. It closes the sessions
// holding the entry open and reports how many it closed — no Files session can
// exist against a simulator share, so the honest count is zero.
func handleFilesForceCloseHandles(w http.ResponseWriter, r *http.Request, account, share, entryPath string) {
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return
	}
	hostPath, ok := filesEntryHostPath(root, entryPath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(hostPath); err != nil {
		if !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	if r.Header.Get("x-ms-handle-id") == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-handle-id.",
			http.StatusBadRequest)
		return
	}
	w.Header().Set("x-ms-number-of-handles-closed", "0")
	w.Header().Set("x-ms-number-of-handles-failed", "0")
	w.Header().Set("x-ms-marker", "")
	w.WriteHeader(http.StatusOK)
}

// ── File properties, metadata and leases ────────────────────────────

// handleFilesSetFileHTTPHeaders is Set File Properties: it replaces the file's
// system properties, and resizes the file when x-ms-content-length names a new
// size — the operation Azure Files resizes a file with.
func handleFilesSetFileHTTPHeaders(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	hostPath, _, props, ok := statShareFile(w, account, share, filePath)
	if !ok {
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	if raw := r.Header.Get("x-ms-content-length"); raw != "" {
		size, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || size < 0 {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-content-length.",
				http.StatusBadRequest)
			return
		}
		if err := os.Truncate(hostPath, size); err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := filesApplyLastWriteTime(r, hostPath); err != nil {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-file-last-write-time.",
			http.StatusBadRequest)
		return
	}
	props.Account, props.Share, props.Path = account, share, filePath
	if raw := r.Header.Get("x-ms-content-type"); raw != "" {
		props.ContentType = raw
	}
	fileObjects.Put(fileObjectKey(account, share, filePath), props)
	info, err := os.Stat(hostPath)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	writeFileSMBHeaders(w, share, filePath, info, "", filesStorePermissionFromRequest(r, account, share))
	w.WriteHeader(http.StatusOK)
}

// handleFilesSetFileMetadata is Set File Metadata: the x-ms-meta-* headers
// replace the file's metadata wholesale.
func handleFilesSetFileMetadata(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	_, info, props, ok := statShareFile(w, account, share, filePath)
	if !ok {
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	props.Account, props.Share, props.Path = account, share, filePath
	props.Metadata = collectMetadata(r)
	fileObjects.Put(fileObjectKey(account, share, filePath), props)
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	w.WriteHeader(http.StatusOK)
}

func handleFilesFileLease(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	if r.Method != http.MethodPut {
		writeStorageOperationNotImplemented(w, r, "Files")
		return
	}
	_, info, _, ok := statShareFile(w, account, share, filePath)
	if !ok {
		return
	}
	filesHandleLease(w, r, account, share, filePath, "file",
		fileETag(info), info.ModTime().UTC().Format(http.TimeFormat))
}

// ── Ranges ──────────────────────────────────────────────────────────

// handleFilesGetRangeList is List Ranges: it reports the ranges of the file
// that hold data. The share's files are real files, so the ranges come from the
// filesystem's own record of which extents of the file are allocated — the same
// question Azure Files answers about its own storage.
func handleFilesGetRangeList(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return
	}
	hostPath, ok := filesEntryHostPath(root, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(hostPath)
	if err != nil || info.IsDir() {
		if err != nil && !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		writeStorageError(w, "ResourceNotFound", "The specified file does not exist.", http.StatusNotFound)
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	ranges, err := fileDataRanges(hostPath, info.Size())
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	// An x-ms-range header narrows the enumeration to the requested window.
	if raw := r.Header.Get("x-ms-range"); raw != "" {
		start, end, valid := parseAzureFileRange(raw)
		if !valid {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-range.",
				http.StatusBadRequest)
			return
		}
		ranges = clipFileRanges(ranges, start, end)
	}
	type fileRange struct {
		XMLName xml.Name `xml:"Range"`
		Start   int64    `xml:"Start"`
		End     int64    `xml:"End"`
	}
	type rangeList struct {
		XMLName xml.Name    `xml:"Ranges"`
		Ranges  []fileRange `xml:"Range"`
	}
	out := rangeList{}
	for _, rg := range ranges {
		out.Ranges = append(out.Ranges, fileRange{Start: rg.Start, End: rg.End})
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-content-length", strconv.FormatInt(info.Size(), 10))
	writeStorageXML(w, http.StatusOK, out)
}

// fileByteRange is one inclusive byte range of a file.
type fileByteRange struct {
	Start int64
	End   int64
}

// clipFileRanges narrows an enumeration to the window [start, end].
func clipFileRanges(ranges []fileByteRange, start, end int64) []fileByteRange {
	var out []fileByteRange
	for _, rg := range ranges {
		lo, hi := rg.Start, rg.End
		if lo < start {
			lo = start
		}
		if hi > end {
			hi = end
		}
		if lo <= hi {
			out = append(out, fileByteRange{Start: lo, End: hi})
		}
	}
	return out
}

// handleFilesUploadRangeFromURL is Upload Range from URL: it copies the source
// range out of the file the x-ms-copy-source URL names and writes it into this
// file's range.
func handleFilesUploadRangeFromURL(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	hostPath, info, _, ok := statShareFile(w, account, share, filePath)
	if !ok {
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	start, end, valid := parseAzureFileRange(r.Header.Get("x-ms-range"))
	if !valid {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-range.",
			http.StatusBadRequest)
		return
	}
	if end >= info.Size() {
		writeStorageError(w, "InvalidRange",
			"The range specified is invalid for the current size of the resource.",
			http.StatusRequestedRangeNotSatisfiable)
		return
	}
	sourcePath, ok := filesResolveCopySource(w, r.Header.Get("x-ms-copy-source"))
	if !ok {
		return
	}
	sourceStart, sourceEnd := start, end
	if raw := r.Header.Get("x-ms-source-range"); raw != "" {
		s, e, valid := parseAzureFileRange(raw)
		if !valid {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-source-range.",
				http.StatusBadRequest)
			return
		}
		sourceStart, sourceEnd = s, e
	}
	if sourceEnd-sourceStart != end-start {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-source-range.",
			http.StatusBadRequest)
		return
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source does not exist.", http.StatusNotFound)
		return
	}
	defer source.Close()
	data := make([]byte, sourceEnd-sourceStart+1)
	if _, err := source.ReadAt(data, sourceStart); err != nil {
		writeStorageError(w, "InvalidRange",
			"The range specified is invalid for the current size of the source.",
			http.StatusRequestedRangeNotSatisfiable)
		return
	}
	f, err := os.OpenFile(hostPath, os.O_RDWR, 0)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := f.WriteAt(data, start); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	written, err := f.Stat()
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(written))
	w.Header().Set("Last-Modified", written.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	w.WriteHeader(http.StatusCreated)
}

// ── Rename ──────────────────────────────────────────────────────────

// handleFilesRenameEntry is Rename File and Rename Directory. The request
// addresses the destination and names the source in x-ms-file-rename-source, so
// the entry really moves in the share's directory tree.
func handleFilesRenameEntry(w http.ResponseWriter, r *http.Request, account, share, destPath string, directory bool) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	raw := r.Header.Get("x-ms-file-rename-source")
	if raw == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-file-rename-source.",
			http.StatusBadRequest)
		return
	}
	srcAccount, srcShare, srcPath, ok := parseFilesResourceURL(raw)
	if !ok || srcAccount != account || srcShare != share {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-file-rename-source.",
			http.StatusBadRequest)
		return
	}
	sourceHost, ok := fileShareHostPath(account, share, srcPath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	destHost, ok := fileShareHostPath(account, share, destPath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	sourceInfo, err := os.Lstat(sourceHost)
	if err != nil {
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	if sourceInfo.IsDir() != directory {
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	if !filesRequireLease(w, r, account, share, srcPath, "file") {
		return
	}
	replace := strings.EqualFold(r.Header.Get("x-ms-file-rename-replace-if-exists"), "true")
	if _, err := os.Lstat(destHost); err == nil && !replace {
		writeStorageError(w, "ResourceAlreadyExists",
			"The specified resource already exists.", http.StatusConflict)
		return
	}
	if parent, err := os.Stat(filepath.Dir(destHost)); err != nil || !parent.IsDir() {
		writeStorageError(w, "ParentNotFound",
			"The specified parent path does not exist.", http.StatusNotFound)
		return
	}
	if err := os.Rename(sourceHost, destHost); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	filesMoveEntryRecords(account, share, srcPath, destPath, directory)
	info, err := os.Lstat(destHost)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-request-server-encrypted", "false")
	writeFileSMBHeaders(w, share, destPath, info, "", filesStorePermissionFromRequest(r, account, share))
	w.WriteHeader(http.StatusOK)
}

// filesMoveEntryRecords carries the properties, metadata and lease rows of a
// renamed entry — and, for a directory, of everything under it — to the new
// path, so the move is complete rather than only on disk.
func filesMoveEntryRecords(account, share, srcPath, destPath string, directory bool) {
	rename := func(p string) string {
		switch {
		case p == srcPath:
			return destPath
		case strings.HasPrefix(p, srcPath+"/"):
			return destPath + strings.TrimPrefix(p, srcPath)
		default:
			return ""
		}
	}
	for _, obj := range fileObjects.List() {
		if obj.Account != account || obj.Share != share {
			continue
		}
		moved := rename(obj.Path)
		if moved == "" {
			continue
		}
		fileObjects.Delete(fileObjectKey(account, share, obj.Path))
		obj.Path = moved
		fileObjects.Put(fileObjectKey(account, share, moved), obj)
	}
	for _, dir := range fileDirectories.List() {
		if dir.Account != account || dir.Share != share {
			continue
		}
		moved := rename(dir.Path)
		if moved == "" {
			continue
		}
		fileDirectories.Delete(fileDirectoryKey(account, share, dir.Path))
		dir.Path = moved
		fileDirectories.Put(fileDirectoryKey(account, share, moved), dir)
	}
	for _, lease := range fileLeases.List() {
		if lease.Account != account || lease.Share != share {
			continue
		}
		moved := rename(lease.Path)
		if moved == "" {
			continue
		}
		fileLeases.Delete(fileLeaseKey(account, share, lease.Path))
		lease.Path = moved
		fileLeases.Put(fileLeaseKey(account, share, moved), lease)
	}
}

// ── Copy ────────────────────────────────────────────────────────────

// handleFilesStartCopy is Copy File. The simulator's shares are local, so the
// copy finishes within the request and the response reports the completed
// status the service reports for a copy that has already succeeded.
func handleFilesStartCopy(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	source := r.Header.Get("x-ms-copy-source")
	if source == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-copy-source.",
			http.StatusBadRequest)
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	sourcePath, ok := filesResolveCopySource(w, source)
	if !ok {
		return
	}
	destHost, ok := fileShareHostPath(account, share, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	if parent, err := os.Stat(filepath.Dir(destHost)); err != nil || !parent.IsDir() {
		writeStorageError(w, "ParentNotFound",
			"The specified parent path does not exist.", http.StatusNotFound)
		return
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source does not exist.", http.StatusNotFound)
		return
	}
	defer in.Close()
	out, err := os.OpenFile(destHost, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Chmod(destHost, 0o666); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	metadata := collectMetadata(r)
	sourceProps := filesSourceProperties(source)
	if len(metadata) == 0 {
		metadata = sourceProps.Metadata
	}
	fileObjects.Put(fileObjectKey(account, share, filePath), FileObject{
		Account: account, Share: share, Path: filePath,
		ContentType: sourceProps.ContentType,
		Metadata:    metadata,
	})
	info, err := out.Stat()
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-copy-id", generateUUID())
	w.Header().Set("x-ms-copy-status", "success")
	w.WriteHeader(http.StatusAccepted)
}

// handleFilesAbortCopy is Abort Copy File. A copy on the simulator completes
// within the request that starts it, so no copy is ever pending and the service
// answers exactly as it does when the named copy has already finished.
func handleFilesAbortCopy(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	if _, _, _, ok := statShareFile(w, account, share, filePath); !ok {
		return
	}
	if !strings.EqualFold(r.Header.Get("x-ms-copy-action"), "abort") {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-copy-action.",
			http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("copyid") == "" {
		writeStorageError(w, "InvalidQueryParameterValue",
			"Value for one of the query parameters specified in the request URI is invalid: copyid.",
			http.StatusBadRequest)
		return
	}
	writeStorageError(w, "NoPendingCopyOperation",
		"There is currently no pending copy operation.", http.StatusConflict)
}

// filesSourceProperties returns the data-plane properties recorded for the file
// a copy source URL names, so a copy carries the source's content type and
// metadata the way the service does.
func filesSourceProperties(sourceURL string) FileObject {
	account, share, path, ok := parseFilesResourceURL(sourceURL)
	if !ok {
		return FileObject{}
	}
	props, _ := fileObjects.Get(fileObjectKey(account, share, path))
	return props
}

// filesResolveCopySource resolves a copy-source URL to the on-disk file it
// names inside this simulator's Files data plane.
func filesResolveCopySource(w http.ResponseWriter, sourceURL string) (string, bool) {
	account, share, path, ok := parseFilesResourceURL(sourceURL)
	if !ok {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source is invalid.", http.StatusNotFound)
		return "", false
	}
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source does not exist.", http.StatusNotFound)
		return "", false
	}
	hostPath, ok := fileShareHostPath(account, share, path)
	if !ok {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source is invalid.", http.StatusNotFound)
		return "", false
	}
	return hostPath, true
}

// parseFilesResourceURL splits a Files data-plane URL into the account, share
// and share-relative path it addresses. Both spellings a client can be
// configured with are understood: the `<account>.file.<host>/<share>/<path>`
// hostname Azure publishes, and the `/file/<account>/<share>/<path>` path-style
// form an endpoint-configured SDK produces.
func parseFilesResourceURL(raw string) (account, share, entryPath string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", false
	}
	escaped := strings.TrimPrefix(u.EscapedPath(), "/")
	if escaped == "" {
		return "", "", "", false
	}
	if parts := strings.SplitN(u.Hostname(), ".file.", 2); len(parts) == 2 {
		account, ok = pathUnescape(parts[0])
		if !ok {
			return "", "", "", false
		}
	} else {
		rest, found := strings.CutPrefix(escaped, "file/")
		if !found {
			return "", "", "", false
		}
		accountEsc, remainder, found := strings.Cut(rest, "/")
		if !found {
			return "", "", "", false
		}
		account, ok = pathUnescape(accountEsc)
		if !ok {
			return "", "", "", false
		}
		escaped = remainder
	}
	shareEsc, pathEsc, found := strings.Cut(escaped, "/")
	if !found || pathEsc == "" {
		return "", "", "", false
	}
	share, ok = pathUnescape(shareEsc)
	if !ok {
		return "", "", "", false
	}
	entryPath, ok = pathUnescape(strings.Trim(pathEsc, "/"))
	if !ok || entryPath == "" {
		return "", "", "", false
	}
	return account, share, entryPath, true
}

// ── Hard and symbolic links ─────────────────────────────────────────

// handleFilesCreateHardLink is Create Hard Link: it makes a real hard link, so
// the two names share one inode and one set of bytes exactly as they do on an
// NFS Azure file share.
func handleFilesCreateHardLink(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	target := strings.Trim(r.Header.Get("x-ms-file-target-file"), "/")
	if target == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-file-target-file.",
			http.StatusBadRequest)
		return
	}
	targetHost, ok := fileShareHostPath(account, share, target)
	if !ok {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-file-target-file.",
			http.StatusBadRequest)
		return
	}
	linkHost, ok := fileShareHostPath(account, share, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(targetHost); err != nil {
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	if _, err := os.Lstat(linkHost); err == nil {
		writeStorageError(w, "ResourceAlreadyExists",
			"The specified resource already exists.", http.StatusConflict)
		return
	}
	if err := os.Link(targetHost, linkHost); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	targetProps, _ := fileObjects.Get(fileObjectKey(account, share, target))
	fileObjects.Put(fileObjectKey(account, share, filePath), FileObject{
		Account: account, Share: share, Path: filePath,
		ContentType: targetProps.ContentType,
		Metadata:    targetProps.Metadata,
	})
	info, err := os.Stat(linkHost)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-file-file-type", "File")
	if links, ok := statLinkCount(info); ok {
		w.Header().Set("x-ms-link-count", strconv.Itoa(links))
	}
	writeFileSMBHeaders(w, share, filePath, info, "", "")
	w.WriteHeader(http.StatusCreated)
}

// handleFilesCreateSymbolicLink is Create Symbolic Link: it makes a real
// symbolic link inside the share. The link text must resolve to somewhere in
// the share, so a link can never be a way out of it.
func handleFilesCreateSymbolicLink(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	linkText := r.Header.Get("x-ms-link-text")
	if linkText == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-link-text.",
			http.StatusBadRequest)
		return
	}
	linkHost, ok := fileShareHostPath(account, share, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	shareRoot := FileShareHostDir(account, share)
	resolved := linkText
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(linkHost), linkText)
	}
	if rel, err := filepath.Rel(shareRoot, filepath.Clean(resolved)); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-link-text.",
			http.StatusBadRequest)
		return
	}
	if _, err := os.Lstat(linkHost); err == nil {
		writeStorageError(w, "ResourceAlreadyExists",
			"The specified resource already exists.", http.StatusConflict)
		return
	}
	if err := os.Symlink(linkText, linkHost); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	fileObjects.Put(fileObjectKey(account, share, filePath), FileObject{
		Account: account, Share: share, Path: filePath,
		Metadata: collectMetadata(r),
	})
	info, err := os.Lstat(linkHost)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-file-file-type", "SymLink")
	writeFileSMBHeaders(w, share, filePath, info, "", "")
	w.WriteHeader(http.StatusCreated)
}

// handleFilesGetSymbolicLink is Get Symbolic Link: it reads the link back.
func handleFilesGetSymbolicLink(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return
	}
	linkHost, ok := filesEntryHostPath(root, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	info, err := os.Lstat(linkHost)
	if err != nil {
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	if info.Mode()&os.ModeSymlink == 0 {
		writeStorageError(w, "InvalidOperation",
			"The specified resource is not a symbolic link.", http.StatusConflict)
		return
	}
	linkText, err := os.Readlink(linkHost)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("x-ms-link-text", linkText)
	w.WriteHeader(http.StatusOK)
}

// filesShareSnapshotsFor returns the snapshots taken of one share, oldest
// first — the order List Shares enumerates them in.
func filesShareSnapshotsFor(account, share string) []FileShareSnapshotData {
	var out []FileShareSnapshotData
	for _, snap := range fileShareSnapshots.List() {
		if snap.Account == account && snap.Share == share {
			out = append(out, snap)
		}
	}
	sortFileShareSnapshots(out)
	return out
}

func sortFileShareSnapshots(snaps []FileShareSnapshotData) {
	for i := 1; i < len(snaps); i++ {
		for j := i; j > 0 && snaps[j].Snapshot < snaps[j-1].Snapshot; j-- {
			snaps[j], snaps[j-1] = snaps[j-1], snaps[j]
		}
	}
}

// filesDeleteShareContents removes everything one share owns: its snapshots,
// its stored permissions, its leases and its directory rows. Delete Share does
// this whether the share is destroyed outright or soft-deleted, because none of
// it survives the share.
func filesDeleteShareContents(account, share string) error {
	for _, snap := range filesShareSnapshotsFor(account, share) {
		fileShareSnapshots.Delete(fileShareSnapshotKey(account, share, snap.Snapshot))
	}
	if err := os.RemoveAll(filepath.Join(fileShareSnapshotRoot(account), share)); err != nil {
		return err
	}
	prefix := account + "/" + share + "/"
	for _, dir := range fileDirectories.List() {
		if key := fileDirectoryKey(dir.Account, dir.Share, dir.Path); strings.HasPrefix(key, prefix) {
			fileDirectories.Delete(key)
		}
	}
	for _, perm := range filePermissions.List() {
		if key := filePermissionKey(perm.Account, perm.Share, perm.Key); strings.HasPrefix(key, prefix) {
			filePermissions.Delete(key)
		}
	}
	filesDropLeases(account, share)
	return nil
}

// filesSoftDeleteRetentionDays reports the share delete-retention policy the
// account's File service carries, as an administrator configured it through the
// Microsoft.Storage fileServices resource. Zero means share soft delete is off,
// and Delete Share destroys a share outright.
func filesSoftDeleteRetentionDays(account string) int {
	if azStorageAccounts == nil || azStorageServiceProps == nil {
		return 0
	}
	for _, acct := range azStorageAccounts.List() {
		if acct.Name != account {
			continue
		}
		props, ok := azStorageServiceProps.Get(acct.ID + "/fileServices")
		if !ok {
			return 0
		}
		policy, ok := props["shareDeleteRetentionPolicy"].(map[string]any)
		if !ok {
			return 0
		}
		if enabled, ok := policy["enabled"].(bool); !ok || !enabled {
			return 0
		}
		days, _ := policy["days"].(float64)
		if days <= 0 {
			// Azure's default retention when a policy is enabled without a
			// day count.
			return 7
		}
		return int(days)
	}
	return 0
}

// filesSoftDeleteShare moves a deleted share's contents into the account's
// deleted-share root and records it so Restore Share can put it back.
func filesSoftDeleteShare(account, share string, data FileShareData) error {
	version := strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	target := fileDeletedShareHostDir(account, share, version)
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return err
	}
	if err := os.Rename(FileShareHostDir(account, share), target); err != nil {
		return err
	}
	fileDeletedShares.Put(fileDeletedShareKey(account, share, version), FileDeletedShareData{
		Account: account, Share: share, Version: version,
		Deleted:  time.Now().UTC().Format(http.TimeFormat),
		Quota:    data.Quota,
		Metadata: data.Metadata,
		ACLs:     data.ACLs,
	})
	return nil
}
