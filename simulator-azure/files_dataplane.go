package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure Files data plane — shares, share snapshots, share leases, stored
// permissions, and the service-level operations.
//
// The bytes of every share live in a real directory tree under
// azureFilesHostRoot(); the directories and files below a share are ordinary
// directories and files, which is what makes a share usable both through this
// REST surface and as the host path a Container Apps / Azure Functions workload
// bind-mounts for a Volume{StorageType: AzureFile}. Everything a POSIX
// directory tree cannot express — user metadata, leases, security descriptors,
// snapshot identity, soft-delete state — lives in the simulator's persistent
// stores so it survives a restart exactly as the bytes do.
//
// Wire reference: https://learn.microsoft.com/rest/api/storageservices/file-service-rest-api

// FileShareSnapshotData is one point-in-time snapshot of a share. The
// snapshot's contents are a real copy of the share's directory tree taken when
// the snapshot was created, so reads addressed with `?sharesnapshot=<id>` see
// the bytes as they were then rather than the share's current bytes.
type FileShareSnapshotData struct {
	Account  string
	Share    string
	Snapshot string // ISO 8601 identifier, the value of x-ms-snapshot
	Created  string
	Quota    int
	Metadata map[string]string
	ETag     string
}

// FileDirectoryData carries the properties of one directory in a share that a
// POSIX directory cannot express: its user-defined metadata, the key of the
// security descriptor assigned to it, and the moment the data plane created it.
type FileDirectoryData struct {
	Account       string
	Share         string
	Path          string // forward-slash separated; "" is the share root
	Metadata      map[string]string
	PermissionKey string
	Created       string
}

// FileLeaseData is a lease held on a share or on a single file. Path is empty
// for a share lease. A lease with a finite duration expires on its own; the
// simulator resolves that when the lease is next read, which is when the
// expiry can first make a difference to a caller.
type FileLeaseData struct {
	Account   string
	Share     string
	Path      string
	LeaseID   string
	Duration  int   // seconds; -1 is an infinite lease
	ExpiresAt int64 // Unix seconds; 0 for an infinite lease
	BreakAt   int64 // Unix seconds a breaking lease becomes broken; 0 when not breaking
}

// FilePermissionData is a security descriptor stored on a share by Create
// Permission. The key is derived from the descriptor itself, so storing the
// same descriptor twice yields the same key — the behavior real Azure Files
// has, and what lets a client cache a key across calls.
type FilePermissionData struct {
	Account    string
	Share      string
	Key        string
	Permission string
	Format     string
}

// FilesServiceConfig is the account's File service properties document as Set
// Service Properties stored it. Get Service Properties reads back exactly what
// was written.
type FilesServiceConfig struct {
	Account    string
	Properties filesServiceProperties
	Configured bool
}

// FileDeletedShareData is a share that Delete Share soft-deleted because the
// account's File service carries a share delete-retention policy. Its contents
// stay on disk under the account's deleted-share root until Restore Share puts
// them back.
type FileDeletedShareData struct {
	Account  string
	Share    string
	Version  string
	Deleted  string
	Quota    int
	Metadata map[string]string
	ACLs     []TableSignedIdentifier
}

var (
	fileDirectories     sim.Store[FileDirectoryData]
	fileShareSnapshots  sim.Store[FileShareSnapshotData]
	fileLeases          sim.Store[FileLeaseData]
	filePermissions     sim.Store[FilePermissionData]
	filesServiceConfigs sim.Store[FilesServiceConfig]
	fileDeletedShares   sim.Store[FileDeletedShareData]
)

// registerFilesDataPlaneStores opens every persistent store the Files data
// plane keeps beyond the share and file rows storage_dataplane.go opens.
func registerFilesDataPlaneStores(srv *sim.Server) {
	fileDirectories = sim.MakeStore[FileDirectoryData](srv.DB(), "file_directories")
	fileShareSnapshots = sim.MakeStore[FileShareSnapshotData](srv.DB(), "file_share_snapshots")
	fileLeases = sim.MakeStore[FileLeaseData](srv.DB(), "file_leases")
	filePermissions = sim.MakeStore[FilePermissionData](srv.DB(), "file_permissions")
	filesServiceConfigs = sim.MakeStore[FilesServiceConfig](srv.DB(), "file_service_properties")
	fileDeletedShares = sim.MakeStore[FileDeletedShareData](srv.DB(), "file_deleted_shares")
}

// ── Store keys ──────────────────────────────────────────────────────

func fileDirectoryKey(account, share, dirPath string) string {
	return account + "/" + share + "/" + dirPath
}

func fileLeaseKey(account, share, entryPath string) string {
	return account + "/" + share + "/" + entryPath
}

func filePermissionKey(account, share, key string) string {
	return account + "/" + share + "/" + key
}

func fileShareSnapshotKey(account, share, snapshot string) string {
	return account + "/" + share + "/" + snapshot
}

func fileDeletedShareKey(account, share, version string) string {
	return account + "/" + share + "/" + version
}

