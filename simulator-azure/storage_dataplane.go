package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure Storage Files / Queues / Tables data planes.
//
// The Microsoft.Storage ARM PUT response advertises four data-plane
// endpoint URLs on every storage-account create:
//
//	https://{account}.blob.<host>/ (part 1 in blob.go)
//	https://{account}.file.<host>/   ← this file
//	https://{account}.queue.<host>/  ← this file
//	https://{account}.table.<host>/  ← this file
//
// Real Azure SDK / azcopy / az CLI consumers follow these URLs;
// before this commit the latter three 404'd (the sim emitted the
// URLs but had no handler servicing them — exact shape).
// Each data plane is scope-tight to the canonical
// CRUD that terraform-provider-azurerm + the Go SDK exercise; full
// REST surfaces (ranges, leases, SAS, multipart copy, full OData
// query) are out of scope for the first cut.
//
// Wire spec references:
//   Files:  https://learn.microsoft.com/rest/api/storageservices/file-service-rest-api
//   Queues: https://learn.microsoft.com/rest/api/storageservices/queue-service-rest-api
//   Tables: https://learn.microsoft.com/rest/api/storageservices/table-service-rest-api

// ── Files data plane ────────────────────────────────────────────────

// FileShareData is a share's data-plane projection. Distinct from
// the ARM FileShare type (in files.go) which stores ARM-control-plane
// metadata. The data plane stores per-share metadata; the share's files live in
// the share's backing directory (FileShareHostDir).
type FileShareData struct {
	Account    string
	Name       string
	Quota      int // GiB
	AccessTier string
	RootSquash string
	Metadata   map[string]string
	Created    string
	ETag       string
	ACLs       []TableSignedIdentifier
}

// FileObject carries the properties of one file in a share that a filesystem
// cannot express — its content type and its user-defined metadata. The file's
// existence, size, contents and modification time are the filesystem's, read
// from the share's backing directory, because that directory is what a
// Container Apps workload mounting the share sees: a file written through this
// data plane is readable inside the container, and a file the container writes
// is readable through this data plane.
type FileObject struct {
	Account     string
	Share       string
	Path        string // forward-slash separated; "" = share root
	ContentType string
	Metadata    map[string]string
}

var (
	fileShareData sim.Store[FileShareData]
	fileObjects   sim.Store[FileObject]
)

// ── Queues data plane ───────────────────────────────────────────────

type QueueData struct {
	Account  string
	Name     string
	Created  string
	Metadata map[string]string
	Messages []QueueMessage
	ACLs     []TableSignedIdentifier
}

type QueueMessage struct {
	MessageID      string
	MessageText    string // base64 (per real Azure spec) or raw
	InsertionTime  string
	ExpirationTime string
	PopReceipt     string
	VisibleAt      int64 // Unix seconds; >now → in-flight
	DequeueCount   int
}

var queueData sim.Store[QueueData]

// ── Tables data plane ───────────────────────────────────────────────

type TableData struct {
	Account string
	Name    string
	Created string
	ACLs    []TableSignedIdentifier
}

type TableSignedIdentifiers struct {
	XMLName xml.Name                `xml:"SignedIdentifiers"`
	Items   []TableSignedIdentifier `xml:"SignedIdentifier"`
}

type TableSignedIdentifier struct {
	ID           string            `json:"id" xml:"Id"`
	AccessPolicy TableAccessPolicy `json:"accessPolicy" xml:"AccessPolicy"`
}

type TableAccessPolicy struct {
	Start      string `json:"startTime,omitempty" xml:"Start,omitempty"`
	Expiry     string `json:"expiryTime,omitempty" xml:"Expiry,omitempty"`
	Permission string `json:"permission,omitempty" xml:"Permission,omitempty"`
}

// TableEntity stores arbitrary OData properties keyed by name. Real
// Azure Tables types each property; the sim treats every value as
// json.RawMessage so round-trip is byte-exact.
type TableEntity struct {
	Account      string
	Table        string
	PartitionKey string
	RowKey       string
	Properties   map[string]json.RawMessage
	ETag         string
	Timestamp    string
}

var (
	tableData     sim.Store[TableData]
	tableEntities sim.Store[TableEntity]
)

// ── Dispatcher ──────────────────────────────────────────────────────

