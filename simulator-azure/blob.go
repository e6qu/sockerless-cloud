package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure Storage Blob data plane
// (https://learn.microsoft.com/rest/api/storageservices/blob-service-rest-api).
//
// ARM `Microsoft.Storage/storageAccounts/{name}` advertises this plane at
// `https://{name}.blob.<host>:<port>/` in its `primaryEndpoints.blob` field, and
// real SDK / azcopy / az CLI consumers follow that URL. The middleware installed
// by registerBlobDataPlane claims requests addressed there — plus the
// Azurite-compatible `/{account}/…` path-style form SDKs use against a localhost
// endpoint — and handleBlobDataPlane selects the operation from the
// `restype` + `comp` query pair the service itself discriminates on.
//
// The whole vendored Blob data-plane surface is served: containers, blobs, the
// copy family, blocks, page and append ranges, snapshots and soft delete, tags,
// tier and expiry, immutability and legal hold, leases, blob query, blob batch
// and the account-wide service operations. Blob type is enforced — a page
// operation on a block blob is refused with InvalidBlobType — and a page blob's
// written ranges are tracked separately from its bytes, so a page written with
// zeros is distinguishable from a sparse one.
//
// SSE-C headers (`x-ms-encryption-key-sha256`) surface through the
// sentinel-header log but are not enforced by the handler.

// BlobLease is the lock Lease Blob / Lease Container establishes on a resource.
// Azure derives the reported lease state, status and duration from the clock, so
// only the facts are stored: the lease ID, the requested duration (-1 for an
// infinite lease, 15–60 seconds for a finite one), when a finite lease runs out,
// when a pending break completes, and whether a break has already completed.
type BlobLease struct {
	ID        string
	Duration  int32
	ExpiresAt time.Time
	BreakAt   time.Time
	Broken    bool
}

// BlobPageRange is one written (non-sparse) byte range of a page blob. Start and
// End are both inclusive, exactly as Azure reports them in Get Page Ranges.
type BlobPageRange struct {
	Start int64
	End   int64
}

// BlobSignedIdentifier is one stored access policy of a container ACL.
type BlobSignedIdentifier struct {
	ID         string
	Start      string
	Expiry     string
	Permission string
}

type BlobObject struct {
	Account   string
	Container string
	Name      string
	// Snapshot is empty for the base blob and carries the snapshot timestamp
	// (`?snapshot=<ts>`) for a snapshot, which is how Azure addresses one.
	Snapshot     string
	Data         []byte
	ContentType  string
	BlobType     string
	ETag         string
	LastModified string
	CreationTime string
	Metadata     map[string]string
	Tags         map[string]string

	CacheControl       string
	ContentEncoding    string
	ContentLanguage    string
	ContentDisposition string
	// ContentMD5 is base64, the encoding both x-ms-blob-content-md5 and the
	// Content-MD5 response header use.
	ContentMD5 string

	AccessTier           string
	AccessTierInferred   bool
	AccessTierChangeTime string
	ExpiresOn            string

	SequenceNumber      int64
	CommittedBlockCount int32
	Sealed              bool

	// PageRanges tracks the written extents of a page blob; everything outside
	// them is sparse and reads back as zeros.
	PageRanges []BlobPageRange

	Lease BlobLease

	ImmutabilityPolicyExpiry string
	ImmutabilityPolicyMode   string
	LegalHold                bool

	// Deleted marks a soft-deleted blob, retained because the account's blob
	// service properties declare a delete-retention policy.
	Deleted                bool
	DeletedTime            string
	RemainingRetentionDays int32

	CopyID                  string
	CopyStatus              string
	CopySource              string
	CopyProgress            string
	CopyCompletionTime      string
	CopyStatusDescription   string
	IncrementalCopy         bool
	CopyDestinationSnapshot string
}

type BlobContainerData struct {
	Account  string
	Name     string
	Created  string
	ETag     string
	Metadata map[string]string
	// Version identifies one incarnation of a container name, which is how
	// Restore Container addresses a soft-deleted container.
	Version           string
	PublicAccess      string
	Lease             BlobLease
	SignedIdentifiers []BlobSignedIdentifier

	Deleted                bool
	DeletedTime            string
	RemainingRetentionDays int32
}

type BlobBlockData struct {
	Account         string
	Container       string
	Blob            string
	BlockID         string
	UncommittedData []byte
	CommittedData   []byte
	HasUncommitted  bool
	HasCommitted    bool
	CommitOrdinal   int
}

type blockRef struct {
	id   string
	data []byte
}

var (
	blobObjects        sim.Store[BlobObject]
	blobContainersData sim.Store[BlobContainerData]
	blobBlocks         sim.Store[BlobBlockData]
)

// Secondary indexes that let a per-container / per-blob operation enumerate
// only the relevant keys instead of scanning every blob in every container
// (or every block in every blob) — the store exposes no prefix scan. Both
// index the FULL store key:
//   - blobIndex:  containerKey ("account/container") → set of blobObjectKeys
//   - blockIndex: blobKey ("account/container/blob") → set of blobBlockKeys
//
// All blobObjects / blobBlocks mutations route through the put*/delete*
// helpers below so the indexes stay consistent. blobIndexMu guards both.
//   - blocksByContainer: containerKey ("account/container") → set of
//     blobBlockKeys, so a container delete can reach staged-but-uncommitted
//     blocks that have no committed blob object.
var (
	blobIndexMu       sync.Mutex
	blobIndex         = map[string]map[string]struct{}{}
	blockIndex        = map[string]map[string]struct{}{}
	blocksByContainer = map[string]map[string]struct{}{}
)

func indexAdd(idx map[string]map[string]struct{}, group, key string) {
	set := idx[group]
	if set == nil {
		set = map[string]struct{}{}
		idx[group] = set
	}
	set[key] = struct{}{}
}

func indexRemove(idx map[string]map[string]struct{}, group, key string) {
	set := idx[group]
	if set == nil {
		return
	}
	delete(set, key)
	if len(set) == 0 {
		delete(idx, group)
	}
}

// putBlobObject stores a blob (or one of its snapshots) under the key its own
// Account/Container/Name/Snapshot fields address, keeping the container index in
// step. The record carries its own coordinates, so it is the single source of
// the key.
func putBlobObject(b BlobObject) {
	key := blobObjectKeyOf(b)
	blobIndexMu.Lock()
	indexAdd(blobIndex, blobContainerKey(b.Account, b.Container), key)
	blobIndexMu.Unlock()
	blobObjects.Put(key, b)
}

// deleteBlobSnapshot removes one stored record — the base blob when snapshot is
// empty, a snapshot otherwise — and keeps the container index in step.
func deleteBlobSnapshot(account, container, name, snapshot string) {
	key := blobSnapshotKey(account, container, name, snapshot)
	blobIndexMu.Lock()
	indexRemove(blobIndex, blobContainerKey(account, container), key)
	blobIndexMu.Unlock()
	blobObjects.Delete(key)
}

func putBlobBlock(account, container, blob, blockID string, b BlobBlockData) {
	key := blobBlockKey(account, container, blob, blockID)
	blobIndexMu.Lock()
	indexAdd(blockIndex, blobObjectKey(account, container, blob), key)
	indexAdd(blocksByContainer, blobContainerKey(account, container), key)
	blobIndexMu.Unlock()
	blobBlocks.Put(key, b)
}

func deleteBlobBlock(account, container, blob, blockID string) {
	key := blobBlockKey(account, container, blob, blockID)
	blobIndexMu.Lock()
	indexRemove(blockIndex, blobObjectKey(account, container, blob), key)
	indexRemove(blocksByContainer, blobContainerKey(account, container), key)
	blobIndexMu.Unlock()
	blobBlocks.Delete(key)
}