// Every Azure Files row is keyed `account/share/...`, so one index per store
// under each path prefix answers "everything this share owns" — which a share
// delete, a directory rename and a share listing all ask — without decoding
// every other share's rows.
var (
	fileObjectsByPrefix        sim.GenerationIndex[FileObject]
	fileDirectoriesByPrefix    sim.GenerationIndex[FileDirectoryData]
	fileLeasesByPrefix         sim.GenerationIndex[FileLeaseData]
	filePermissionsByPrefix    sim.GenerationIndex[FilePermissionData]
	fileShareSnapshotsByPrefix sim.GenerationIndex[FileShareSnapshotData]
	fileDeletedSharesByPrefix  sim.GenerationIndex[FileDeletedShareData]
)

// filesSharePrefix names everything one share owns.
func filesSharePrefix(account, share string) string { return account + "/" + share + "/" }

func fileObjectsUnder(prefix string) []FileObject {
	return fileObjectsByPrefix.LookupAll(fileObjects, prefix, func(f FileObject) []string {
		return sim.PathPrefixes(fileObjectKey(f.Account, f.Share, f.Path))
	})
}

func fileDirectoriesUnder(prefix string) []FileDirectoryData {
	return fileDirectoriesByPrefix.LookupAll(fileDirectories, prefix, func(d FileDirectoryData) []string {
		return sim.PathPrefixes(fileDirectoryKey(d.Account, d.Share, d.Path))
	})
}

func fileLeasesUnder(prefix string) []FileLeaseData {
	return fileLeasesByPrefix.LookupAll(fileLeases, prefix, func(l FileLeaseData) []string {
		return sim.PathPrefixes(fileLeaseKey(l.Account, l.Share, l.Path))
	})
}

func filePermissionsUnder(prefix string) []FilePermissionData {
	return filePermissionsByPrefix.LookupAll(filePermissions, prefix, func(p FilePermissionData) []string {
		return sim.PathPrefixes(filePermissionKey(p.Account, p.Share, p.Key))
	})
}

func fileShareSnapshotsUnder(prefix string) []FileShareSnapshotData {
	return fileShareSnapshotsByPrefix.LookupAll(fileShareSnapshots, prefix, func(snap FileShareSnapshotData) []string {
		return sim.PathPrefixes(fileShareSnapshotKey(snap.Account, snap.Share, snap.Snapshot))
	})
}

func fileDeletedSharesUnder(prefix string) []FileDeletedShareData {
	return fileDeletedSharesByPrefix.LookupAll(fileDeletedShares, prefix, func(d FileDeletedShareData) []string {
		return sim.PathPrefixes(fileDeletedShareKey(d.Account, d.Share, d.Version))
	})
}

// ── Backing directories ─────────────────────────────────────────────

// fileShareSnapshotRoot is the directory holding every snapshot of every share
// on an account. It sits beside the account's share directories rather than
// inside one: '@' cannot appear in an Azure storage account name, so no share
// or account directory can ever collide with it.
func fileShareSnapshotRoot(account string) string {
	return filepath.Join(azureFilesHostRoot(), account+"@snapshots")
}

func fileShareSnapshotHostDir(account, share, snapshot string) string {
	return filepath.Join(fileShareSnapshotRoot(account), share, snapshot)
}

// fileDeletedShareRoot holds the contents of soft-deleted shares, for the same
// reason and with the same collision-proof spelling as the snapshot root.
func fileDeletedShareRoot(account string) string {
	return filepath.Join(azureFilesHostRoot(), account+"@deleted")
}

func fileDeletedShareHostDir(account, share, version string) string {
	return filepath.Join(fileDeletedShareRoot(account), share, version)
}

// filesShareRootDir resolves the directory a request reads from: the share's
// own backing directory, or the copy a snapshot froze when the request carries
// `?sharesnapshot=`. It reports the Azure error and false when the share or the
// named snapshot does not exist.
func filesShareRootDir(w http.ResponseWriter, r *http.Request, account, share string) (string, bool) {
	snapshot := r.URL.Query().Get("sharesnapshot")
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return "", false
	}
	if !isStoragePathSegment(account) || !isStoragePathSegment(share) {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return "", false
	}
	if snapshot == "" {
		return FileShareHostDir(account, share), true
	}
	if !isStoragePathSegment(snapshot) {
		writeStorageError(w, "InvalidQueryParameterValue",
			"Value for one of the query parameters specified in the request URI is invalid: sharesnapshot.",
			http.StatusBadRequest)
		return "", false
	}
	if _, ok := fileShareSnapshots.Get(fileShareSnapshotKey(account, share, snapshot)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return "", false
	}
	return fileShareSnapshotHostDir(account, share, snapshot), true
}

// filesEntryHostPath joins a share-relative entry path onto a resolved share
// root, refusing any name that would resolve outside it.
func filesEntryHostPath(root, entryPath string) (string, bool) {
	if entryPath == "" {
		return root, true
	}
	segments := strings.Split(entryPath, "/")
	for _, segment := range segments {
		if !isStoragePathSegment(segment) {
			return "", false
		}
	}
	return filepath.Join(root, filepath.Join(segments...)), true
}