// registerStorageDataPlane wraps the server handler so requests
// arriving with a `<account>.{file,queue,table}.<host>` Host header
// reach the matching per-service handler. The blob data plane (also
// host-dispatched) is wired separately in blob.go::registerBlobDataPlane;
// the two WrapHandlers stack but only the one whose suffix matches
// dispatches.
func registerStorageDataPlane(srv *sim.Server) {
	fileShareData = sim.MakeStore[FileShareData](srv.DB(), "file_share_data")
	fileObjects = sim.MakeStore[FileObject](srv.DB(), "file_objects")
	queueData = sim.MakeStore[QueueData](srv.DB(), "queue_data")
	tableData = sim.MakeStore[TableData](srv.DB(), "table_data")
	tableEntities = sim.MakeStore[TableEntity](srv.DB(), "table_entities")
	registerFilesDataPlaneStores(srv)
	registerQueuesDataPlaneStores(srv)

	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if i := strings.LastIndex(host, ":"); i >= 0 {
				host = host[:i]
			}
			if parts := strings.SplitN(host, ".file.", 2); len(parts) == 2 {
				handleFilesDataPlane(w, r, parts[0])
				return
			}
			if parts := strings.SplitN(host, ".queue.", 2); len(parts) == 2 {
				handleQueuesDataPlane(w, r, parts[0])
				return
			}
			if parts := strings.SplitN(host, ".table.", 2); len(parts) == 2 {
				handleTablesDataPlane(w, r, parts[0])
				return
			}
			// Azure Storage static website (`{account}.web.…`) and Azure Data
			// Lake Storage Gen2 (`{account}.dfs.…`) are two more data planes of
			// the same storage account, and the ARM storage-account response
			// advertises both endpoints because real Azure serves them. The
			// simulator implements neither, so every request that arrives at
			// those hostnames is answered with the same declared gap the served
			// planes use for an operation they do not implement — a storage
			// error a real SDK surfaces as a typed failure, never a bare 200.
			if strings.Contains(host, ".web.") {
				writeStorageOperationNotImplemented(w, r, "static website")
				return
			}
			if strings.Contains(host, ".dfs.") {
				writeStorageOperationNotImplemented(w, r, "Data Lake Storage Gen2")
				return
			}
			// Path-style fallback for SDKs configured with a non-
			// `*.core.windows.net` endpoint (Azurite-compatible).
			// Sockerless runs on a single port, so the service is
			// discriminated by a path prefix instead of a per-service
			// port:
			//   /file/{account}/...   → Files data plane
			//   /queue/{account}/...  → Queues data plane
			//   /table/{account}/...  → Tables data plane
			// Connection-string contract: callers configure
			// `FileEndpoint=http://localhost:14568/file/<account>`.
			// Bare `/{account}/...` (blob default) is matched in
			// blob.go's WrapHandler.
			//
			// Skip when the host carries a non-storage Azure
			// subdomain (`.vault.`, `.servicebus.`) — those hosts
			// belong to other data planes and must not be reinterpreted
			// as storage path-style requests.
			if hasNonStorageAzureSubdomain(host) {
				next.ServeHTTP(w, r)
				return
			}
			// Same protocol-signal discriminator as the blob
			// path-style fallback in blob.go — keeps non-storage
			// callers that happen to use `/file/`, `/queue/`, or
			// `/table/` as a path prefix routed to their own
			// handlers.
			if !hasAzureStorageSignal(r) {
				next.ServeHTTP(w, r)
				return
			}
			if account, rest, ok := splitServicePrefix(r.URL.Path, "file"); ok {
				container, _ := storagePathResource(rest)
				if !AuthorizeStorageDataPlane(w, r, "file", account, container, "") {
					return
				}
				r = StorageMarkAuthorized(r)
				r.URL.Path = "/" + rest
				handleFilesDataPlane(w, r, account)
				return
			}
			if account, rest, ok := splitServicePrefix(r.URL.Path, "queue"); ok {
				container, _ := storagePathResource(rest)
				if !AuthorizeStorageDataPlane(w, r, "queue", account, container, "") {
					return
				}
				r = StorageMarkAuthorized(r)
				r.URL.Path = "/" + rest
				handleQueuesDataPlane(w, r, account)
				return
			}
			if account, rest, ok := splitServicePrefix(r.URL.Path, "table"); ok {
				container, _ := storagePathResource(rest)
				if !AuthorizeStorageDataPlane(w, r, "table", account, container, "") {
					return
				}
				r = StorageMarkAuthorized(r)
				r.URL.Path = "/" + rest
				handleTablesDataPlane(w, r, account)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

// splitServicePrefix matches `/<service>/<account>/<rest...>` where
// `<service>` is the literal `service` argument (file / queue / table).
// Returns (account, rest-of-path, true) on match. Account names are
// accepted as-is — real Azurite is permissive — and the service-prefix
// guarantees the route is intended for storage. Path-style sibling of
// the bare `/{account}/...` blob form in blob.go.
func splitServicePrefix(path, service string) (account, rest string, ok bool) {
	p := strings.TrimPrefix(path, "/")
	prefix := service + "/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", false
	}
	p = p[len(prefix):]
	slash := strings.IndexByte(p, '/')
	var first string
	if slash < 0 {
		first = p
		p = ""
	} else {
		first = p[:slash]
		p = p[slash+1:]
	}
	if first == "" {
		return "", "", false
	}
	return first, p, true
}

// ── Files dispatch ──────────────────────────────────────────────────

func fileShareKey(account, share string) string { return account + "/" + share }
func fileObjectKey(account, share, p string) string {
	return account + "/" + share + "/" + p
}

// handleFilesDataPlane dispatches one Files data-plane request. As on the Blob
// plane the operation comes from the `restype` + `comp` query pair rather than
// from the path, so the dispatcher resolves that pair explicitly: a value it
// does not recognize is answered with a declared gap, never handed to whichever
// sibling handler happens to sit under the same method. Without that,
// `PUT /{share}/{dir}?restype=directory` (Create Directory) would land on
// Create File and report 201 for a directory that was never created.
//
// Three levels are addressed:
//
//	/                      the File service     (list shares, service properties, delegation key)
//	/{share}?restype=share the share itself     (CRUD, lease, snapshot, permissions, ACL, stats, restore)
//	/{share}/{path...}     an entry in a share  (directories and files at any depth, plus links and handles)
func handleFilesDataPlane(w http.ResponseWriter, r *http.Request, account string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	q := r.URL.Query()
	restype, comp := q.Get("restype"), q.Get("comp")

	// Every request into this plane carries a credential the account's keys
	// signed, or reaches a resource whose public access level serves it. The
	// Blob, Files and Queues planes share one authorization — see
	// blob_authorization.go — because they share one set of account keys and one
	// Shared Key scheme.
	if !AuthorizeStorageDataPlane(w, r, "file", account, storageFirstSegment(path), "") {
		return
	}

	// Service level.
	if path == "" {
		switch {
		case r.Method == http.MethodGet && restype == "" && comp == "list":
			handleFilesListShares(w, r, account)
		case (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			restype == "service" && comp == "properties":
			// The azurerm provider polls a service's properties while waiting
			// for a storage account's data plane.
			handleFilesGetServiceProperties(w, r, account)
		case r.Method == http.MethodPut && restype == "service" && comp == "properties":
			handleFilesSetServiceProperties(w, r, account)
		case r.Method == http.MethodPost && restype == "service" && comp == "userdelegationkey":
			if !RequireStorageOAuthBearer(w, r) {
				return
			}
			handleFilesGetUserDelegationKey(w, r, account)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
		return
	}

	share, entryPath, _ := strings.Cut(path, "/")
	entryPath = strings.Trim(entryPath, "/")
	if share == "" {
		writeStorageError(w, "InvalidUri",
			"The requested URI does not represent any resource on the server.", http.StatusBadRequest)
		return
	}
	if entryPath == "" && restype == "share" {
		handleFilesShareOperation(w, r, account, share, comp)
		return
	}
	handleFilesEntryOperation(w, r, account, share, entryPath, restype, comp)
}

func handleFilesShareACL(w http.ResponseWriter, r *http.Request, account, share string) {
	key := fileShareKey(account, share)
	data, ok := fileShareData.Get(key)
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("ETag", data.ETag)
		w.Header().Set("Last-Modified", data.Created)
		if err := xml.NewEncoder(w).Encode(TableSignedIdentifiers{Items: data.ACLs}); err != nil {
			sim.AzureError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		}
	case http.MethodPut:
		if !filesRequireLease(w, r, account, share, "", "share") {
			return
		}
		var body TableSignedIdentifiers
		if err := xml.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeStorageError(w, "InvalidXmlDocument", "The XML specified is not syntactically valid.", http.StatusBadRequest)
			return
		}
		if len(body.Items) > 5 {
			writeStorageError(w, "InvalidXmlDocument", "A share can contain at most five stored access policies.", http.StatusBadRequest)
			return
		}
		data.ACLs = body.Items
		data.ETag = `"` + generateUUID() + `"`
		data.Created = time.Now().UTC().Format(http.TimeFormat)
		fileShareData.Put(key, data)
		updateFileShareARMAccessPolicies(account, share, body.Items)
		w.Header().Set("ETag", data.ETag)
		w.Header().Set("Last-Modified", data.Created)
		w.WriteHeader(http.StatusOK)
	default:
		writeStorageError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
	}
}

func handleFilesCreateShare(w http.ResponseWriter, r *http.Request, account, share string) {
	key := fileShareKey(account, share)
	if _, ok := fileShareData.Get(key); ok {
		writeStorageError(w, "ShareAlreadyExists", "The specified share already exists.", http.StatusConflict)
		return
	}
	if !isStoragePathSegment(account) || !isStoragePathSegment(share) {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	quota := 5120
	if raw := r.Header.Get("x-ms-share-quota"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-share-quota.",
				http.StatusBadRequest)
			return
		}
		quota = n
	}
	// A share that has just been created holds no files, so its backing
	// directory starts empty whatever an earlier share of the same name left in
	// it — the share metadata lives in this process, the directory outlives it.
	if err := resetFileShareHostDir(account, share); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	accessTier := r.Header.Get("x-ms-access-tier")
	if accessTier == "" {
		accessTier = "TransactionOptimized"
	}
	s := FileShareData{
		Account: account, Name: share, Quota: quota,
		AccessTier: accessTier,
		RootSquash: r.Header.Get("x-ms-root-squash"),
		Metadata:   collectMetadata(r),
		Created:    time.Now().UTC().Format(http.TimeFormat),
		ETag:       `"` + generateUUID() + `"`,
	}
	fileShareData.Put(key, s)
	upsertFileShareARMProjection(account, share, s.Quota, s.Metadata)
	w.Header().Set("Last-Modified", s.Created)
	w.Header().Set("ETag", s.ETag)
	w.WriteHeader(http.StatusCreated)
}