// blobKeysInContainer returns the store keys of every blob in the container.
func blobKeysInContainer(account, container string) []string {
	blobIndexMu.Lock()
	defer blobIndexMu.Unlock()
	set := blobIndex[blobContainerKey(account, container)]
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// blockKeysForBlob returns the store keys of every block staged/committed
// under the given blob.
func blockKeysForBlob(account, container, blob string) []string {
	blobIndexMu.Lock()
	defer blobIndexMu.Unlock()
	set := blockIndex[blobObjectKey(account, container, blob)]
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// blockKeysInContainer returns the store keys of every block staged/committed
// in the container, including blocks with no committed blob object.
func blockKeysInContainer(account, container string) []string {
	blobIndexMu.Lock()
	defer blobIndexMu.Unlock()
	set := blocksByContainer[blobContainerKey(account, container)]
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func registerBlobDataPlane(srv *sim.Server) {
	blobObjects = sim.MakeStore[BlobObject](srv.DB(), "blob_objects")
	blobContainersData = sim.MakeStore[BlobContainerData](srv.DB(), "blob_containers_data")
	blobBlocks = sim.MakeStore[BlobBlockData](srv.DB(), "blob_blocks")
	blobServicePropsStore = sim.MakeStore[BlobServiceConfig](srv.DB(), "blob_dataplane_service_properties")
	blobDelegationKeys = sim.MakeStore[BlobUserDelegationKey](srv.DB(), "blob_user_delegation_keys")

	// Rebuild the secondary indexes from any persisted store contents so a
	// restart with a SQLite-backed store starts consistent.
	blobIndexMu.Lock()
	blobIndex = map[string]map[string]struct{}{}
	blockIndex = map[string]map[string]struct{}{}
	blocksByContainer = map[string]map[string]struct{}{}
	for _, b := range blobObjects.List() {
		indexAdd(blobIndex, blobContainerKey(b.Account, b.Container), blobObjectKeyOf(b))
	}
	for _, bl := range blobBlocks.List() {
		indexAdd(blockIndex, blobObjectKey(bl.Account, bl.Container, bl.Blob), blobBlockKey(bl.Account, bl.Container, bl.Blob, bl.BlockID))
		indexAdd(blocksByContainer, blobContainerKey(bl.Account, bl.Container), blobBlockKey(bl.Account, bl.Container, bl.Blob, bl.BlockID))
	}
	blobIndexMu.Unlock()

	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			hostname := host
			if i := strings.LastIndex(hostname, ":"); i >= 0 {
				hostname = hostname[:i]
			}
			parts := strings.SplitN(hostname, ".blob.", 2)
			if len(parts) == 2 {
				handleBlobDataPlane(w, r, parts[0])
				return
			}
			// A Cosmos data-plane request (master-key auth / documentdb headers,
			// or a path under the Cosmos document API) shares the sim port but
			// must never be misrouted to blob by the path-style fallback below.
			// Cosmos also sends x-ms-version, which is a storage signal, and a
			// request the Cosmos plane refuses for its credential carries no
			// Cosmos header at all — only its path says whose it is.
			if cosmosIsDataPlaneRequest(r) || cosmosIsDataPlanePath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// Path-style fallback (Azurite-compatible). When the
			// host carries NO service-specific Azure subdomain
			// AND the URL path starts with `/{account}/...` AND
			// the request carries an Azure-Storage protocol signal,
			// dispatch as a blob data-plane request. Matches the
			// Azure SDK / azurerm provider default for non-
			// `*.core.windows.net` endpoints and the Azurite
			// connection-string contract. Account names are
			// accepted as-is (real Azurite is permissive — no
			// prior ARM registration required); the storage-signal
			// requirement keeps non-storage co-tenants (IMDS at
			// `/metadata/...`, Monitor ingest at
			// `/dataCollectionRules/...`, MSI at `/metadata/...`)
			// from being misrouted on the shared sim port.
			if hasNonStorageAzureSubdomain(hostname) {
				next.ServeHTTP(w, r)
				return
			}
			if !hasAzureStorageSignal(r) {
				next.ServeHTTP(w, r)
				return
			}
			if account, rest, ok := splitPathStyleAccount(r.URL.Path); ok {
				r.URL.Path = "/" + rest
				handleBlobDataPlane(w, r, account)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

// hasAzureStorageSignal reports whether the request carries a
// protocol marker that real Azure Storage SDKs always emit:
//   - `x-ms-version`     — REST API version selector (every SDK call)
//   - `x-ms-date`        — request-signing timestamp
//   - `x-ms-blob-type`   — PutBlob blob-type selector
//   - `x-ms-type`        — Files PutFile file-type selector
//   - `Authorization: SharedKey ...` — account-key signed request
//   - query `restype=`   — container/share/directory operation discriminator
//   - query `comp=`      — sub-resource (list, properties, metadata, …)
//   - query `sv=`        — Shared Access Signature (SAS) version
//
// Co-tenants on the shared sim port (IMDS `/metadata/...`, Monitor
// ingest `/dataCollectionRules/...`, MSI token endpoint) never carry
// any of these markers, so the signal cleanly partitions storage
// path-style requests from everything else.
func hasAzureStorageSignal(r *http.Request) bool {
	if r.Header.Get("x-ms-version") != "" ||
		r.Header.Get("x-ms-date") != "" ||
		r.Header.Get("x-ms-blob-type") != "" ||
		r.Header.Get("x-ms-type") != "" {
		return true
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "SharedKey ") || strings.HasPrefix(auth, "SharedKeyLite ") {
		return true
	}
	q := r.URL.Query()
	if q.Get("restype") != "" || q.Get("comp") != "" || q.Get("sv") != "" {
		return true
	}
	return false
}

// hasNonStorageAzureSubdomain reports whether the host carries an
// Azure-service subdomain that belongs to a service other than Azure
// Storage. A host like `myvault.vault.localhost`,
// `myns.servicebus.localhost`, `myfn.azurewebsites.net`, or
// `mycr.azurecr.io` MUST NOT be dispatched as a path-style storage
// request — the subdomain identifies a different data plane. New
// service subdomains added to the sim go here too.
//
// The storage subdomains the simulator does not implement — static website
// (`.web.`) and Azure Data Lake Storage Gen2 (`.dfs.`) — are not listed: the
// storage dispatcher answers them with a declared gap before this predicate is
// consulted, because they are storage, not another service.
func hasNonStorageAzureSubdomain(hostname string) bool {
	for _, marker := range []string{
		".vault.",
		".servicebus.",
		".redis.cache.",
		".postgres.database.",
		".azurewebsites.",
		".azurecr.",
		".azure-api.",
		".azurecontainerapps.",
		".applicationinsights.",
		".cognitiveservices.",
	} {
		if strings.Contains(hostname, marker) {
			return true
		}
	}
	return false
}

// splitPathStyleAccount returns (account, restOfPath, true) when the
// path looks like Azurite-style storage: `/{account}/{container}/{blob}`.
// Real Azurite accepts any account name without prior registration;
// the discriminator from non-storage routes is the prefix exclusion
// — ARM (`/subscriptions/...`, `/providers/...`), Docker SDK
// (`/v1.NN/...`), and GCP-shaped paths (`/v1/...`, `/storage/v1/...`)
// are NOT path-style storage. Anything else with `/{segment}/{rest}`
// is dispatched to the data plane with `{segment}` as the account.
func splitPathStyleAccount(path string) (account, rest string, ok bool) {
	p := strings.TrimPrefix(path, "/")
	if p == "" {
		return "", "", false
	}
	slash := strings.IndexByte(p, '/')
	var first string
	if slash < 0 {
		first = p
		p = ""
	} else {
		first = p[:slash]
		p = p[slash+1:]
	}
	if isNonStorageFirstSegment(first) {
		return "", "", false
	}
	return first, p, true
}

// isNonStorageFirstSegment reports whether the first path segment
// belongs to a non-storage route (ARM, Docker SDK, GCP-shaped, or
// other registered sim surfaces). Anything not in this set is
// treated as a storage account name for path-style dispatch.
func isNonStorageFirstSegment(s string) bool {
	switch s {
	case "subscriptions", "providers", "tenants", "locations",
		"storage", "v1", "$metadata":
		return true
	}
	// Docker SDK paths: /v1.44/, /v1.41/, etc.
	if strings.HasPrefix(s, "v1.") {
		return true
	}
	// internal/v1/ surface (sockerless control plane).
	if s == "internal" {
		return true
	}
	return false
}

// blobStoragePage applies Azure Storage list pagination to an already-sorted
// slice. It reads ?maxresults=N (page size, default unlimited) and
// ?marker=NAME (continuation token = name of first item to include) from the
// request query, slices the items, and returns the page plus a NextMarker
// value (empty when the page is the last one). The name func extracts the
// sortable name from each item.
func blobStoragePage[T any](r *http.Request, items []T, name func(T) string) ([]T, string) {
	// Apply marker: skip items whose name is <= marker.
	marker := r.URL.Query().Get("marker")
	start := 0
	if marker != "" {
		for start < len(items) && name(items[start]) <= marker {
			start++
		}
	}
	items = items[start:]

	// Apply maxresults.
	if raw := r.URL.Query().Get("maxresults"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < len(items) {
			// NextMarker is the name of the last returned item. The next
			// request passes it as ?marker=NAME and the skip loop above
			// advances past all items whose name is <= marker, landing on
			// the first item of the next page.
			next := name(items[n-1])
			return items[:n], next
		}
	}
	return items, ""
}

func blobObjectKey(account, container, name string) string {
	return account + "/" + container + "/" + name
}

// blobSnapshotKey addresses one snapshot of a blob. Azure addresses a snapshot
// with the `?snapshot=<timestamp>` query on the base blob's URL, and the store
// key mirrors that spelling so the base blob and its snapshots are distinct
// rows under the same container index.
func blobSnapshotKey(account, container, name, snapshot string) string {
	if snapshot == "" {
		return blobObjectKey(account, container, name)
	}
	return blobObjectKey(account, container, name) + "?snapshot=" + snapshot
}

func blobObjectKeyOf(b BlobObject) string {
	return blobSnapshotKey(b.Account, b.Container, b.Name, b.Snapshot)
}

func blobContainerKey(account, container string) string {
	return account + "/" + container
}

// blobDeletedContainerKey addresses one soft-deleted incarnation of a container
// name. A container name can be recreated after a delete, so the live row and
// every retained deleted row have to coexist; Azure distinguishes them by the
// version Restore Container takes.
func blobDeletedContainerKey(account, container, version string) string {
	return account + "/" + container + "#deleted#" + version
}
func blobBlockKey(account, container, blob, blockID string) string {
	return account + "/" + container + "/" + blob + "/" + blockID
}

// handleBlobDataPlane dispatches one Blob Storage data-plane request. The
// operation is selected by the `restype` + `comp` query pair — plus, where Azure
// overloads one `comp` across several operations, the discriminating request
// header — at three levels: service `/`, container `/{container}` and blob
// `/{container}/{blob}`.
//
// A `restype`/`comp` combination the dispatcher does not recognize is answered
// with a declared gap, never by falling through to whichever sibling handler
// happens to sit under the same method: a fall-through would perform a DIFFERENT
// operation than the caller asked for and report it as success.
func handleBlobDataPlane(w http.ResponseWriter, r *http.Request, account string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	q := r.URL.Query()
	comp, restype := q.Get("comp"), q.Get("restype")

	// Service level: /?restype=…&comp=…
	if path == "" {
		handleBlobServiceLevel(w, r, account, restype, comp)
		return
	}

	segs := strings.SplitN(path, "/", 2)
	container := segs[0]
	if len(segs) == 1 {
		handleBlobContainerLevel(w, r, account, container, restype, comp)
		return
	}

	blob := segs[1]
	// Get Account Information is the one blob-level operation carrying a
	// restype; every other blob operation has none.
	if restype != "" {
		if r.Method == http.MethodGet && restype == "account" && comp == "properties" {
			handleBlobGetAccountInfo(w, r, account)
			return
		}
		writeStorageOperationNotImplemented(w, r, "Blob")
		return
	}
	switch r.Method {
	case http.MethodPut:
		handleBlobLevelPut(w, r, account, container, blob, comp)
	case http.MethodGet:
		switch comp {
		case "blocklist":
			handleGetBlockList(w, r, account, container, blob)
		case "pagelist":
			handleGetPageRanges(w, r, account, container, blob)
		case "tags":
			handleGetBlobTags(w, r, account, container, blob)
		case "":
			handleGetBlob(w, r, account, container, blob)
		default:
			writeStorageOperationNotImplemented(w, r, "Blob")
		}
	case http.MethodPost:
		if comp == "query" {
			handleBlobQuery(w, r, account, container, blob)
			return
		}
		writeStorageOperationNotImplemented(w, r, "Blob")
	case http.MethodHead:
		if comp != "" {
			writeStorageOperationNotImplemented(w, r, "Blob")
			return
		}
		handleHeadBlob(w, r, account, container, blob)
	case http.MethodDelete:
		switch comp {
		case "immutabilityPolicies":
			handleDeleteBlobImmutabilityPolicy(w, r, account, container, blob)
		case "":
			handleDeleteBlob(w, r, account, container, blob)
		default:
			writeStorageOperationNotImplemented(w, r, "Blob")
		}
	default:
		writeStorageOperationNotImplemented(w, r, "Blob")
	}
}

// handleBlobLevelPut resolves the blob-level PUT operations. Azure overloads two
// `comp` values across several operations and discriminates them by header:
// `comp=properties` is Set Blob HTTP Headers unless x-ms-blob-content-length
// (Resize) or x-ms-sequence-number-action (Update Sequence Number) is present,
// and `comp=page` is Upload Pages unless x-ms-page-write says `clear`.
func handleBlobLevelPut(w http.ResponseWriter, r *http.Request, account, container, blob, comp string) {
	switch comp {
	case "block":
		if r.Header.Get("x-ms-copy-source") != "" {
			handleStageBlockFromURL(w, r, account, container, blob)
			return
		}
		handleStageBlock(w, r, account, container, blob)
	case "blocklist":
		handleCommitBlockList(w, r, account, container, blob)
	case "lease":
		handleBlobLease(w, r, account, container, blob)
	case "metadata":
		handleSetBlobMetadata(w, r, account, container, blob)
	case "properties":
		switch {
		case r.Header.Get("x-ms-blob-content-length") != "":
			handlePageBlobResize(w, r, account, container, blob)
		case r.Header.Get("x-ms-sequence-number-action") != "":
			handlePageBlobUpdateSequenceNumber(w, r, account, container, blob)
		default:
			handleSetBlobHTTPHeaders(w, r, account, container, blob)
		}
	case "tier":
		handleSetBlobTier(w, r, account, container, blob)
	case "expiry":
		handleSetBlobExpiry(w, r, account, container, blob)
	case "tags":
		handleSetBlobTags(w, r, account, container, blob)
	case "snapshot":
		handleCreateBlobSnapshot(w, r, account, container, blob)
	case "undelete":
		handleUndeleteBlob(w, r, account, container, blob)
	case "immutabilityPolicies":
		handleSetBlobImmutabilityPolicy(w, r, account, container, blob)
	case "legalhold":
		handleSetBlobLegalHold(w, r, account, container, blob)
	case "copy":
		handleBlobCompCopy(w, r, account, container, blob)
	case "incrementalcopy":
		handlePageBlobCopyIncremental(w, r, account, container, blob)
	case "page":
		if strings.EqualFold(r.Header.Get("x-ms-page-write"), "clear") {
			handlePageBlobClearPages(w, r, account, container, blob)
			return
		}
		if r.Header.Get("x-ms-copy-source") != "" {
			handlePageBlobUploadPagesFromURL(w, r, account, container, blob)
			return
		}
		handlePageBlobUploadPages(w, r, account, container, blob)
	case "appendblock":
		if r.Header.Get("x-ms-copy-source") != "" {
			handleAppendBlockFromURL(w, r, account, container, blob)
			return
		}
		handleAppendBlock(w, r, account, container, blob)
	case "seal":
		handleAppendBlobSeal(w, r, account, container, blob)
	case "":
		// Copy Blob is the bare PUT plus x-ms-copy-source — Azure spells it
		// with no comp at all.
		if r.Header.Get("x-ms-copy-source") != "" {
			handleCopyBlob(w, r, account, container, blob)
			return
		}
		handlePutBlob(w, r, account, container, blob)
	default:
		writeStorageOperationNotImplemented(w, r, "Blob")
	}
}

// handleBlobServiceLevel resolves the account-wide operations addressed at `/`.
func handleBlobServiceLevel(w http.ResponseWriter, r *http.Request, account, restype, comp string) {
	switch {
	case r.Method == http.MethodGet && restype == "" && comp == "list":
		handleListContainers(w, r, account)
	case r.Method == http.MethodGet && restype == "" && comp == "blobs":
		handleFilterBlobs(w, r, account, "")
	case r.Method == http.MethodPost && restype == "" && comp == "batch":
		handleBlobSubmitBatch(w, r, account, "")
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		restype == "service" && comp == "properties":
		// Get Blob Service Properties. The azurerm provider polls this while
		// waiting for a storage account's data plane to come up, so it is part
		// of creating an account rather than an optional extra.
		handleGetBlobServiceProperties(w, r, account)
	case r.Method == http.MethodPut && restype == "service" && comp == "properties":
		handleSetBlobServiceProperties(w, r, account)
	case r.Method == http.MethodGet && restype == "service" && comp == "stats":
		handleGetBlobServiceStatistics(w, r, account)
	case r.Method == http.MethodPost && restype == "service" && comp == "userdelegationkey":
		handleGetUserDelegationKey(w, r, account)
	case r.Method == http.MethodGet && restype == "account" && comp == "properties":
		handleBlobGetAccountInfo(w, r, account)
	default:
		writeStorageOperationNotImplemented(w, r, "Blob")
	}
}

// handleBlobContainerLevel resolves the operations addressed at `/{container}`.
func handleBlobContainerLevel(w http.ResponseWriter, r *http.Request, account, container, restype, comp string) {
	if restype == "account" && comp == "properties" && r.Method == http.MethodGet {
		handleBlobGetAccountInfo(w, r, account)
		return
	}
	if restype != "container" {
		writeStorageOperationNotImplemented(w, r, "Blob")
		return
	}
	switch r.Method {
	case http.MethodPut:
		switch comp {
		case "":
			handleCreateContainer(w, r, account, container)
		case "metadata":
			handleSetContainerMetadata(w, r, account, container)
		case "acl":
			handleSetContainerAccessPolicy(w, r, account, container)
		case "lease":
			handleContainerLease(w, r, account, container)
		case "rename":
			handleRenameContainer(w, r, account, container)
		case "undelete":
			handleRestoreContainer(w, r, account, container)
		default:
			writeStorageOperationNotImplemented(w, r, "Blob")
		}
	case http.MethodDelete:
		if comp != "" {
			writeStorageOperationNotImplemented(w, r, "Blob")
			return
		}
		handleDeleteContainer(w, r, account, container)
	case http.MethodGet:
		switch comp {
		case "list":
			handleListBlobs(w, r, account, container)
		case "acl":
			handleGetContainerAccessPolicy(w, r, account, container)
		case "blobs":
			handleFilterBlobs(w, r, account, container)
		case "":
			handleGetContainer(w, r, account, container)
		default:
			writeStorageOperationNotImplemented(w, r, "Blob")
		}
	case http.MethodPost:
		if comp == "batch" {
			handleBlobSubmitBatch(w, r, account, container)
			return
		}
		writeStorageOperationNotImplemented(w, r, "Blob")
	case http.MethodHead:
		if comp != "" {
			writeStorageOperationNotImplemented(w, r, "Blob")
			return
		}
		handleGetContainer(w, r, account, container)
	default:
		writeStorageOperationNotImplemented(w, r, "Blob")
	}
}

func handleCreateContainer(w http.ResponseWriter, r *http.Request, account, container string) {
	key := blobContainerKey(account, container)
	if _, ok := blobContainersData.Get(key); ok {
		writeStorageError(w, "ContainerAlreadyExists",
			"The specified container already exists.", http.StatusConflict)
		return
	}
	c := BlobContainerData{
		Account:      account,
		Name:         container,
		Created:      time.Now().UTC().Format(http.TimeFormat),
		ETag:         `"` + generateUUID() + `"`,
		Metadata:     collectMetadata(r),
		Version:      generateUUID(),
		PublicAccess: r.Header.Get("x-ms-blob-public-access"),
	}
	blobContainersData.Put(key, c)
	w.Header().Set("ETag", c.ETag)
	w.Header().Set("Last-Modified", c.Created)
	w.WriteHeader(http.StatusCreated)
}

func handleDeleteContainer(w http.ResponseWriter, r *http.Request, account, container string) {
	key := blobContainerKey(account, container)
	c, ok := blobContainersData.Get(key)
	if !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	if !blobContainerWriteAllowed(w, r, c) {
		return
	}
	blobContainersData.Delete(key)

	// With a container delete-retention policy in force the container and its
	// contents are retained for the policy's window and Restore Container brings
	// them back; without one the delete is permanent.
	if days, soft := blobContainerSoftDeleteDays(account); soft {
		c.Deleted = true
		c.DeletedTime = blobNowHTTP()
		c.RemainingRetentionDays = days
		c.Lease = BlobLease{}
		blobContainersData.Put(blobDeletedContainerKey(account, container, c.Version), c)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Cascade-delete this container's blobs and their staged/committed blocks
	// via the secondary indexes, touching only the container's own keys.
	for _, key := range blobKeysInContainer(account, container) {
		if b, ok := blobObjects.Get(key); ok {
			deleteBlobSnapshot(b.Account, b.Container, b.Name, b.Snapshot)
		}
	}
	for _, key := range blockKeysInContainer(account, container) {
		if bl, ok := blobBlocks.Get(key); ok {
			deleteBlobBlock(bl.Account, bl.Container, bl.Blob, bl.BlockID)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleGetContainer(w http.ResponseWriter, r *http.Request, account, container string) {
	c, ok := blobContainersData.Get(blobContainerKey(account, container))
	if !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	writeContainerHeaders(w, c)
	w.WriteHeader(http.StatusOK)
}

// writeContainerHeaders emits the container property headers Get Container
// Properties answers with: identity, metadata, lease state and access level.
func writeContainerHeaders(w http.ResponseWriter, c BlobContainerData) {
	w.Header().Set("Last-Modified", c.Created)
	w.Header().Set("ETag", c.ETag)
	for k, v := range c.Metadata {
		w.Header().Set("x-ms-meta-"+k, v)
	}
	writeBlobLeaseHeaders(w, c.Lease, time.Now())
	if c.PublicAccess != "" {
		w.Header().Set("x-ms-blob-public-access", c.PublicAccess)
	}
	w.Header().Set("x-ms-has-immutability-policy", "false")
	w.Header().Set("x-ms-has-legal-hold", "false")
}

// blobListContainerProperties is the <Properties> element of one container in a
// List Containers response.
type blobListContainerProperties struct {
	LastModified           string `xml:"Last-Modified"`
	ETag                   string `xml:"Etag"`
	LeaseStatus            string `xml:"LeaseStatus,omitempty"`
	LeaseState             string `xml:"LeaseState,omitempty"`
	LeaseDuration          string `xml:"LeaseDuration,omitempty"`
	PublicAccess           string `xml:"PublicAccess,omitempty"`
	HasImmutabilityPolicy  bool   `xml:"HasImmutabilityPolicy"`
	HasLegalHold           bool   `xml:"HasLegalHold"`
	DeletedTime            string `xml:"DeletedTime,omitempty"`
	RemainingRetentionDays *int32 `xml:"RemainingRetentionDays,omitempty"`
}

type blobListContainerEntry struct {
	Name       string                      `xml:"Name"`
	Deleted    *bool                       `xml:"Deleted,omitempty"`
	Version    string                      `xml:"Version,omitempty"`
	Properties blobListContainerProperties `xml:"Properties"`
	Metadata   *blobMetadataElement        `xml:"Metadata,omitempty"`
}

// blobMetadataElement carries x-ms-meta-* pairs as the arbitrarily named child
// elements Azure's list responses use.
type blobMetadataElement struct {
	Entries []blobMetadataEntry `xml:",any"`
}

type blobMetadataEntry struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

func blobMetadataXML(m map[string]string) *blobMetadataElement {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	el := &blobMetadataElement{}
	for _, k := range keys {
		el.Entries = append(el.Entries, blobMetadataEntry{XMLName: xml.Name{Local: k}, Value: m[k]})
	}
	return el
}

func handleListContainers(w http.ResponseWriter, r *http.Request, account string) {
	type enum struct {
		XMLName         xml.Name                 `xml:"EnumerationResults"`
		ServiceEndpoint string                   `xml:"ServiceEndpoint,attr"`
		Prefix          string                   `xml:"Prefix,omitempty"`
		Containers      []blobListContainerEntry `xml:"Containers>Container"`
		NextMarker      string                   `xml:"NextMarker"`
	}
	q := r.URL.Query()
	include := blobListIncludeSet(q.Get("include"))
	reqPrefix := q.Get("prefix")
	now := time.Now()

	prefix := account + "/"
	var all []blobListContainerEntry
	for _, c := range blobContainersData.List() {
		if !strings.HasPrefix(blobContainerKey(c.Account, c.Name), prefix) {
			continue
		}
		if c.Deleted && !include["deleted"] {
			continue
		}
		if reqPrefix != "" && !strings.HasPrefix(c.Name, reqPrefix) {
			continue
		}
		state := blobLeaseState(c.Lease, now)
		entry := blobListContainerEntry{
			Name: c.Name,
			Properties: blobListContainerProperties{
				LastModified:  c.Created,
				ETag:          c.ETag,
				LeaseStatus:   blobLeaseStatus(state),
				LeaseState:    state,
				LeaseDuration: blobLeaseDurationType(c.Lease, state),
				PublicAccess:  c.PublicAccess,
			},
		}
		if c.Deleted {
			deleted := true
			retention := c.RemainingRetentionDays
			entry.Deleted = &deleted
			entry.Version = c.Version
			entry.Properties.DeletedTime = c.DeletedTime
			entry.Properties.RemainingRetentionDays = &retention
		}
		if include["metadata"] {
			entry.Metadata = blobMetadataXML(c.Metadata)
		}
		all = append(all, entry)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].Version < all[j].Version
	})

	page, marker := blobStoragePage(r, all, func(e blobListContainerEntry) string { return e.Name })
	out := enum{
		ServiceEndpoint: azureStorageEndpointURL(r, account, "blob"),
		Prefix:          reqPrefix,
		Containers:      page,
		NextMarker:      marker,
	}
	writeStorageXML(w, http.StatusOK, out)
}

// blobListIncludeSet parses the comma-separated `include=` list a List
// Containers / List Blobs request carries (snapshots, metadata, deleted, tags, …).
func blobListIncludeSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if v := strings.ToLower(strings.TrimSpace(part)); v != "" {
			out[v] = true
		}
	}
	return out
}

func handleListBlobs(w http.ResponseWriter, r *http.Request, account, container string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	type blobPrefixEntry struct {
		Name string `xml:"Name"`
	}
	type enum struct {
		XMLName         xml.Name          `xml:"EnumerationResults"`
		ServiceEndpoint string            `xml:"ServiceEndpoint,attr"`
		ContainerName   string            `xml:"ContainerName,attr"`
		Prefix          string            `xml:"Prefix,omitempty"`
		Delimiter       string            `xml:"Delimiter,omitempty"`
		Blobs           []blobListEntry   `xml:"Blobs>Blob"`
		BlobPrefixes    []blobPrefixEntry `xml:"Blobs>BlobPrefix"`
		NextMarker      string            `xml:"NextMarker"`
	}

	reqPrefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	include := blobListIncludeSet(r.URL.Query().Get("include"))

	var all []blobListEntry
	for _, b := range blobsInContainer(account, container) {
		if b.Snapshot != "" && !include["snapshots"] {
			continue
		}
		if b.Deleted && !include["deleted"] {
			continue
		}
		if reqPrefix != "" && !strings.HasPrefix(b.Name, reqPrefix) {
			continue
		}
		all = append(all, blobListEntryFor(b, include))
	}

	// With a delimiter, roll names that contain it (past the request prefix)
	// into virtual directories surfaced as <BlobPrefix> entries; only names
	// without a further delimiter are listed as blobs.
	var prefixEntries []blobPrefixEntry
	if delimiter != "" {
		seenPrefix := map[string]bool{}
		var flat []blobListEntry
		for _, be := range all {
			rest := strings.TrimPrefix(be.Name, reqPrefix)
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				virtual := reqPrefix + rest[:idx+len(delimiter)]
				if !seenPrefix[virtual] {
					seenPrefix[virtual] = true
					prefixEntries = append(prefixEntries, blobPrefixEntry{Name: virtual})
				}
				continue
			}
			flat = append(flat, be)
		}
		all = flat
		sort.Slice(prefixEntries, func(i, j int) bool { return prefixEntries[i].Name < prefixEntries[j].Name })
	}

	page, marker := blobStoragePage(r, all, func(e blobListEntry) string { return e.Name })
	out := enum{
		ServiceEndpoint: azureStorageEndpointURL(r, account, "blob"),
		ContainerName:   container,
		Prefix:          reqPrefix,
		Delimiter:       delimiter,
		Blobs:           page,
		BlobPrefixes:    prefixEntries,
		NextMarker:      marker,
	}
	writeStorageXML(w, http.StatusOK, out)
}

// azureBlobPreconditionOK validates the conditional headers against the current
// blob ETag (exists=false → no current blob). Writes a 412 ConditionNotMet and
// returns false on a failed precondition; an absent header always passes.
func azureBlobPreconditionOK(w http.ResponseWriter, r *http.Request, currentETag string, exists bool) bool {
	if inm := r.Header.Get("If-None-Match"); inm == "*" && exists {
		writeStorageError(w, "ConditionNotMet",
			"The condition specified using HTTP conditional header(s) is not met.", http.StatusPreconditionFailed)
		return false
	}
	if im := r.Header.Get("If-Match"); im != "" && im != "*" {
		if !exists || strings.Trim(im, `"`) != strings.Trim(currentETag, `"`) {
			writeStorageError(w, "ConditionNotMet",
				"The condition specified using HTTP conditional header(s) is not met.", http.StatusPreconditionFailed)
			return false
		}
	}
	return true
}

func handlePutBlob(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	existing, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if exists && existing.Deleted {
		exists = false
		existing = BlobObject{}
	}
	if !azureBlobPreconditionOK(w, r, existing.ETag, exists) {
		return
	}
	if !blobWriteAllowed(w, r, existing, exists) {
		return
	}
	blobType := r.Header.Get("x-ms-blob-type")
	if blobType == "" {
		blobType = "BlockBlob"
	}

	var data []byte
	switch blobType {
	case "PageBlob":
		// Create Page Blob declares the blob's size and writes no bytes: the
		// whole blob starts sparse.
		size, err := strconv.ParseInt(r.Header.Get("x-ms-blob-content-length"), 10, 64)
		if err != nil || size < 0 || size%blobPageSize != 0 {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-blob-content-length.",
				http.StatusBadRequest)
			return
		}
		data = make([]byte, size)
	case "AppendBlob":
		// Create Append Blob writes no bytes either; Append Block adds them.
		data = nil
	default:
		body, err := openStreamingBody(r)
		if err != nil {
			writeStorageError(w, "UnsupportedHttpVerb", err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		defer body.Close()
		data, err = io.ReadAll(body)
		if err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	}

	b := BlobObject{
		Account:      account,
		Container:    container,
		Name:         blob,
		Data:         data,
		BlobType:     blobType,
		CreationTime: blobNowHTTP(),
		Metadata:     collectMetadata(r),
		Tags:         parseBlobTagsHeader(r.Header.Get("x-ms-tags")),
		Lease:        existing.Lease,
	}
	applyBlobHTTPHeaders(&b, r)
	if b.ContentType == "" {
		b.ContentType = r.Header.Get("Content-Type")
	}
	if blobType == "PageBlob" {
		if seq, err := strconv.ParseInt(r.Header.Get("x-ms-blob-sequence-number"), 10, 64); err == nil {
			b.SequenceNumber = seq
		}
	}
	if tier := r.Header.Get("x-ms-access-tier"); tier != "" {
		b.AccessTier = tier
		b.AccessTierChangeTime = blobNowHTTP()
	} else {
		b.AccessTier = blobDefaultTier(blobType)
		b.AccessTierInferred = true
	}
	b.ContentMD5 = blobContentMD5(data)
	blobTouch(&b)
	putBlobObject(b)

	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("Content-MD5", b.ContentMD5)
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(http.StatusCreated)
}

// blobDefaultTier is the access tier Azure infers for a blob whose upload did
// not name one. Page blobs live on the premium/page tier scale and report none.
func blobDefaultTier(blobType string) string {
	if blobType == "PageBlob" {
		return ""
	}
	return "Hot"
}

// applyBlobHTTPHeaders folds the x-ms-blob-* system-property headers of a write
// request into the stored record. They are the same header set Put Blob and
// Set Blob HTTP Headers both carry.
func applyBlobHTTPHeaders(b *BlobObject, r *http.Request) {
	b.ContentType = r.Header.Get("x-ms-blob-content-type")
	b.ContentEncoding = r.Header.Get("x-ms-blob-content-encoding")
	b.ContentLanguage = r.Header.Get("x-ms-blob-content-language")
	b.ContentDisposition = r.Header.Get("x-ms-blob-content-disposition")
	b.CacheControl = r.Header.Get("x-ms-blob-cache-control")
	if md5 := r.Header.Get("x-ms-blob-content-md5"); md5 != "" {
		b.ContentMD5 = md5
	}
}

func handleCopyBlob(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	sourceURL := r.Header.Get("x-ms-copy-source")
	srcAccount, srcContainer, srcBlob, ok := parseBlobCopySource(sourceURL)
	if !ok {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source is invalid.", http.StatusNotFound)
		return
	}
	source, ok := blobObjects.Get(blobSnapshotKey(srcAccount, srcContainer, srcBlob, blobCopySourceSnapshot(sourceURL)))
	if !ok || source.Deleted {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source does not exist.", http.StatusNotFound)
		return
	}

	existing, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if exists && existing.Deleted {
		exists = false
		existing = BlobObject{}
	}
	if !blobWriteAllowed(w, r, existing, exists) {
		return
	}

	data := append([]byte(nil), source.Data...)
	metadata := collectMetadata(r)
	if len(metadata) == 0 {
		metadata = cloneBlobMetadata(source.Metadata)
	}
	copyID := generateUUID()
	completion := blobNowHTTP()
	dst := BlobObject{
		Account:            account,
		Container:          container,
		Name:               blob,
		Data:               data,
		ContentType:        source.ContentType,
		ContentEncoding:    source.ContentEncoding,
		ContentLanguage:    source.ContentLanguage,
		ContentDisposition: source.ContentDisposition,
		CacheControl:       source.CacheControl,
		ContentMD5:         blobContentMD5(data),
		BlobType:           source.BlobType,
		CreationTime:       completion,
		Metadata:           metadata,
		Tags:               parseBlobTagsHeader(r.Header.Get("x-ms-tags")),
		PageRanges:         append([]BlobPageRange(nil), source.PageRanges...),
		SequenceNumber:     source.SequenceNumber,
		AccessTier:         source.AccessTier,
		AccessTierInferred: source.AccessTierInferred,
		Lease:              existing.Lease,
		CopyID:             copyID,
		CopyStatus:         "success",
		CopySource:         sourceURL,
		CopyProgress:       fmt.Sprintf("%d/%d", len(data), len(data)),
		CopyCompletionTime: completion,
	}
	if dst.Tags == nil {
		dst.Tags = cloneBlobMetadata(source.Tags)
	}
	if tier := r.Header.Get("x-ms-access-tier"); tier != "" {
		dst.AccessTier = tier
		dst.AccessTierInferred = false
		dst.AccessTierChangeTime = completion
	}
	blobTouch(&dst)
	putBlobObject(dst)
	w.Header().Set("ETag", dst.ETag)
	w.Header().Set("Last-Modified", dst.LastModified)
	w.Header().Set("x-ms-copy-id", copyID)
	w.Header().Set("x-ms-copy-status", "success")
	// Copy Blob From URL is the synchronous spelling: it carries
	// x-ms-requires-sync and answers with the copied content's MD5.
	if strings.EqualFold(r.Header.Get("x-ms-requires-sync"), "true") {
		w.Header().Set("Content-MD5", dst.ContentMD5)
	}
	w.WriteHeader(http.StatusAccepted)
}

// blobCopySourceSnapshot reads the `?snapshot=` a copy source URL may carry, so
// copying from a snapshot copies the snapshot's bytes.
func blobCopySourceSnapshot(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Query().Get("snapshot")
}

func parseBlobCopySource(raw string) (account, container, blob string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", false
	}
	host := u.Hostname()
	escapedPath := strings.TrimPrefix(u.EscapedPath(), "/")
	if escapedPath == "" {
		return "", "", "", false
	}
	if parts := strings.SplitN(host, ".blob.", 2); len(parts) == 2 {
		account, ok = pathUnescape(parts[0])
		if !ok {
			return "", "", "", false
		}
		container, blob, ok = splitContainerBlobPath(escapedPath)
		return account, container, blob, ok
	}
	accountEsc, rest, found := strings.Cut(escapedPath, "/")
	if !found {
		return "", "", "", false
	}
	account, ok = pathUnescape(accountEsc)
	if !ok {
		return "", "", "", false
	}
	container, blob, ok = splitContainerBlobPath(rest)
	return account, container, blob, ok
}

func splitContainerBlobPath(escapedPath string) (container, blob string, ok bool) {
	containerEsc, blobEsc, found := strings.Cut(escapedPath, "/")
	if !found || blobEsc == "" {
		return "", "", false
	}
	container, ok = pathUnescape(containerEsc)
	if !ok {
		return "", "", false
	}
	blob, ok = pathUnescape(blobEsc)
	if !ok {
		return "", "", false
	}
	return container, blob, true
}

func pathUnescape(s string) (string, bool) {
	out, err := url.PathUnescape(s)
	if err != nil {
		return "", false
	}
	return out, true
}

func cloneBlobMetadata(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func handleStageBlock(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	blockID := r.URL.Query().Get("blockid")
	if blockID == "" {
		writeStorageError(w, "MissingRequiredQueryParameter",
			"A query parameter that's mandatory for this request is not specified: blockid.",
			http.StatusBadRequest)
		return
	}
	if existing, ok := blobObjects.Get(blobObjectKey(account, container, blob)); ok {
		if !blobLeaseAccessOK(w, r, existing.Lease, "blob") {
			return
		}
	}
	body, err := openStreamingBody(r)
	if err != nil {
		writeStorageError(w, "UnsupportedHttpVerb", err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	key := blobBlockKey(account, container, blob, blockID)
	block, _ := blobBlocks.Get(key)
	block.Account = account
	block.Container = container
	block.Blob = blob
	block.BlockID = blockID
	block.UncommittedData = data
	block.HasUncommitted = true
	putBlobBlock(account, container, blob, blockID, block)
	w.WriteHeader(http.StatusCreated)
}

type blockListRequest struct {
	XMLName     xml.Name `xml:"BlockList"`
	Committed   []string `xml:"Committed"`
	Latest      []string `xml:"Latest"`
	Uncommitted []string `xml:"Uncommitted"`
}

func handleCommitBlockList(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	priorBlob, priorExists := blobObjects.Get(blobObjectKey(account, container, blob))
	if priorExists && priorBlob.Deleted {
		priorExists, priorBlob = false, BlobObject{}
	}
	if !blobWriteAllowed(w, r, priorBlob, priorExists) {
		return
	}
	defer r.Body.Close()
	var req blockListRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	refs := make([]blockRef, 0, len(req.Committed)+len(req.Latest)+len(req.Uncommitted))
	for _, id := range req.Committed {
		block, ok := blobBlocks.Get(blobBlockKey(account, container, blob, id))
		if !ok || !block.HasCommitted {
			writeStorageError(w, "InvalidBlockList",
				"The specified block list is invalid.", http.StatusBadRequest)
			return
		}
		refs = append(refs, blockRef{id: id, data: block.CommittedData})
	}
	for _, id := range req.Latest {
		block, ok := blobBlocks.Get(blobBlockKey(account, container, blob, id))
		if !ok || (!block.HasUncommitted && !block.HasCommitted) {
			writeStorageError(w, "InvalidBlockList",
				"The specified block list is invalid.", http.StatusBadRequest)
			return
		}
		data := block.CommittedData
		if block.HasUncommitted {
			data = block.UncommittedData
		}
		refs = append(refs, blockRef{id: id, data: data})
	}
	for _, id := range req.Uncommitted {
		block, ok := blobBlocks.Get(blobBlockKey(account, container, blob, id))
		if !ok || !block.HasUncommitted {
			writeStorageError(w, "InvalidBlockList",
				"The specified block list is invalid.", http.StatusBadRequest)
			return
		}
		refs = append(refs, blockRef{id: id, data: block.UncommittedData})
	}

	var data []byte
	committed := map[string]blockRef{}
	for _, ref := range refs {
		data = append(data, ref.data...)
		committed[ref.id] = ref
	}
	for _, key := range blockKeysForBlob(account, container, blob) {
		block, ok := blobBlocks.Get(key)
		if !ok {
			continue
		}
		ref, keepCommitted := committed[block.BlockID]
		block.HasCommitted = keepCommitted
		if keepCommitted {
			block.CommittedData = ref.data
			block.CommitOrdinal = indexBlockRef(refs, block.BlockID)
			block.HasUncommitted = false
			block.UncommittedData = nil
		}
		if !block.HasCommitted && !block.HasUncommitted {
			deleteBlobBlock(block.Account, block.Container, block.Blob, block.BlockID)
			continue
		}
		putBlobBlock(block.Account, block.Container, block.Blob, block.BlockID, block)
	}
	for idx, ref := range refs {
		key := blobBlockKey(account, container, blob, ref.id)
		block, _ := blobBlocks.Get(key)
		block.Account = account
		block.Container = container
		block.Blob = blob
		block.BlockID = ref.id
		block.CommittedData = ref.data
		block.HasCommitted = true
		block.HasUncommitted = false
		block.UncommittedData = nil
		block.CommitOrdinal = idx
		putBlobBlock(account, container, blob, ref.id, block)
	}

	committedBlob := BlobObject{
		Account:            account,
		Container:          container,
		Name:               blob,
		Data:               data,
		BlobType:           "BlockBlob",
		CreationTime:       blobNowHTTP(),
		Metadata:           collectMetadata(r),
		Tags:               parseBlobTagsHeader(r.Header.Get("x-ms-tags")),
		ContentMD5:         blobContentMD5(data),
		AccessTier:         "Hot",
		AccessTierInferred: true,
		Lease:              priorBlob.Lease,
	}
	applyBlobHTTPHeaders(&committedBlob, r)
	if committedBlob.ContentType == "" {
		committedBlob.ContentType = r.Header.Get("Content-Type")
	}
	if tier := r.Header.Get("x-ms-access-tier"); tier != "" {
		committedBlob.AccessTier = tier
		committedBlob.AccessTierInferred = false
		committedBlob.AccessTierChangeTime = blobNowHTTP()
	}
	blobTouch(&committedBlob)
	putBlobObject(committedBlob)
	w.Header().Set("ETag", committedBlob.ETag)
	w.Header().Set("Last-Modified", committedBlob.LastModified)
	w.Header().Set("Content-MD5", committedBlob.ContentMD5)
	w.WriteHeader(http.StatusCreated)
}

func indexBlockRef(refs []blockRef, id string) int {
	for i, ref := range refs {
		if ref.id == id {
			return i
		}
	}
	return 0
}

func handleGetBlockList(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	listType := r.URL.Query().Get("blocklisttype")
	if listType == "" {
		listType = "committed"
	}
	type blockEntry struct {
		Name string `xml:"Name"`
		Size int64  `xml:"Size"`
	}
	type blockList struct {
		XMLName           xml.Name     `xml:"BlockList"`
		CommittedBlocks   []blockEntry `xml:"CommittedBlocks>Block"`
		UncommittedBlocks []blockEntry `xml:"UncommittedBlocks>Block"`
	}
	out := blockList{}
	for _, key := range blockKeysForBlob(account, container, blob) {
		block, ok := blobBlocks.Get(key)
		if !ok {
			continue
		}
		if (listType == "committed" || listType == "all") && block.HasCommitted {
			out.CommittedBlocks = append(out.CommittedBlocks, blockEntry{
				Name: block.BlockID,
				Size: int64(len(block.CommittedData)),
			})
		}
		if (listType == "uncommitted" || listType == "all") && block.HasUncommitted {
			out.UncommittedBlocks = append(out.UncommittedBlocks, blockEntry{
				Name: block.BlockID,
				Size: int64(len(block.UncommittedData)),
			})
		}
	}
	sort.Slice(out.CommittedBlocks, func(i, j int) bool {
		left, _ := blobBlocks.Get(blobBlockKey(account, container, blob, out.CommittedBlocks[i].Name))
		right, _ := blobBlocks.Get(blobBlockKey(account, container, blob, out.CommittedBlocks[j].Name))
		return left.CommitOrdinal < right.CommitOrdinal
	})
	sort.Slice(out.UncommittedBlocks, func(i, j int) bool {
		return out.UncommittedBlocks[i].Name < out.UncommittedBlocks[j].Name
	})
	writeStorageXML(w, http.StatusOK, out)
}

func handleGetBlob(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := lookupBlob(r, account, container, blob)
	if !ok {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	start, end, partial, ok := azureStorageReadRange(w, r, int64(len(b.Data)))
	if !ok {
		return // azureStorageReadRange has written the error.
	}
	writeBlobHeaders(w, b)
	if !partial {
		_, _ = w.Write(b.Data)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(b.Data)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(b.Data[start : end+1])
}

// azureStorageReadRange resolves the byte range a storage read requests. Azure
// Storage reads carry the range in x-ms-range (which takes precedence) or the
// standard Range header, and answer a ranged request with 206 Partial Content
// plus Content-Range — clients such as the az CLI's `storage blob download`
// require that header and fail outright on a 200 carrying the whole blob.
//
// It returns partial=false when the request asks for the whole resource. On a
// malformed or unsatisfiable range it writes the storage error itself and
// returns ok=false.
func azureStorageReadRange(w http.ResponseWriter, r *http.Request, size int64) (start, end int64, partial, ok bool) {
	raw := r.Header.Get("x-ms-range")
	if raw == "" {
		raw = r.Header.Get("Range")
	}
	if raw == "" {
		return 0, 0, false, true
	}
	spec, found := strings.CutPrefix(strings.TrimSpace(raw), "bytes=")
	if !found {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: Range.",
			http.StatusBadRequest)
		return 0, 0, false, false
	}
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: Range.",
			http.StatusBadRequest)
		return 0, 0, false, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(lo), 10, 64)
	if err != nil || start < 0 {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: Range.",
			http.StatusBadRequest)
		return 0, 0, false, false
	}
	// An open-ended "bytes=start-" runs to the end of the resource.
	end = size - 1
	if trimmed := strings.TrimSpace(hi); trimmed != "" {
		end, err = strconv.ParseInt(trimmed, 10, 64)
		if err != nil || end < start {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: Range.",
				http.StatusBadRequest)
			return 0, 0, false, false
		}
	}
	// Azure clamps a range that runs past the resource rather than failing; only
	// a start beyond the end is unsatisfiable.
	if start >= size && size > 0 {
		writeStorageError(w, "InvalidRange",
			"The range specified is invalid for the current size of the resource.",
			http.StatusRequestedRangeNotSatisfiable)
		return 0, 0, false, false
	}
	if end > size-1 {
		end = size - 1
	}
	if size == 0 {
		return 0, 0, false, true
	}
	return start, end, true, true
}

func handleHeadBlob(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := lookupBlob(r, account, container, blob)
	if !ok {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	writeBlobHeaders(w, b)
}

func handleDeleteBlob(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	snapshot := r.URL.Query().Get("snapshot")
	existing, exists := blobObjects.Get(blobSnapshotKey(account, container, blob, snapshot))
	if !exists || existing.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if !azureBlobPreconditionOK(w, r, existing.ETag, true) {
		return
	}
	if !blobWriteAllowed(w, r, existing, true) {
		return
	}

	// x-ms-delete-snapshots decides what happens to a base blob that still has
	// snapshots: `include` deletes them with it, `only` deletes just them, and
	// omitting the header on a blob that has snapshots is an error.
	deleteSnapshots := strings.ToLower(r.Header.Get("x-ms-delete-snapshots"))
	var snapshots []BlobObject
	if snapshot == "" {
		for _, s := range blobsInContainer(account, container) {
			if s.Name == blob && s.Snapshot != "" && !s.Deleted {
				snapshots = append(snapshots, s)
			}
		}
		if len(snapshots) > 0 && deleteSnapshots == "" {
			writeStorageError(w, "SnapshotsPresent",
				"This operation is not permitted because the blob has snapshots.",
				http.StatusConflict)
			return
		}
	}

	days, soft := blobSoftDeleteDays(account)
	removeOne := func(b BlobObject) {
		if soft {
			b.Deleted = true
			b.DeletedTime = blobNowHTTP()
			b.RemainingRetentionDays = days
			b.Lease = BlobLease{}
			putBlobObject(b)
			return
		}
		deleteBlobSnapshot(b.Account, b.Container, b.Name, b.Snapshot)
		if b.Snapshot == "" {
			for _, key := range blockKeysForBlob(b.Account, b.Container, b.Name) {
				if block, ok := blobBlocks.Get(key); ok {
					deleteBlobBlock(block.Account, block.Container, block.Blob, block.BlockID)
				}
			}
		}
	}

	if snapshot == "" && deleteSnapshots == "only" {
		for _, s := range snapshots {
			removeOne(s)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if snapshot == "" {
		for _, s := range snapshots {
			removeOne(s)
		}
	}
	removeOne(existing)
	w.WriteHeader(http.StatusAccepted)
}

func writeBlobHeaders(w http.ResponseWriter, b BlobObject) {
	if b.ContentType != "" {
		w.Header().Set("Content-Type", b.ContentType)
	}
	if b.ContentEncoding != "" {
		w.Header().Set("Content-Encoding", b.ContentEncoding)
	}
	if b.ContentLanguage != "" {
		w.Header().Set("Content-Language", b.ContentLanguage)
	}
	if b.ContentDisposition != "" {
		w.Header().Set("Content-Disposition", b.ContentDisposition)
	}
	if b.CacheControl != "" {
		w.Header().Set("Cache-Control", b.CacheControl)
	}
	if b.ContentMD5 != "" {
		w.Header().Set("Content-MD5", b.ContentMD5)
	}
	if b.BlobType != "" {
		w.Header().Set("x-ms-blob-type", b.BlobType)
	}
	if b.CreationTime != "" {
		w.Header().Set("x-ms-creation-time", b.CreationTime)
	}
	if b.CopyID != "" {
		w.Header().Set("x-ms-copy-id", b.CopyID)
	}
	if b.CopyStatus != "" {
		w.Header().Set("x-ms-copy-status", b.CopyStatus)
	}
	if b.CopySource != "" {
		w.Header().Set("x-ms-copy-source", b.CopySource)
	}
	if b.CopyProgress != "" {
		w.Header().Set("x-ms-copy-progress", b.CopyProgress)
	}
	if b.CopyCompletionTime != "" {
		w.Header().Set("x-ms-copy-completion-time", b.CopyCompletionTime)
	}
	if b.CopyDestinationSnapshot != "" {
		w.Header().Set("x-ms-copy-destination-snapshot", b.CopyDestinationSnapshot)
	}
	if b.IncrementalCopy {
		w.Header().Set("x-ms-incremental-copy", "true")
	}
	if b.AccessTier != "" {
		w.Header().Set("x-ms-access-tier", b.AccessTier)
	}
	if b.AccessTierInferred {
		w.Header().Set("x-ms-access-tier-inferred", "true")
	}
	if b.AccessTierChangeTime != "" {
		w.Header().Set("x-ms-access-tier-change-time", b.AccessTierChangeTime)
	}
	if b.ExpiresOn != "" {
		w.Header().Set("x-ms-expiry-time", b.ExpiresOn)
	}
	if b.BlobType == "PageBlob" {
		w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	}
	if b.BlobType == "AppendBlob" {
		w.Header().Set("x-ms-blob-committed-block-count", strconv.FormatInt(int64(b.CommittedBlockCount), 10))
		if b.Sealed {
			w.Header().Set("x-ms-blob-sealed", "true")
		}
	}
	if b.ImmutabilityPolicyExpiry != "" {
		w.Header().Set("x-ms-immutability-policy-until-date", b.ImmutabilityPolicyExpiry)
		w.Header().Set("x-ms-immutability-policy-mode", b.ImmutabilityPolicyMode)
	}
	if b.LegalHold {
		w.Header().Set("x-ms-legal-hold", "true")
	}
	if n := len(b.Tags); n > 0 {
		w.Header().Set("x-ms-tag-count", strconv.Itoa(n))
	}
	if b.Snapshot != "" {
		w.Header().Set("x-ms-snapshot", b.Snapshot)
	}
	writeBlobLeaseHeaders(w, b.Lease, time.Now())
	w.Header().Set("x-ms-server-encrypted", "true")
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(b.Data)))
	w.Header().Set("Accept-Ranges", "bytes")
	for k, v := range b.Metadata {
		w.Header().Set("x-ms-meta-"+k, v)
	}
}

func collectMetadata(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-ms-meta-") && len(v) > 0 {
			out[strings.TrimPrefix(lk, "x-ms-meta-")] = v[0]
		}
	}
	return out
}