// filesCopyTree copies a directory tree byte for byte, which is what taking a
// share snapshot and soft-deleting a share both do to a share's contents.
func filesCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o777)
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
			if err != nil {
				return err
			}
			defer out.Close()
			if _, err := io.Copy(out, in); err != nil {
				return err
			}
			return os.Chmod(target, 0o666)
		}
	})
}

// ── Leases ──────────────────────────────────────────────────────────

// filesActiveLease returns the lease currently held on a share or file, having
// first retired a lease whose duration has run out or whose break period has
// elapsed. A lease the service has let go of is not a lease.
func filesActiveLease(account, share, entryPath string) (FileLeaseData, bool) {
	key := fileLeaseKey(account, share, entryPath)
	lease, ok := fileLeases.Get(key)
	if !ok {
		return FileLeaseData{}, false
	}
	now := time.Now().Unix()
	if lease.ExpiresAt != 0 && now >= lease.ExpiresAt {
		fileLeases.Delete(key)
		return FileLeaseData{}, false
	}
	if lease.BreakAt != 0 && now >= lease.BreakAt {
		fileLeases.Delete(key)
		return FileLeaseData{}, false
	}
	return lease, true
}

// filesLeaseState names the lease state Azure reports on the resource: a lease
// counting down to a break is "breaking", any other live lease is "leased", and
// a resource with no lease is "available".
func filesLeaseState(lease FileLeaseData, held bool) string {
	switch {
	case !held:
		return "available"
	case lease.BreakAt != 0:
		return "breaking"
	default:
		return "leased"
	}
}

// filesRequireLease enforces a lease on a write. A leased resource accepts a
// write only from the holder of the lease id, exactly as Azure Files requires;
// `resource` selects between the file-operation and share-operation mismatch
// codes the service distinguishes.
func filesRequireLease(w http.ResponseWriter, r *http.Request, account, share, entryPath, resource string) bool {
	lease, held := filesActiveLease(account, share, entryPath)
	if !held {
		return true
	}
	supplied := r.Header.Get("x-ms-lease-id")
	if supplied == "" {
		writeStorageError(w, "LeaseIdMissing",
			"There is currently a lease on the resource and no lease ID was specified in the request.",
			http.StatusPreconditionFailed)
		return false
	}
	if !strings.EqualFold(supplied, lease.LeaseID) {
		code := "LeaseIdMismatchWithFileOperation"
		if resource == "share" {
			code = "LeaseIdMismatchWithShareOperation"
		}
		writeStorageError(w, code,
			"The lease ID specified did not match the lease ID for the resource.",
			http.StatusPreconditionFailed)
		return false
	}
	return true
}