// handleFilesDeleteShare is Delete Share. A single snapshot is deleted when the
// request names one; otherwise the share goes, and with it its files,
// snapshots, permissions and leases. Where the account's File service carries a
// share delete-retention policy the share is soft-deleted instead, so Restore
// Share can bring it back with its contents intact.
func handleFilesDeleteShare(w http.ResponseWriter, r *http.Request, account, share string) {
	key := fileShareKey(account, share)
	data, ok := fileShareData.Get(key)
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if snapshot := r.URL.Query().Get("sharesnapshot"); snapshot != "" {
		snapKey := fileShareSnapshotKey(account, share, snapshot)
		if _, ok := fileShareSnapshots.Get(snapKey); !ok {
			writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
			return
		}
		if err := os.RemoveAll(fileShareSnapshotHostDir(account, share, snapshot)); err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		fileShareSnapshots.Delete(snapKey)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if !filesRequireLease(w, r, account, share, "", "share") {
		return
	}
	fileShareData.Delete(key)
	deleteFileObjectProperties(account, share)
	if err := filesDeleteShareContents(account, share); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if filesSoftDeleteRetentionDays(account) > 0 {
		if err := filesSoftDeleteShare(account, share, data); err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	} else if err := removeFileShareHostDir(account, share); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	deleteFileShareARMProjection(account, share)
	w.WriteHeader(http.StatusAccepted)
}

func handleFilesGetShareProperties(w http.ResponseWriter, r *http.Request, account, share string) {
	s, ok := fileShareData.Get(fileShareKey(account, share))
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if snapshot := r.URL.Query().Get("sharesnapshot"); snapshot != "" {
		snap, ok := fileShareSnapshots.Get(fileShareSnapshotKey(account, share, snapshot))
		if !ok {
			writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
			return
		}
		s.Quota, s.Metadata, s.Created, s.ETag = snap.Quota, snap.Metadata, snap.Created, snap.ETag
	}
	lease, held := filesActiveLease(account, share, "")
	w.Header().Set("Last-Modified", s.Created)
	w.Header().Set("ETag", s.ETag)
	w.Header().Set("x-ms-share-quota", fmt.Sprintf("%d", s.Quota))
	if s.AccessTier != "" {
		w.Header().Set("x-ms-access-tier", s.AccessTier)
	}
	if s.RootSquash != "" {
		w.Header().Set("x-ms-root-squash", s.RootSquash)
	}
	w.Header().Set("x-ms-lease-state", filesLeaseState(lease, held))
	if held {
		w.Header().Set("x-ms-lease-status", "locked")
		if lease.Duration > 0 {
			w.Header().Set("x-ms-lease-duration", "fixed")
		} else {
			w.Header().Set("x-ms-lease-duration", "infinite")
		}
	} else {
		w.Header().Set("x-ms-lease-status", "unlocked")
	}
	for k, v := range s.Metadata {
		w.Header().Set("x-ms-meta-"+k, v)
	}
	w.WriteHeader(http.StatusOK)
}

// handleFilesListShares is List Shares. It enumerates the account's shares, and
// their snapshots when `include=snapshots` asks for them, paging the
// enumeration the way the service does: `prefix` filters, `marker` resumes
// after the last entry the previous page returned, and `maxresults` bounds the
// page.
func handleFilesListShares(w http.ResponseWriter, r *http.Request, account string) {
	type shareProperties struct {
		LastModified string `xml:"Last-Modified"`
		ETag         string `xml:"Etag"`
		Quota        int    `xml:"Quota"`
		AccessTier   string `xml:"AccessTier,omitempty"`
	}
	type shareEntry struct {
		Name       string          `xml:"Name"`
		Snapshot   string          `xml:"Snapshot,omitempty"`
		Deleted    bool            `xml:"Deleted,omitempty"`
		Version    string          `xml:"Version,omitempty"`
		Properties shareProperties `xml:"Properties"`
	}
	type enum struct {
		XMLName         xml.Name     `xml:"EnumerationResults"`
		ServiceEndpoint string       `xml:"ServiceEndpoint,attr"`
		Prefix          string       `xml:"Prefix,omitempty"`
		Marker          string       `xml:"Marker,omitempty"`
		MaxResults      *int32       `xml:"MaxResults,omitempty"`
		Shares          []shareEntry `xml:"Shares>Share"`
		NextMarker      string       `xml:"NextMarker"`
	}
	q := r.URL.Query()
	prefix, marker := q.Get("prefix"), q.Get("marker")
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
	includeSnapshots, includeDeleted := false, false
	for _, include := range q["include"] {
		for _, value := range strings.Split(include, ",") {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "snapshots":
				includeSnapshots = true
			case "deleted":
				includeDeleted = true
			}
		}
	}

	// One entry per share, plus one per snapshot when asked for, ordered the
	// way the service enumerates them: by name, and within a name by snapshot.
	var candidates []shareEntry
	for _, s := range fileShareData.List() {
		if s.Account != account {
			continue
		}
		candidates = append(candidates, shareEntry{
			Name: s.Name,
			Properties: shareProperties{
				LastModified: s.Created,
				ETag:         s.ETag,
				Quota:        s.Quota,
				AccessTier:   s.AccessTier,
			},
		})
		if !includeSnapshots {
			continue
		}
		for _, snap := range filesShareSnapshotsFor(account, s.Name) {
			candidates = append(candidates, shareEntry{
				Name:     s.Name,
				Snapshot: snap.Snapshot,
				Properties: shareProperties{
					LastModified: snap.Created,
					ETag:         snap.ETag,
					Quota:        snap.Quota,
				},
			})
		}
	}
	if includeDeleted {
		for _, deleted := range fileDeletedSharesUnder(account + "/") {
			candidates = append(candidates, shareEntry{
				Name:    deleted.Share,
				Deleted: true,
				Version: deleted.Version,
				Properties: shareProperties{
					LastModified: deleted.Deleted,
					Quota:        deleted.Quota,
				},
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		if candidates[i].Snapshot != candidates[j].Snapshot {
			return candidates[i].Snapshot < candidates[j].Snapshot
		}
		return candidates[i].Version < candidates[j].Version
	})

	out := enum{
		ServiceEndpoint: azureStorageEndpointURL(r, account, "file"),
		Prefix:          prefix,
		Marker:          marker,
	}
	if maxResults > 0 {
		n := int32(maxResults)
		out.MaxResults = &n
	}
	for _, entry := range candidates {
		token := entry.Name + "/" + entry.Snapshot + "/" + entry.Version
		if prefix != "" && !strings.HasPrefix(entry.Name, prefix) {
			continue
		}
		if marker != "" && token <= marker {
			continue
		}
		if maxResults > 0 && len(out.Shares) == maxResults {
			out.NextMarker = marker
			break
		}
		out.Shares = append(out.Shares, entry)
		marker = token
	}
	writeStorageXML(w, http.StatusOK, out)
}

// handleFilesCreateFile is Create File: it allocates a file of the size the
// REQUIRED x-ms-content-length header declares, zero-filled, and discards any
// request body — that is what the operation does on real Azure Files, where
// content arrives afterwards through Upload Range. A file that already exists
// is replaced.
func handleFilesCreateFile(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	raw := r.Header.Get("x-ms-content-length")
	if raw == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-content-length.",
			http.StatusBadRequest)
		return
	}
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || size < 0 {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-content-length.",
			http.StatusBadRequest)
		return
	}
	hostPath, ok := fileShareHostPath(account, share, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	// Azure Files creates a file only inside a directory that already exists;
	// Create Directory is the operation that makes one.
	if parent, err := os.Stat(filepath.Dir(hostPath)); err != nil || !parent.IsDir() {
		if err != nil && !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		writeStorageError(w, "ParentNotFound",
			"The specified parent path does not exist.", http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	f, err := os.OpenFile(hostPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		writeStorageError(w, "ResourceAlreadyExists",
			"The specified resource already exists.", http.StatusConflict)
		return
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	// A real Azure Files share is mounted with CIFS file_mode 0777, so a
	// non-root workload can write a file the data plane created; chmod past the
	// process umask so the materialized file matches.
	if err := os.Chmod(hostPath, 0o666); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	info, err := f.Stat()
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	contentType := r.Header.Get("x-ms-content-type")
	if contentType == "" {
		contentType = r.Header.Get("Content-Type")
	}
	fileObjects.Put(fileObjectKey(account, share, filePath), FileObject{
		Account: account, Share: share, Path: filePath,
		ContentType: contentType,
		Metadata:    collectMetadata(r),
	})
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusCreated)
}

// handleFilesUploadRange is Upload Range: it writes the request body into the
// byte range the x-ms-range (or Range) header names, or zeroes that range when
// x-ms-write is "clear". The range must lie inside the file the preceding
// Create File allocated — Azure Files never grows a file through Upload Range.
func handleFilesUploadRange(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	hostPath, info, _, ok := statShareFile(w, account, share, filePath)
	if !ok {
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	rangeHeader := r.Header.Get("x-ms-range")
	if rangeHeader == "" {
		rangeHeader = r.Header.Get("Range")
	}
	if rangeHeader == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-range.",
			http.StatusBadRequest)
		return
	}
	start, end, ok := parseAzureFileRange(rangeHeader)
	if !ok {
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
	length := end - start + 1

	defer r.Body.Close()
	clear := strings.EqualFold(r.Header.Get("x-ms-write"), "clear")
	var data []byte
	if !clear {
		// "update" is the default write mode: the body supplies the bytes.
		body, err := openStreamingBody(r)
		if err != nil {
			writeStorageError(w, "UnsupportedHeader", err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		data, err = io.ReadAll(body)
		if err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		if int64(len(data)) != length {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: Content-Length does not match x-ms-range.",
				http.StatusBadRequest)
			return
		}
	}

	f, err := os.OpenFile(hostPath, os.O_RDWR, 0)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if clear {
		if err := filesClearFileRange(f, info, start, length); err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	} else if _, err := f.WriteAt(data, start); err != nil {
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
	w.WriteHeader(http.StatusCreated)
}

// parseAzureFileRange parses the "bytes=start-end" form Azure Files requires on
// x-ms-range / Range. Both bounds are mandatory and inclusive — the open-ended
// "bytes=start-" spelling is not valid for Upload Range.
func parseAzureFileRange(raw string) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(raw), "bytes=")
	if !found {
		return 0, 0, false
	}
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(lo), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err = strconv.ParseInt(strings.TrimSpace(hi), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if start < 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// statShareFile resolves one file in a share to its on-disk path, the
// filesystem's record of it, and the properties only this data plane knows
// (content type, user metadata). It writes the Azure Storage error and reports
// false when the share, the name or the file itself does not hold up.
func statShareFile(w http.ResponseWriter, account, share, filePath string) (string, os.FileInfo, FileObject, bool) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return "", nil, FileObject{}, false
	}
	hostPath, ok := fileShareHostPath(account, share, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return "", nil, FileObject{}, false
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		if !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return "", nil, FileObject{}, false
		}
		writeStorageError(w, "ResourceNotFound", "The specified file does not exist.", http.StatusNotFound)
		return "", nil, FileObject{}, false
	}
	// A directory is a different resource type, addressed with
	// restype=directory; it is not a file this operation can act on.
	if info.IsDir() {
		writeStorageError(w, "ResourceNotFound", "The specified file does not exist.", http.StatusNotFound)
		return "", nil, FileObject{}, false
	}
	props, _ := fileObjects.Get(fileObjectKey(account, share, filePath))
	return hostPath, info, props, true
}

// statShareFileForRead resolves one file the way a read addresses it: in the
// share itself, or in the copy a snapshot froze when the request carries
// `?sharesnapshot=`.
func statShareFileForRead(w http.ResponseWriter, r *http.Request, account, share, filePath string) (string, os.FileInfo, FileObject, bool) {
	if r.URL.Query().Get("sharesnapshot") == "" {
		return statShareFile(w, account, share, filePath)
	}
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return "", nil, FileObject{}, false
	}
	hostPath, ok := filesEntryHostPath(root, filePath)
	if !ok {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return "", nil, FileObject{}, false
	}
	info, err := os.Stat(hostPath)
	if err != nil || info.IsDir() {
		if err != nil && !os.IsNotExist(err) {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return "", nil, FileObject{}, false
		}
		writeStorageError(w, "ResourceNotFound", "The specified file does not exist.", http.StatusNotFound)
		return "", nil, FileObject{}, false
	}
	props, _ := fileObjects.Get(fileObjectKey(account, share, filePath))
	return hostPath, info, props, true
}

func handleFilesGetFile(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	hostPath, info, props, ok := statShareFileForRead(w, r, account, share, filePath)
	if !ok {
		return
	}
	start, end, partial, ok := azureStorageReadRange(w, r, info.Size())
	if !ok {
		return // azureStorageReadRange has written the error.
	}
	f, err := os.Open(hostPath)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	writeFileHeaders(w, props, info)
	if !partial {
		_, _ = io.Copy(w, f)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, info.Size()))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.Copy(w, io.NewSectionReader(f, start, end-start+1))
}

func handleFilesHeadFile(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	_, info, props, ok := statShareFileForRead(w, r, account, share, filePath)
	if !ok {
		return
	}
	writeFileHeaders(w, props, info)
	lease, held := filesActiveLease(account, share, filePath)
	w.Header().Set("x-ms-lease-state", filesLeaseState(lease, held))
	if held {
		w.Header().Set("x-ms-lease-status", "locked")
	} else {
		w.Header().Set("x-ms-lease-status", "unlocked")
	}
	writeFileSMBHeaders(w, share, filePath, info, "", "")
}

func handleFilesDeleteFile(w http.ResponseWriter, r *http.Request, account, share, filePath string) {
	hostPath, _, _, ok := statShareFile(w, account, share, filePath)
	if !ok {
		return
	}
	if !filesRequireLease(w, r, account, share, filePath, "file") {
		return
	}
	fileLeases.Delete(fileLeaseKey(account, share, filePath))
	if err := os.Remove(hostPath); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	fileObjects.Delete(fileObjectKey(account, share, filePath))
	w.WriteHeader(http.StatusAccepted)
}

// deleteFileObjectProperties drops the properties rows of every file in a share
// that no longer exists.
func deleteFileObjectProperties(account, share string) {
	for _, f := range fileObjectsUnder(filesSharePrefix(account, share)) {
		fileObjects.Delete(fileObjectKey(f.Account, f.Share, f.Path))
	}
}