// filesHandleLease serves the five lease actions Azure Files documents for both
// shares and files. The action arrives in x-ms-lease-action; the Swagger's
// `&acquire` / `&release` query spellings are a documentation device for the
// generated clients, not something a request carries.
func filesHandleLease(w http.ResponseWriter, r *http.Request, account, share, entryPath, resource string, etag, lastModified string) {
	action := strings.ToLower(strings.TrimSpace(r.Header.Get("x-ms-lease-action")))
	if action == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-lease-action.",
			http.StatusBadRequest)
		return
	}
	key := fileLeaseKey(account, share, entryPath)
	lease, held := filesActiveLease(account, share, entryPath)
	supplied := r.Header.Get("x-ms-lease-id")

	setCommonHeaders := func() {
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", lastModified)
	}

	switch action {
	case "acquire":
		duration := -1
		if raw := r.Header.Get("x-ms-lease-duration"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || (n != -1 && (n < 15 || n > 60)) {
				writeStorageError(w, "InvalidHeaderValue",
					"The value for one of the HTTP headers is not in the correct format: x-ms-lease-duration.",
					http.StatusBadRequest)
				return
			}
			duration = n
		}
		proposed := r.Header.Get("x-ms-proposed-lease-id")
		if held {
			if proposed == "" || !strings.EqualFold(proposed, lease.LeaseID) {
				writeStorageError(w, "LeaseAlreadyPresent",
					"There is already a lease present.", http.StatusConflict)
				return
			}
		}
		if proposed == "" {
			proposed = generateUUID()
		}
		newLease := FileLeaseData{
			Account: account, Share: share, Path: entryPath,
			LeaseID: proposed, Duration: duration,
		}
		if duration > 0 {
			newLease.ExpiresAt = time.Now().Add(time.Duration(duration) * time.Second).Unix()
		}
		fileLeases.Put(key, newLease)
		setCommonHeaders()
		w.Header().Set("x-ms-lease-id", newLease.LeaseID)
		w.WriteHeader(http.StatusCreated)
	case "renew":
		if !held {
			writeStorageError(w, "LeaseNotPresentWithLeaseOperation",
				"There is currently no lease on the resource.", http.StatusConflict)
			return
		}
		if !strings.EqualFold(supplied, lease.LeaseID) {
			writeStorageError(w, "LeaseIdMismatchWithLeaseOperation",
				"The lease ID specified did not match the lease ID for the resource.", http.StatusConflict)
			return
		}
		lease.BreakAt = 0
		if lease.Duration > 0 {
			lease.ExpiresAt = time.Now().Add(time.Duration(lease.Duration) * time.Second).Unix()
		}
		fileLeases.Put(key, lease)
		setCommonHeaders()
		w.Header().Set("x-ms-lease-id", lease.LeaseID)
		w.WriteHeader(http.StatusOK)
	case "change":
		if !held {
			writeStorageError(w, "LeaseNotPresentWithLeaseOperation",
				"There is currently no lease on the resource.", http.StatusConflict)
			return
		}
		if !strings.EqualFold(supplied, lease.LeaseID) {
			writeStorageError(w, "LeaseIdMismatchWithLeaseOperation",
				"The lease ID specified did not match the lease ID for the resource.", http.StatusConflict)
			return
		}
		proposed := r.Header.Get("x-ms-proposed-lease-id")
		if proposed == "" {
			writeStorageError(w, "MissingRequiredHeader",
				"An HTTP header that's mandatory for this request is not specified: x-ms-proposed-lease-id.",
				http.StatusBadRequest)
			return
		}
		lease.LeaseID = proposed
		fileLeases.Put(key, lease)
		setCommonHeaders()
		w.Header().Set("x-ms-lease-id", lease.LeaseID)
		w.WriteHeader(http.StatusOK)
	case "release":
		if !held {
			writeStorageError(w, "LeaseNotPresentWithLeaseOperation",
				"There is currently no lease on the resource.", http.StatusConflict)
			return
		}
		if !strings.EqualFold(supplied, lease.LeaseID) {
			writeStorageError(w, "LeaseIdMismatchWithLeaseOperation",
				"The lease ID specified did not match the lease ID for the resource.", http.StatusConflict)
			return
		}
		fileLeases.Delete(key)
		setCommonHeaders()
		w.WriteHeader(http.StatusOK)
	case "break":
		if !held {
			writeStorageError(w, "LeaseNotPresentWithLeaseOperation",
				"There is currently no lease on the resource.", http.StatusConflict)
			return
		}
		breakPeriod := 0
		if raw := r.Header.Get("x-ms-lease-break-period"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 60 {
				writeStorageError(w, "InvalidHeaderValue",
					"The value for one of the HTTP headers is not in the correct format: x-ms-lease-break-period.",
					http.StatusBadRequest)
				return
			}
			breakPeriod = n
		}
		if breakPeriod == 0 {
			fileLeases.Delete(key)
		} else {
			lease.BreakAt = time.Now().Add(time.Duration(breakPeriod) * time.Second).Unix()
			fileLeases.Put(key, lease)
		}
		setCommonHeaders()
		w.Header().Set("x-ms-lease-time", strconv.Itoa(breakPeriod))
		w.WriteHeader(http.StatusAccepted)
	default:
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-lease-action.",
			http.StatusBadRequest)
	}
}

// filesDropLeases removes every lease recorded under a share, which is what
// destroying the share does to the locks held on it and on its files.
func filesDropLeases(account, share string) {
	for _, lease := range fileLeasesUnder(filesSharePrefix(account, share)) {
		fileLeases.Delete(fileLeaseKey(lease.Account, lease.Share, lease.Path))
	}
}

// ── Share-level dispatch ────────────────────────────────────────────