// fileETag derives a file's entity tag from what the filesystem records about
// it, so the tag moves whenever the bytes do — including on a write from a
// container that mounts the share, which no store in this process witnesses.
func fileETag(info os.FileInfo) string {
	return fmt.Sprintf(`"0x%X%X"`, info.ModTime().UTC().UnixNano(), info.Size())
}

func writeFileHeaders(w http.ResponseWriter, props FileObject, info os.FileInfo) {
	contentType := props.ContentType
	if contentType == "" {
		// Azure Files reports this for a file whose content type was never set,
		// which is every file a mounting container writes.
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", fileETag(info))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("x-ms-type", "File")
	for k, v := range props.Metadata {
		w.Header().Set("x-ms-meta-"+k, v)
	}
}

// ── Queues dispatch ─────────────────────────────────────────────────

func queueKey(account, queue string) string { return account + "/" + queue }

// handleQueuesDataPlane dispatches one Queues data-plane request. The served
// set:
//
//	GET    /?comp=list                             ListQueues
//	GET    /?restype=service&comp=properties        GetServiceProperties
//	HEAD   /?restype=service&comp=properties        GetServiceProperties
//	PUT    /{queue}                                 CreateQueue
//	DELETE /{queue}                                 DeleteQueue
//	GET    /{queue}?comp=metadata                   GetQueueProperties
//	HEAD   /{queue}?comp=metadata                   GetQueueProperties
//	PUT    /{queue}?comp=metadata                   SetQueueMetadata
//	POST   /{queue}/messages                        PutMessage
//	GET    /{queue}/messages                        GetMessages
//	GET    /{queue}/messages?peekonly=true          PeekMessages
//	DELETE /{queue}/messages                        ClearMessages
//	PUT    /{queue}/messages/{messageid}            UpdateMessage
//	DELETE /{queue}/messages/{messageid}            DeleteMessage
//	GET    /{queue}?comp=acl                        GetQueueAccessPolicy
//	PUT    /{queue}?comp=acl                        SetQueueAccessPolicy
//	PUT    /?restype=service&comp=properties        SetServiceProperties
//	GET    /?restype=service&comp=stats             GetServiceStatistics
func handleQueuesDataPlane(w http.ResponseWriter, r *http.Request, account string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	q := r.URL.Query()
	restype, comp := q.Get("restype"), q.Get("comp")

	// Every request into this plane carries a credential the account's keys
	// signed, or reaches a resource whose public access level serves it. The
	// Blob, Files and Queues planes share one authorization — see
	// blob_authorization.go — because they share one set of account keys and one
	// Shared Key scheme.
	if !AuthorizeStorageDataPlane(w, r, "queue", account, storageFirstSegment(path), "") {
		return
	}

	// Service level: /?comp=…
	if path == "" {
		switch {
		case r.Method == http.MethodGet && restype == "" && comp == "list":
			handleQueuesList(w, r, account)
		case (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			restype == "service" && comp == "properties":
			handleQueuesGetServiceProperties(w, r, account)
		case r.Method == http.MethodPut && restype == "service" && comp == "properties":
			handleQueuesSetServiceProperties(w, r, account)
		case r.Method == http.MethodGet && restype == "service" && comp == "stats":
			handleQueuesGetServiceStatistics(w, r, account)
		default:
			writeStorageOperationNotImplemented(w, r, "Queues")
		}
		return
	}

	segs := strings.Split(path, "/")
	queue := segs[0]
	if queue == "" {
		writeStorageError(w, "InvalidUri", "Unrecognized Queues data-plane path", http.StatusBadRequest)
		return
	}

	if len(segs) == 1 {
		if comp == "acl" {
			handleQueueACL(w, r, account, queue)
			return
		}
		switch r.Method {
		case http.MethodPut:
			switch comp {
			case "":
				handleQueueCreate(w, r, account, queue)
			case "metadata":
				handleQueueSetMetadata(w, r, account, queue)
			default:
				writeStorageOperationNotImplemented(w, r, "Queues")
			}
		case http.MethodDelete:
			if comp != "" {
				writeStorageOperationNotImplemented(w, r, "Queues")
				return
			}
			handleQueueDelete(w, r, account, queue)
		case http.MethodGet, http.MethodHead:
			// Get Queue Metadata is the only documented read on the queue
			// itself besides the access policy, and Azure addresses it with
			// comp=metadata.
			if comp != "metadata" {
				writeStorageOperationNotImplemented(w, r, "Queues")
				return
			}
			handleQueueGetMetadata(w, r, account, queue)
		default:
			writeStorageOperationNotImplemented(w, r, "Queues")
		}
		return
	}

	// Messages: /{queue}/messages or /{queue}/messages/{messageid}. No
	// documented message operation carries restype or comp.
	if segs[1] != "messages" {
		writeStorageError(w, "InvalidUri", "Unrecognized Queues data-plane path", http.StatusBadRequest)
		return
	}
	if restype != "" || comp != "" {
		writeStorageOperationNotImplemented(w, r, "Queues")
		return
	}
	if len(segs) == 2 {
		switch r.Method {
		case http.MethodPost:
			handleQueuePutMessage(w, r, account, queue)
		case http.MethodGet:
			if q.Get("peekonly") == "true" {
				handleQueuePeekMessages(w, r, account, queue)
			} else {
				handleQueueGetMessages(w, r, account, queue)
			}
		case http.MethodDelete:
			handleQueueClearMessages(w, r, account, queue)
		default:
			writeStorageOperationNotImplemented(w, r, "Queues")
		}
		return
	}
	if len(segs) == 3 {
		messageID := segs[2]
		switch r.Method {
		case http.MethodDelete:
			handleQueueDeleteMessage(w, r, account, queue, messageID)
		case http.MethodPut:
			handleQueueUpdateMessage(w, r, account, queue, messageID)
		default:
			writeStorageOperationNotImplemented(w, r, "Queues")
		}
		return
	}
	writeStorageError(w, "InvalidUri", "Unrecognized Queues data-plane path", http.StatusBadRequest)
}

func handleQueueCreate(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); ok {
		// Real Azure: 204 No Content if queue exists with same metadata.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := QueueData{
		Account: account, Name: queue,
		Created:  time.Now().UTC().Format(time.RFC1123),
		Metadata: collectMetadata(r),
	}
	queueData.Put(key, q)
	w.WriteHeader(http.StatusCreated)
}

// handleQueueSetMetadata is Set Queue Metadata: the x-ms-meta-* headers replace
// the queue's metadata wholesale, and Get Queue Metadata reads back exactly what
// was set.
func handleQueueSetMetadata(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	metadata := collectMetadata(r)
	queueData.Update(key, func(q *QueueData) {
		q.Metadata = metadata
	})
	w.WriteHeader(http.StatusNoContent)
}

func handleQueueDelete(w http.ResponseWriter, r *http.Request, account, queue string) {
	if !queueData.Delete(queueKey(account, queue)) {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleQueueGetMetadata(w http.ResponseWriter, r *http.Request, account, queue string) {
	q, ok := queueData.Get(queueKey(account, queue))
	if !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	for k, v := range q.Metadata {
		w.Header().Set("x-ms-meta-"+k, v)
	}
	visible := 0
	now := time.Now().Unix()
	for _, m := range q.Messages {
		if m.VisibleAt <= now {
			visible++
		}
	}
	w.Header().Set("x-ms-approximate-messages-count", fmt.Sprintf("%d", visible))
	w.WriteHeader(http.StatusOK)
}

func handleQueuesList(w http.ResponseWriter, r *http.Request, account string) {
	type qEntry struct {
		Name string `xml:"Name"`
	}
	type enum struct {
		XMLName xml.Name `xml:"EnumerationResults"`
		Queues  []qEntry `xml:"Queues>Queue"`
	}
	out := enum{}
	prefix := account + "/"
	for _, q := range queueData.List() {
		if strings.HasPrefix(queueKey(q.Account, q.Name), prefix) {
			out.Queues = append(out.Queues, qEntry{Name: q.Name})
		}
	}
	writeStorageXML(w, http.StatusOK, out)
}

// QueueMessageRequest is the XML request body for Put Message.
type QueueMessageRequest struct {
	XMLName     xml.Name `xml:"QueueMessage"`
	MessageText string   `xml:"MessageText"`
}

// QueueMessageResponse is the XML response shape for Get / Peek.
type QueueMessageResponse struct {
	XMLName         xml.Name `xml:"QueueMessage"`
	MessageID       string   `xml:"MessageId,omitempty"`
	InsertionTime   string   `xml:"InsertionTime,omitempty"`
	ExpirationTime  string   `xml:"ExpirationTime,omitempty"`
	PopReceipt      string   `xml:"PopReceipt,omitempty"`
	TimeNextVisible string   `xml:"TimeNextVisible,omitempty"`
	DequeueCount    int      `xml:"DequeueCount,omitempty"`
	MessageText     string   `xml:"MessageText"`
}

func handleQueuePutMessage(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "RequestBodyInvalid",
			"Failed to read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req QueueMessageRequest
	if err := xml.Unmarshal(data, &req); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"The specified XML is not syntactically valid: "+err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now()
	msg := QueueMessage{
		MessageID:      generateUUID(),
		MessageText:    req.MessageText,
		InsertionTime:  now.UTC().Format(time.RFC1123),
		ExpirationTime: now.Add(7 * 24 * time.Hour).UTC().Format(time.RFC1123),
	}
	queueData.Update(key, func(q *QueueData) {
		q.Messages = append(q.Messages, msg)
	})
	resp := QueueMessageResponse{
		MessageID:       msg.MessageID,
		InsertionTime:   msg.InsertionTime,
		ExpirationTime:  msg.ExpirationTime,
		PopReceipt:      "",
		TimeNextVisible: msg.InsertionTime,
	}
	type wrap struct {
		XMLName  xml.Name               `xml:"QueueMessagesList"`
		Messages []QueueMessageResponse `xml:"QueueMessage"`
	}
	writeStorageXML(w, http.StatusCreated, wrap{Messages: []QueueMessageResponse{resp}})
}

func handleQueueGetMessages(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	now := time.Now().Unix()
	visTimeout := int64(30)
	if v := r.URL.Query().Get("visibilitytimeout"); v != "" {
		var n int64
		_, _ = fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			visTimeout = n
		}
	}
	numMessages := 1
	if v := r.URL.Query().Get("numofmessages"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &numMessages)
	}
	if numMessages <= 0 || numMessages > 32 {
		numMessages = 1
	}
	var picked []QueueMessage
	queueData.Update(key, func(qq *QueueData) {
		for i := range qq.Messages {
			if len(picked) >= numMessages {
				break
			}
			if qq.Messages[i].VisibleAt > now {
				continue
			}
			qq.Messages[i].PopReceipt = generateUUID()
			qq.Messages[i].VisibleAt = now + visTimeout
			qq.Messages[i].DequeueCount++
			picked = append(picked, qq.Messages[i])
		}
	})
	type wrap struct {
		XMLName  xml.Name               `xml:"QueueMessagesList"`
		Messages []QueueMessageResponse `xml:"QueueMessage"`
	}
	out := wrap{}
	for _, m := range picked {
		out.Messages = append(out.Messages, QueueMessageResponse{
			MessageID:       m.MessageID,
			InsertionTime:   m.InsertionTime,
			ExpirationTime:  m.ExpirationTime,
			PopReceipt:      m.PopReceipt,
			TimeNextVisible: time.Unix(m.VisibleAt, 0).UTC().Format(time.RFC1123),
			DequeueCount:    m.DequeueCount,
			MessageText:     m.MessageText,
		})
	}
	writeStorageXML(w, http.StatusOK, out)
}

func handleQueuePeekMessages(w http.ResponseWriter, r *http.Request, account, queue string) {
	q, ok := queueData.Get(queueKey(account, queue))
	if !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	type wrap struct {
		XMLName  xml.Name               `xml:"QueueMessagesList"`
		Messages []QueueMessageResponse `xml:"QueueMessage"`
	}
	out := wrap{}
	now := time.Now().Unix()
	for _, m := range q.Messages {
		if m.VisibleAt > now {
			continue
		}
		out.Messages = append(out.Messages, QueueMessageResponse{
			MessageID:      m.MessageID,
			InsertionTime:  m.InsertionTime,
			ExpirationTime: m.ExpirationTime,
			DequeueCount:   m.DequeueCount,
			MessageText:    m.MessageText,
		})
	}
	writeStorageXML(w, http.StatusOK, out)
}

func handleQueueDeleteMessage(w http.ResponseWriter, r *http.Request, account, queue, messageID string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	popReceipt := r.URL.Query().Get("popreceipt")
	var found, mismatched bool
	queueData.Update(key, func(qq *QueueData) {
		out := qq.Messages[:0]
		for _, m := range qq.Messages {
			if m.MessageID == messageID {
				found = true
				if m.PopReceipt != popReceipt {
					mismatched = true
				} else {
					continue
				}
			}
			out = append(out, m)
		}
		qq.Messages = out
	})
	// A delete is only the holder of the pop receipt's to make, and the service
	// says so rather than reporting a deletion that did not happen.
	if !found {
		writeStorageError(w, "MessageNotFound",
			"The specified message does not exist.", http.StatusNotFound)
		return
	}
	if mismatched {
		writeStorageError(w, "PopReceiptMismatch",
			"The specified pop receipt did not match the pop receipt for a dequeued message.",
			http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleQueueClearMessages(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	queueData.Update(key, func(qq *QueueData) {
		qq.Messages = nil
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── Tables dispatch ─────────────────────────────────────────────────

func tableKey(account, table string) string { return account + "/" + table }

// Table entities are keyed account/table/partition/row, so one index under
// every path prefix serves a table's query, a table's deletion and an
// account-wide batch snapshot alike, instead of each decoding every entity in
// the process.
var tableEntitiesByPrefix sim.GenerationIndex[TableEntity]

// tableEntitiesUnder returns the entities whose key begins with prefix, which
// must end at a path separator.
func tableEntitiesUnder(prefix string) []TableEntity {
	return tableEntitiesByPrefix.LookupAll(tableEntities, prefix, func(e TableEntity) []string {
		return sim.PathPrefixes(tableEntityKey(e.Account, e.Table, e.PartitionKey, e.RowKey))
	})
}

func tableEntityKey(account, table, pk, rk string) string {
	return account + "/" + table + "/" + pk + "/" + rk
}

func upsertTableDataPlaneProjection(account, table string) {
	key := tableKey(account, table)
	if existing, ok := tableData.Get(key); ok {
		existing.Account = account
		existing.Name = table
		if existing.Created == "" {
			existing.Created = time.Now().UTC().Format(time.RFC3339)
		}
		tableData.Put(key, existing)
		return
	}
	tableData.Put(key, TableData{Account: account, Name: table, Created: time.Now().UTC().Format(time.RFC3339)})
}

func deleteTableDataPlaneProjection(account, table string) {
	tableData.Delete(tableKey(account, table))
	for _, e := range tableEntitiesUnder(account + "/" + table + "/") {
		tableEntities.Delete(tableEntityKey(e.Account, e.Table, e.PartitionKey, e.RowKey))
	}
}

func handleTablesDataPlane(w http.ResponseWriter, r *http.Request, account string) {
	// The Table service authorizes every request like its three siblings, over
	// its own, shorter Shared Key string — see blob_authorization.go.
	if !AuthorizeStorageDataPlane(w, r, "table", account, storageFirstSegment(strings.TrimPrefix(r.URL.Path, "/")), "") {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")

	// Transactional batch: POST /$batch (multipart/mixed change-set).
	if path == "$batch" && r.Method == http.MethodPost {
		handleTableBatch(w, r, account)
		return
	}

	// Tables CRUD path: POST /Tables, DELETE /Tables('name')
	if path == "Tables" && r.Method == http.MethodPost {
		handleTableCreate(w, r, account)
		return
	}
	if strings.HasPrefix(path, "Tables('") && strings.HasSuffix(path, "')") {
		name := strings.TrimSuffix(strings.TrimPrefix(path, "Tables('"), "')")
		if r.URL.Query().Get("comp") == "acl" {
			handleTableACL(w, r, account, name)
			return
		}
		if r.Method == http.MethodDelete {
			handleTableDelete(w, r, account, name)
			return
		}
		if r.Method == http.MethodGet {
			handleTableGet(w, r, account, name)
			return
		}
	}
	if path == "Tables" && r.Method == http.MethodGet {
		handleTablesList(w, r, account)
		return
	}
	if !strings.Contains(path, "/") && path != "" && r.URL.Query().Get("comp") == "acl" {
		handleTableACL(w, r, account, path)
		return
	}
	if strings.HasSuffix(path, "()") && r.Method == http.MethodGet {
		handleEntityQuery(w, r, account, strings.TrimSuffix(path, "()"))
		return
	}

	// Entity ops on /{table}
	// PartitionKey/RowKey-addressed: /{table}(PartitionKey='X',RowKey='Y')
	if i := strings.Index(path, "(PartitionKey="); i > 0 {
		table := path[:i]
		rest := path[i+1:]
		rest = strings.TrimSuffix(rest, ")")
		// rest is now PartitionKey='X',RowKey='Y'
		pk, rk := parsePKRK(rest)
		switch r.Method {
		case http.MethodGet:
			handleEntityGet(w, r, account, table, pk, rk)
		case http.MethodPut:
			// PUT = Update Entity = wholesale replace.
			handleEntityUpsert(w, r, account, table, pk, rk, false)
		case http.MethodPatch, "MERGE":
			// MERGE / PATCH = Merge Entity = overlay only the supplied
			// properties onto the existing entity, preserving omitted ones.
			handleEntityUpsert(w, r, account, table, pk, rk, true)
		case http.MethodDelete:
			handleEntityDelete(w, r, account, table, pk, rk)
		default:
			writeTableODataError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}

	// Plain /{table} — POST for insert, GET for query.
	if !strings.Contains(path, "/") && path != "" {
		switch r.Method {
		case http.MethodPost:
			handleEntityInsert(w, r, account, path)
		case http.MethodGet:
			handleEntityQuery(w, r, account, path)
		default:
			writeTableODataError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	writeTableODataError(w, "InvalidUri", "Unrecognized Tables data-plane path", http.StatusBadRequest)
}

func parsePKRK(s string) (pk, rk string) {
	// Input: `PartitionKey='X',RowKey='Y'`
	for _, kv := range strings.Split(s, ",") {
		parts := strings.SplitN(strings.TrimSpace(kv), "=", 2)
		if len(parts) != 2 {
			continue
		}
		val := strings.Trim(parts[1], "'")
		switch parts[0] {
		case "PartitionKey":
			pk = val
		case "RowKey":
			rk = val
		}
	}
	return pk, rk
}

func handleTableCreate(w http.ResponseWriter, r *http.Request, account string) {
	var body struct {
		TableName string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeTableODataError(w, "InvalidInput", err.Error(), http.StatusBadRequest)
		return
	}
	key := tableKey(account, body.TableName)
	if _, ok := tableData.Get(key); ok {
		writeTableODataError(w, "TableAlreadyExists", "The table specified already exists.", http.StatusConflict)
		return
	}
	upsertTableDataPlaneProjection(account, body.TableName)
	upsertStorageTableARMProjection(account, body.TableName)
	if strings.Contains(strings.ToLower(r.Header.Get("Prefer")), "return-no-content") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sim.WriteJSON(w, http.StatusCreated, map[string]string{"TableName": body.TableName})
}

func handleTableDelete(w http.ResponseWriter, r *http.Request, account, table string) {
	if _, ok := tableData.Get(tableKey(account, table)); !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	deleteTableDataPlaneProjection(account, table)
	deleteStorageTableARMProjection(account, table)
	w.WriteHeader(http.StatusNoContent)
}

func handleTableGet(w http.ResponseWriter, r *http.Request, account, table string) {
	t, ok := tableData.Get(tableKey(account, table))
	if !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]string{"TableName": t.Name})
}

func handleTablesList(w http.ResponseWriter, r *http.Request, account string) {
	prefix := account + "/"
	var names []map[string]string
	for _, t := range tableData.List() {
		if strings.HasPrefix(tableKey(t.Account, t.Name), prefix) {
			names = append(names, map[string]string{"TableName": t.Name})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": names})
}

func handleTableACL(w http.ResponseWriter, r *http.Request, account, table string) {
	key := tableKey(account, table)
	t, ok := tableData.Get(key)
	if !ok {
		writeStorageError(w, "ResourceNotFound", "The specified table does not exist.", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/xml")
		if err := xml.NewEncoder(w).Encode(TableSignedIdentifiers{Items: t.ACLs}); err != nil {
			sim.AzureError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		}
	case http.MethodPut:
		var body TableSignedIdentifiers
		if err := xml.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeStorageError(w, "InvalidXmlDocument", "The XML specified is not syntactically valid.", http.StatusBadRequest)
			return
		}
		t.ACLs = body.Items
		tableData.Put(key, t)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeStorageError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
	}
}

func handleEntityInsert(w http.ResponseWriter, r *http.Request, account, table string) {
	if _, ok := tableData.Get(tableKey(account, table)); !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	var props map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&props); err != nil {
		writeTableODataError(w, "InvalidInput", err.Error(), http.StatusBadRequest)
		return
	}
	pk, pkErr := jsonStringField(props["PartitionKey"])
	rk, rkErr := jsonStringField(props["RowKey"])
	if pkErr != nil || rkErr != nil {
		writeTableODataError(w, "InvalidInput", "PartitionKey and RowKey must be string values", http.StatusBadRequest)
		return
	}
	if pk == "" || rk == "" {
		writeTableODataError(w, "InvalidInput", "PartitionKey and RowKey are required", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	entity := TableEntity{
		Account: account, Table: table,
		PartitionKey: pk, RowKey: rk,
		Properties: props,
		ETag:       `W/"datetime'` + now + `'"`,
		Timestamp:  now,
	}
	tableEntities.Put(tableEntityKey(account, table, pk, rk), entity)
	w.Header().Set("ETag", entity.ETag)
	w.Header().Set("Preference-Applied", "return-no-content")
	if r.Header.Get("Prefer") == "return-content" {
		props["Timestamp"], _ = json.Marshal(now)
		sim.WriteJSON(w, http.StatusCreated, props)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleEntityGet(w http.ResponseWriter, r *http.Request, account, table, pk, rk string) {
	if _, ok := tableData.Get(tableKey(account, table)); !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	e, ok := tableEntities.Get(tableEntityKey(account, table, pk, rk))
	if !ok {
		writeTableODataError(w, "EntityNotFound", "The specified entity does not exist.", http.StatusNotFound)
		return
	}
	out := map[string]json.RawMessage{}
	for k, v := range e.Properties {
		out[k] = v
	}
	out["Timestamp"], _ = json.Marshal(e.Timestamp)
	w.Header().Set("ETag", e.ETag)
	sim.WriteJSON(w, http.StatusOK, out)
}

// handleEntityUpsert handles Update Entity (PUT, full replace) and Merge Entity
// (MERGE/PATCH, partial overlay). When merge is true the supplied properties are
// overlaid onto the existing entity, preserving any omitted ones; otherwise the
// entity is replaced wholesale. Both upsert when the entity doesn't yet exist.
func handleEntityUpsert(w http.ResponseWriter, r *http.Request, account, table, pk, rk string, merge bool) {
	if _, ok := tableData.Get(tableKey(account, table)); !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	var props map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&props); err != nil {
		writeTableODataError(w, "InvalidInput", err.Error(), http.StatusBadRequest)
		return
	}
	if merge {
		if existing, ok := tableEntities.Get(tableEntityKey(account, table, pk, rk)); ok {
			merged := make(map[string]json.RawMessage, len(existing.Properties)+len(props))
			for k, v := range existing.Properties {
				merged[k] = v
			}
			for k, v := range props {
				merged[k] = v
			}
			props = merged
		}
	}
	props["PartitionKey"], _ = json.Marshal(pk)
	props["RowKey"], _ = json.Marshal(rk)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	entity := TableEntity{
		Account: account, Table: table,
		PartitionKey: pk, RowKey: rk,
		Properties: props,
		ETag:       `W/"datetime'` + now + `'"`,
		Timestamp:  now,
	}
	tableEntities.Put(tableEntityKey(account, table, pk, rk), entity)
	w.Header().Set("ETag", entity.ETag)
	w.WriteHeader(http.StatusNoContent)
}

func handleEntityDelete(w http.ResponseWriter, r *http.Request, account, table, pk, rk string) {
	if _, ok := tableData.Get(tableKey(account, table)); !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	if !tableEntities.Delete(tableEntityKey(account, table, pk, rk)) {
		writeTableODataError(w, "EntityNotFound", "The specified entity does not exist.", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleEntityQuery(w http.ResponseWriter, r *http.Request, account, table string) {
	if _, ok := tableData.Get(tableKey(account, table)); !ok {
		writeTableODataError(w, "TableNotFound", "The table specified does not exist.", http.StatusNotFound)
		return
	}
	prefix := account + "/" + table + "/"

	// Gather this table's entities, sorted by (PartitionKey, RowKey) — the
	// canonical Tables ordering real Azure pages over.
	// The rows are copied out of the index because they are sorted below.
	matching := append([]TableEntity(nil), tableEntitiesUnder(prefix)...)
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].PartitionKey != matching[j].PartitionKey {
			return matching[i].PartitionKey < matching[j].PartitionKey
		}
		return matching[i].RowKey < matching[j].RowKey
	})

	// $filter — Azure Tables evaluates a server-side OData filter against each
	// entity's properties (incl. PartitionKey/RowKey). aztables pushes the
	// filter to the server and does not re-filter client-side, so ignoring it
	// returns wrong results.
	var filterNode odataNode
	if f := strings.TrimSpace(r.URL.Query().Get("$filter")); f != "" {
		node, err := azureParseODataFilter(f)
		if err != nil {
			writeTableODataError(w, "InvalidInput", err.Error(), http.StatusBadRequest)
			return
		}
		filterNode = node
	}

	// $skiptoken — the sim pages by entity offset (encoded in the
	// continuation headers Azure emits when $top truncates the result).
	start := 0
	if tok := r.URL.Query().Get("NextRowKey"); tok != "" {
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 {
			start = n
		}
	}

	limit := -1
	if raw := r.URL.Query().Get("$top"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	// $select — project the returned property map to the named columns. Real
	// Tables always includes the system keys the SDK relies on (PartitionKey /
	// RowKey / Timestamp) plus whatever the caller selected.
	selectSet := parseTableSelect(r.URL.Query().Get("$select"))

	entries := []map[string]json.RawMessage{}
	idx := 0
	nextToken := ""
	for _, e := range matching {
		if filterNode != nil && !filterNode.eval(tableEntityFilterMap(e)) {
			continue
		}
		if idx < start {
			idx++
			continue
		}
		if limit >= 0 && len(entries) >= limit {
			nextToken = strconv.Itoa(idx)
			break
		}
		out := map[string]json.RawMessage{}
		for k, v := range e.Properties {
			if selectSet != nil && !selectSet[k] {
				continue
			}
			out[k] = v
		}
		out["Timestamp"], _ = json.Marshal(e.Timestamp)
		entries = append(entries, out)
		idx++
	}

	if nextToken != "" {
		// Real Tables returns the continuation tokens as response headers; the
		// SDK forwards them back as NextPartitionKey/NextRowKey query params.
		w.Header().Set("x-ms-continuation-NextPartitionKey", "p")
		w.Header().Set("x-ms-continuation-NextRowKey", nextToken)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": entries})
}

// tableEntityFilterMap builds the property map an OData $filter is evaluated
// against: every stored property (unmarshalled from its raw JSON) plus the
// PartitionKey/RowKey/Timestamp system properties.
func tableEntityFilterMap(e TableEntity) map[string]any {
	m := make(map[string]any, len(e.Properties)+3)
	for k, raw := range e.Properties {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			m[k] = v
		}
	}
	m["PartitionKey"] = e.PartitionKey
	m["RowKey"] = e.RowKey
	m["Timestamp"] = e.Timestamp
	return m
}

// parseTableSelect builds the set of property names a $select restricts to, or
// nil when $select is absent (= return all properties). The system keys
// PartitionKey/RowKey are always retained so the SDK can address the entity.
func parseTableSelect(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	set := map[string]bool{"PartitionKey": true, "RowKey": true}
	for _, col := range strings.Split(raw, ",") {
		if c := strings.TrimSpace(col); c != "" {
			set[c] = true
		}
	}
	return set
}

// jsonStringField decodes a JSON value that is expected to be a string. An
// absent field (nil/empty raw) decodes to "" with no error — the caller treats
// that as "missing" and raises the required-field error. A field that is present
// but is NOT a JSON string (e.g. an object or number where a key is expected) is
// corrupt input and returns an error, so a malformed value is never silently
// indistinguishable from an absent one.
func jsonStringField(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("expected a JSON string: %w", err)
	}
	return s, nil
}

// storageFirstSegment is the share or queue a data-plane path addresses, which
// is the resource its authorization is evaluated against.
func storageFirstSegment(path string) string {
	if path == "" {
		return ""
	}
	segment, _, _ := strings.Cut(path, "/")
	return segment
}

// storagePathResource splits a path-style remainder into the container-level
// resource and what follows it.
func storagePathResource(rest string) (string, string) {
	container, blobName, _ := strings.Cut(rest, "/")
	return container, blobName
}