// handleFilesShareOperation serves every operation addressed at
// `/{share}?restype=share`, selected by the `comp` value Azure documents for it.
func handleFilesShareOperation(w http.ResponseWriter, r *http.Request, account, share, comp string) {
	switch comp {
	case "":
		switch r.Method {
		case http.MethodPut:
			handleFilesCreateShare(w, r, account, share)
		case http.MethodDelete:
			handleFilesDeleteShare(w, r, account, share)
		case http.MethodGet, http.MethodHead:
			handleFilesGetShareProperties(w, r, account, share)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case "acl":
		handleFilesShareACL(w, r, account, share)
	case "lease":
		if r.Method != http.MethodPut {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		s, ok := fileShareData.Get(fileShareKey(account, share))
		if !ok {
			writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
			return
		}
		filesHandleLease(w, r, account, share, "", "share", s.ETag, s.Created)
	case "snapshot":
		if r.Method != http.MethodPut {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesCreateShareSnapshot(w, r, account, share)
	case "properties":
		if r.Method != http.MethodPut {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesSetShareProperties(w, r, account, share)
	case "metadata":
		if r.Method != http.MethodPut {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesSetShareMetadata(w, r, account, share)
	case "filepermission":
		switch r.Method {
		case http.MethodPut:
			handleFilesCreatePermission(w, r, account, share)
		case http.MethodGet:
			handleFilesGetPermission(w, r, account, share)
		default:
			writeStorageOperationNotImplemented(w, r, "Files")
		}
	case "stats":
		if r.Method != http.MethodGet {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesGetShareStatistics(w, r, account, share)
	case "undelete":
		if r.Method != http.MethodPut {
			writeStorageOperationNotImplemented(w, r, "Files")
			return
		}
		handleFilesRestoreShare(w, r, account, share)
	default:
		writeStorageOperationNotImplemented(w, r, "Files")
	}
}

// handleFilesCreateShareSnapshot is Create Share Snapshot: it freezes the
// share's directory tree into a real copy addressed by the returned snapshot
// identifier, so a later read with `?sharesnapshot=` sees those bytes even
// after the share itself has moved on.
func handleFilesCreateShareSnapshot(w http.ResponseWriter, r *http.Request, account, share string) {
	s, ok := fileShareData.Get(fileShareKey(account, share))
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if !isStoragePathSegment(account) || !isStoragePathSegment(share) {
		writeStorageError(w, "InvalidResourceName",
			"The specified resource name contains invalid characters.", http.StatusBadRequest)
		return
	}
	snapshot := time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z")
	dir := fileShareSnapshotHostDir(account, share, snapshot)
	if _, err := os.Stat(dir); err == nil {
		writeStorageError(w, "ShareSnapshotInProgress",
			"A previous snapshot operation for the specified share is in progress.", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if err := filesCopyTree(FileShareHostDir(account, share), dir); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	metadata := collectMetadata(r)
	if len(metadata) == 0 {
		metadata = s.Metadata
	}
	entry := FileShareSnapshotData{
		Account: account, Share: share, Snapshot: snapshot,
		Created:  time.Now().UTC().Format(http.TimeFormat),
		Quota:    s.Quota,
		Metadata: metadata,
		ETag:     `"` + generateUUID() + `"`,
	}
	fileShareSnapshots.Put(fileShareSnapshotKey(account, share, snapshot), entry)
	w.Header().Set("x-ms-snapshot", snapshot)
	w.Header().Set("ETag", entry.ETag)
	w.Header().Set("Last-Modified", entry.Created)
	w.WriteHeader(http.StatusCreated)
}

// handleFilesSetShareProperties is Set Share Properties: the quota, access tier
// and root-squash mode the headers name replace the share's current values.
func handleFilesSetShareProperties(w http.ResponseWriter, r *http.Request, account, share string) {
	key := fileShareKey(account, share)
	s, ok := fileShareData.Get(key)
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if !filesRequireLease(w, r, account, share, "", "share") {
		return
	}
	quota := s.Quota
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
	accessTier := s.AccessTier
	if raw := r.Header.Get("x-ms-access-tier"); raw != "" {
		accessTier = raw
	}
	rootSquash := s.RootSquash
	if raw := r.Header.Get("x-ms-root-squash"); raw != "" {
		rootSquash = raw
	}
	s.Quota = quota
	s.AccessTier = accessTier
	s.RootSquash = rootSquash
	s.ETag = `"` + generateUUID() + `"`
	s.Created = time.Now().UTC().Format(http.TimeFormat)
	fileShareData.Put(key, s)
	upsertFileShareARMProjection(account, share, s.Quota, s.Metadata)
	w.Header().Set("ETag", s.ETag)
	w.Header().Set("Last-Modified", s.Created)
	w.Header().Set("x-ms-share-quota", strconv.Itoa(s.Quota))
	w.WriteHeader(http.StatusOK)
}

// handleFilesSetShareMetadata is Set Share Metadata: the x-ms-meta-* headers
// replace the share's metadata wholesale.
func handleFilesSetShareMetadata(w http.ResponseWriter, r *http.Request, account, share string) {
	key := fileShareKey(account, share)
	s, ok := fileShareData.Get(key)
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if !filesRequireLease(w, r, account, share, "", "share") {
		return
	}
	s.Metadata = collectMetadata(r)
	s.ETag = `"` + generateUUID() + `"`
	s.Created = time.Now().UTC().Format(http.TimeFormat)
	fileShareData.Put(key, s)
	upsertFileShareARMProjection(account, share, s.Quota, s.Metadata)
	w.Header().Set("ETag", s.ETag)
	w.Header().Set("Last-Modified", s.Created)
	w.WriteHeader(http.StatusOK)
}

// handleFilesGetShareStatistics is Get Share Statistics: it reports the bytes
// the share's directory tree actually occupies, measured now.
func handleFilesGetShareStatistics(w http.ResponseWriter, r *http.Request, account, share string) {
	root, ok := filesShareRootDir(w, r, account, share)
	if !ok {
		return
	}
	if !filesRequireLease(w, r, account, share, "", "share") {
		return
	}
	var used int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		used += info.Size()
		return nil
	})
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	type shareStats struct {
		XMLName         xml.Name `xml:"ShareStats"`
		ShareUsageBytes int64    `xml:"ShareUsageBytes"`
	}
	s, _ := fileShareData.Get(fileShareKey(account, share))
	w.Header().Set("ETag", s.ETag)
	w.Header().Set("Last-Modified", s.Created)
	writeStorageXML(w, http.StatusOK, shareStats{ShareUsageBytes: used})
}

// handleFilesCreatePermission is Create Permission: it stores a security
// descriptor on the share and returns the key that names it. The key is derived
// from the descriptor, so an identical descriptor always maps to the same key.
func handleFilesCreatePermission(w http.ResponseWriter, r *http.Request, account, share string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	var body struct {
		Permission string `json:"permission"`
		Format     string `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeStorageError(w, "InvalidInput",
			"One of the request inputs is not valid.", http.StatusBadRequest)
		return
	}
	if body.Permission == "" {
		writeStorageError(w, "MissingRequiredXmlNode",
			"A required JSON node was not specified: permission.", http.StatusBadRequest)
		return
	}
	key := filesPermissionKeyFor(body.Permission)
	filePermissions.Put(filePermissionKey(account, share, key), FilePermissionData{
		Account: account, Share: share, Key: key,
		Permission: body.Permission, Format: body.Format,
	})
	w.Header().Set("x-ms-file-permission-key", key)
	w.WriteHeader(http.StatusCreated)
}

// handleFilesGetPermission is Get Permission: it returns the security
// descriptor the key names.
func handleFilesGetPermission(w http.ResponseWriter, r *http.Request, account, share string) {
	if _, ok := fileShareData.Get(fileShareKey(account, share)); !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	key := r.Header.Get("x-ms-file-permission-key")
	if key == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-file-permission-key.",
			http.StatusBadRequest)
		return
	}
	perm, ok := filePermissions.Get(filePermissionKey(account, share, key))
	if !ok {
		writeStorageError(w, "ResourceNotFound",
			"The specified resource does not exist.", http.StatusNotFound)
		return
	}
	out := map[string]string{"permission": perm.Permission}
	if perm.Format != "" {
		out["format"] = perm.Format
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// filesPermissionKeyFor derives a security descriptor's permission key from the
// descriptor itself.
func filesPermissionKeyFor(permission string) string {
	sum := sha256.Sum256([]byte(permission))
	return hex.EncodeToString(sum[:16]) + "*" + strconv.Itoa(len(permission))
}

// filesStorePermissionFromRequest records a security descriptor supplied inline
// on a create or set-properties request and returns the key naming it. A
// request that names an already-stored key instead simply carries it through.
func filesStorePermissionFromRequest(r *http.Request, account, share string) string {
	if key := r.Header.Get("x-ms-file-permission-key"); key != "" {
		return key
	}
	permission := r.Header.Get("x-ms-file-permission")
	if permission == "" || strings.EqualFold(permission, "inherit") {
		return ""
	}
	key := filesPermissionKeyFor(permission)
	filePermissions.Put(filePermissionKey(account, share, key), FilePermissionData{
		Account: account, Share: share, Key: key,
		Permission: permission,
		Format:     r.Header.Get("x-ms-file-permission-format"),
	})
	return key
}

// handleFilesRestoreShare is Restore Share: it puts a soft-deleted share and
// its contents back. Only a share the account's share delete-retention policy
// kept can be restored; with the policy off, Delete Share destroys the share
// outright and there is nothing to name here.
func handleFilesRestoreShare(w http.ResponseWriter, r *http.Request, account, share string) {
	name := r.Header.Get("x-ms-deleted-share-name")
	version := r.Header.Get("x-ms-deleted-share-version")
	if name == "" || version == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-deleted-share-name.",
			http.StatusBadRequest)
		return
	}
	deleted, ok := fileDeletedShares.Get(fileDeletedShareKey(account, name, version))
	if !ok {
		writeStorageError(w, "ShareNotFound", "The specified share does not exist.", http.StatusNotFound)
		return
	}
	if _, ok := fileShareData.Get(fileShareKey(account, share)); ok {
		writeStorageError(w, "ShareAlreadyExists",
			"The specified share already exists.", http.StatusConflict)
		return
	}
	target := FileShareHostDir(account, share)
	if err := os.RemoveAll(target); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(fileDeletedShareHostDir(account, name, version), target); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	restored := FileShareData{
		Account: account, Name: share,
		Quota:    deleted.Quota,
		Metadata: deleted.Metadata,
		ACLs:     deleted.ACLs,
		Created:  time.Now().UTC().Format(http.TimeFormat),
		ETag:     `"` + generateUUID() + `"`,
	}
	fileShareData.Put(fileShareKey(account, share), restored)
	fileDeletedShares.Delete(fileDeletedShareKey(account, name, version))
	upsertFileShareARMProjection(account, share, restored.Quota, restored.Metadata)
	w.Header().Set("ETag", restored.ETag)
	w.Header().Set("Last-Modified", restored.Created)
	w.Header().Set("x-ms-share-quota", strconv.Itoa(restored.Quota))
	w.WriteHeader(http.StatusCreated)
}

// ── Service level ───────────────────────────────────────────────────

// filesServiceProperties is the File service's Set/Get Service Properties
// document. The element names are the ones the REST API and the azfile SDK's
// StorageServiceProperties model exchange.
type filesServiceProperties struct {
	XMLName       xml.Name                `xml:"StorageServiceProperties"`
	HourMetrics   *storageAnalyticsMetric `xml:"HourMetrics,omitempty"`
	MinuteMetrics *storageAnalyticsMetric `xml:"MinuteMetrics,omitempty"`
	Cors          []storageCorsRule       `xml:"Cors>CorsRule,omitempty"`
	Protocol      *filesProtocolSettings  `xml:"ProtocolSettings,omitempty"`
}

type storageAnalyticsMetric struct {
	Version         string                     `xml:"Version"`
	Enabled         bool                       `xml:"Enabled"`
	IncludeAPIs     *bool                      `xml:"IncludeAPIs,omitempty"`
	RetentionPolicy *storageAnalyticsRetention `xml:"RetentionPolicy,omitempty"`
}

type storageAnalyticsRetention struct {
	Enabled bool `xml:"Enabled"`
	Days    *int `xml:"Days,omitempty"`
}

type storageCorsRule struct {
	AllowedOrigins  string `xml:"AllowedOrigins"`
	AllowedMethods  string `xml:"AllowedMethods"`
	AllowedHeaders  string `xml:"AllowedHeaders"`
	ExposedHeaders  string `xml:"ExposedHeaders"`
	MaxAgeInSeconds int32  `xml:"MaxAgeInSeconds"`
}

type filesProtocolSettings struct {
	SMB *filesSMBSettings `xml:"SMB,omitempty"`
}

type filesSMBSettings struct {
	Multichannel *filesSMBMultichannel `xml:"Multichannel,omitempty"`
}

type filesSMBMultichannel struct {
	Enabled bool `xml:"Enabled"`
}

// defaultFilesServiceProperties is the document an account's File service
// carries before anything has been written to it: analytics off with no
// retention, and no CORS rules.
func defaultFilesServiceProperties() filesServiceProperties {
	return filesServiceProperties{
		HourMetrics: &storageAnalyticsMetric{
			Version:         "1.0",
			RetentionPolicy: &storageAnalyticsRetention{},
		},
		MinuteMetrics: &storageAnalyticsMetric{
			Version:         "1.0",
			RetentionPolicy: &storageAnalyticsRetention{},
		},
	}
}

func handleFilesGetServiceProperties(w http.ResponseWriter, r *http.Request, account string) {
	props := defaultFilesServiceProperties()
	if stored, ok := filesServiceConfigs.Get(account); ok && stored.Configured {
		props = stored.Properties
	}
	writeStorageXML(w, http.StatusOK, props)
}

// handleFilesSetServiceProperties is Set File Service Properties: the document
// the request carries becomes the account's File service configuration, and Get
// Service Properties reads back exactly what was written.
func handleFilesSetServiceProperties(w http.ResponseWriter, r *http.Request, account string) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	var props filesServiceProperties
	if err := xml.Unmarshal(body, &props); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"The XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	filesServiceConfigs.Put(account, FilesServiceConfig{
		Account: account, Properties: props, Configured: true,
	})
	w.WriteHeader(http.StatusAccepted)
}

// filesUserDelegationKey mirrors the UserDelegationKey element the service
// returns; the field names are the wire's.
type filesUserDelegationKey struct {
	XMLName       xml.Name `xml:"UserDelegationKey"`
	SignedOID     string   `xml:"SignedOid"`
	SignedTID     string   `xml:"SignedTid"`
	SignedStart   string   `xml:"SignedStart"`
	SignedExpiry  string   `xml:"SignedExpiry"`
	SignedService string   `xml:"SignedService"`
	SignedVersion string   `xml:"SignedVersion"`
	Value         string   `xml:"Value"`
}

// handleFilesGetUserDelegationKey is Get User Delegation Key: it issues a key
// bound to the Microsoft Entra tenant that authorized the request and to the
// window the KeyInfo body asks for. The key is derived from those inputs, so
// the same request yields the same key across calls and across a restart —
// which is what a client that signs a shared access signature with it depends
// on.
func handleFilesGetUserDelegationKey(w http.ResponseWriter, r *http.Request, account string) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	var keyInfo struct {
		XMLName xml.Name `xml:"KeyInfo"`
		Start   string   `xml:"Start"`
		Expiry  string   `xml:"Expiry"`
	}
	if err := xml.Unmarshal(body, &keyInfo); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"The XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	if keyInfo.Expiry == "" {
		writeStorageError(w, "MissingRequiredXmlNode",
			"A required XML node was not specified: Expiry.", http.StatusBadRequest)
		return
	}
	start := keyInfo.Start
	if start == "" {
		start = time.Now().UTC().Format(time.RFC3339)
	}
	material := account + "|" + simTenantID + "|" + start + "|" + keyInfo.Expiry
	sum := sha256.Sum256([]byte(material))
	key := filesUserDelegationKey{
		SignedOID:     generateUUIDFromSeed(material),
		SignedTID:     simTenantID,
		SignedStart:   start,
		SignedExpiry:  keyInfo.Expiry,
		SignedService: "f",
		SignedVersion: r.Header.Get("x-ms-version"),
		Value:         hex.EncodeToString(sum[:]),
	}
	writeStorageXML(w, http.StatusOK, key)
}

// generateUUIDFromSeed derives a stable RFC 4122 version-4-shaped identifier
// from a seed, so the same inputs name the same object across calls and across
// a restart.
func generateUUIDFromSeed(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ── Entry helpers shared by directories and files ───────────────────

// azureFileTimeFormat is the spelling Azure Files uses for the SMB timestamps
// it carries in x-ms-file-creation-time, x-ms-file-last-write-time and
// x-ms-file-change-time: RFC 3339 with exactly seven fractional digits. The
// azfile SDK parses these headers with that layout, so a shorter or longer
// fraction is rejected outright.
const azureFileTimeFormat = "2006-01-02T15:04:05.0000000Z07:00"

// filesEntryID derives the identifier Azure Files reports for an entry from the
// entry's path, so the same entry answers with the same id on every request and
// after a restart.
func filesEntryID(share, entryPath string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(share + "/" + entryPath))
	return strconv.FormatUint(h.Sum64(), 10)
}

func filesParentPath(entryPath string) string {
	if i := strings.LastIndex(entryPath, "/"); i >= 0 {
		return entryPath[:i]
	}
	return ""
}

// writeFileSMBHeaders writes the SMB-shaped properties Azure Files reports for
// every entry. The times come from the filesystem, which is the record of when
// the entry's bytes last changed whether they were written through this data
// plane or by a workload with the share mounted; the creation time is the one
// the data plane recorded when it created the entry, and the filesystem's
// modification time for an entry it did not create.
func writeFileSMBHeaders(w http.ResponseWriter, share, entryPath string, info os.FileInfo, created, permissionKey string) {
	modified := info.ModTime().UTC()
	if created == "" {
		created = modified.Format(azureFileTimeFormat)
	}
	attributes := "Archive"
	if info.IsDir() {
		attributes = "Directory"
	}
	w.Header().Set("x-ms-file-attributes", attributes)
	w.Header().Set("x-ms-file-creation-time", created)
	w.Header().Set("x-ms-file-last-write-time", modified.Format(azureFileTimeFormat))
	w.Header().Set("x-ms-file-change-time", modified.Format(azureFileTimeFormat))
	w.Header().Set("x-ms-file-id", filesEntryID(share, entryPath))
	w.Header().Set("x-ms-file-parent-id", filesEntryID(share, filesParentPath(entryPath)))
	if permissionKey != "" {
		w.Header().Set("x-ms-file-permission-key", permissionKey)
	}
}

// filesApplyLastWriteTime honours an x-ms-file-last-write-time header by moving
// the entry's modification time, which is where Azure Files keeps it.
func filesApplyLastWriteTime(r *http.Request, hostPath string) error {
	raw := r.Header.Get("x-ms-file-last-write-time")
	if raw == "" || strings.EqualFold(raw, "preserve") || strings.EqualFold(raw, "now") {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return err
	}
	return os.Chtimes(hostPath, ts, ts)
}

// filesClearFileRange is what `x-ms-write: clear` does to a range of an Azure
// file: the range stops holding data. The share's files are real files and List
// Ranges reads the filesystem's own extent map, so clearing has to deallocate
// the range rather than fill it with zeros — a zero-filled extent is still an
// allocated extent, and the service would go on reporting a range the caller
// was told it had cleared.
//
// Only whole allocation units can be deallocated. The partial blocks at either
// edge of the range are therefore zeroed in place, which is exactly what the
// kernel does for the unaligned edges of a hole punch, and the aligned interior
// is deallocated.
func filesClearFileRange(f *os.File, info os.FileInfo, start, length int64) error {
	if length <= 0 {
		return nil
	}
	blockSize, ok := fileAllocationBlockSize(info)
	if !ok {
		return fmt.Errorf("clear the range of %s: the filesystem does not report an allocation block size", f.Name())
	}
	end := start + length
	alignedStart := ((start + blockSize - 1) / blockSize) * blockSize
	alignedEnd := (end / blockSize) * blockSize
	if alignedEnd <= alignedStart {
		// The range does not span a whole allocation unit, so there is nothing
		// the filesystem can take back; its bytes still have to read as zeros.
		return filesZeroFileRange(f, start, length)
	}
	if alignedStart > start {
		if err := filesZeroFileRange(f, start, alignedStart-start); err != nil {
			return err
		}
	}
	if end > alignedEnd {
		if err := filesZeroFileRange(f, alignedEnd, end-alignedEnd); err != nil {
			return err
		}
	}
	return filePunchHole(f, alignedStart, alignedEnd-alignedStart)
}

// filesZeroFileRange writes zeros over a range without changing the file's
// size.
func filesZeroFileRange(f *os.File, start, length int64) error {
	const chunk = 1 << 20
	zeros := make([]byte, min(length, int64(chunk)))
	for written := int64(0); written < length; {
		n := min(length-written, int64(len(zeros)))
		if _, err := f.WriteAt(zeros[:n], start+written); err != nil {
			return err
		}
		written += n
	}
	return nil
}

// filesSortedNames returns the entries of a directory in the lexical order the
// Files listing operations enumerate them in.
func filesSortedNames(entries []os.DirEntry) []os.DirEntry {
	out := append([]os.DirEntry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// filesJoinPath joins a share-relative directory path with an entry name.
func filesJoinPath(dirPath, name string) string {
	if dirPath == "" {
		return name
	}
	return path.Join(dirPath, name)
}
