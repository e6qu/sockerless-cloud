package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Blob data-plane state that is not the blob bytes themselves: the account's
// blob service properties (which carry the delete-retention policies that make
// soft delete and Restore Container real), the user delegation keys the service
// has issued, and the lease bookkeeping every write path consults.

// BlobServiceConfig is one account's Set Blob Service Properties document,
// stored verbatim so Get Blob Service Properties reads back what was written and
// the delete-retention policies drive real soft-delete behavior.
type BlobServiceConfig struct {
	Account    string
	Properties BlobServiceProperties
}

// BlobUserDelegationKey is a key the service issued through
// Get User Delegation Key, retained so the same key reads back for the lifetime
// it was issued for.
type BlobUserDelegationKey struct {
	Account       string
	SignedOID     string
	SignedTID     string
	SignedStart   string
	SignedExpiry  string
	SignedService string
	SignedVersion string
	Value         string
}

var (
	blobServicePropsStore sim.Store[BlobServiceConfig]
	blobDelegationKeys    sim.Store[BlobUserDelegationKey]

	// blobARMServiceProps is a handle on the ARM blobServices/default property
	// bag the control plane stores, which is where Azure keeps the container
	// delete-retention policy that governs Restore Container.
	blobARMServiceProps sim.Store[map[string]any]
)

// BlobServiceProperties is the StorageServiceProperties document of the Blob
// service. The element names are the wire names Set/Get Blob Service Properties
// exchange.
type BlobServiceProperties struct {
	XMLName               xml.Name             `xml:"StorageServiceProperties"`
	Logging               *BlobLogging         `xml:"Logging,omitempty"`
	HourMetrics           *BlobMetrics         `xml:"HourMetrics,omitempty"`
	MinuteMetrics         *BlobMetrics         `xml:"MinuteMetrics,omitempty"`
	Cors                  *BlobCorsRules       `xml:"Cors,omitempty"`
	DefaultServiceVersion string               `xml:"DefaultServiceVersion,omitempty"`
	DeleteRetentionPolicy *BlobRetentionPolicy `xml:"DeleteRetentionPolicy,omitempty"`
	StaticWebsite         *BlobStaticWebsite   `xml:"StaticWebsite,omitempty"`
}

type BlobLogging struct {
	Version         string               `xml:"Version"`
	Delete          bool                 `xml:"Delete"`
	Read            bool                 `xml:"Read"`
	Write           bool                 `xml:"Write"`
	RetentionPolicy *BlobRetentionPolicy `xml:"RetentionPolicy,omitempty"`
}

type BlobMetrics struct {
	Version         string               `xml:"Version,omitempty"`
	Enabled         bool                 `xml:"Enabled"`
	IncludeAPIs     *bool                `xml:"IncludeAPIs,omitempty"`
	RetentionPolicy *BlobRetentionPolicy `xml:"RetentionPolicy,omitempty"`
}

type BlobCorsRules struct {
	CorsRule []BlobCorsRule `xml:"CorsRule"`
}

type BlobCorsRule struct {
	AllowedOrigins  string `xml:"AllowedOrigins"`
	AllowedMethods  string `xml:"AllowedMethods"`
	AllowedHeaders  string `xml:"AllowedHeaders"`
	ExposedHeaders  string `xml:"ExposedHeaders"`
	MaxAgeInSeconds int32  `xml:"MaxAgeInSeconds"`
}

type BlobRetentionPolicy struct {
	Enabled              bool   `xml:"Enabled"`
	Days                 *int32 `xml:"Days,omitempty"`
	AllowPermanentDelete *bool  `xml:"AllowPermanentDelete,omitempty"`
}

type BlobStaticWebsite struct {
	Enabled          bool   `xml:"Enabled"`
	IndexDocument    string `xml:"IndexDocument,omitempty"`
	DefaultIndexPath string `xml:"DefaultIndexDocumentPath,omitempty"`
	ErrorDocument404 string `xml:"ErrorDocument404Path,omitempty"`
}

// defaultBlobServiceProperties is what a storage account answers with before any
// Set Blob Service Properties call, matching the shape real Azure returns for a
// freshly created account.
func defaultBlobServiceProperties() BlobServiceProperties {
	return BlobServiceProperties{
		Logging: &BlobLogging{
			Version:         "1.0",
			RetentionPolicy: &BlobRetentionPolicy{},
		},
		HourMetrics: &BlobMetrics{
			Version:         "1.0",
			RetentionPolicy: &BlobRetentionPolicy{},
		},
		MinuteMetrics: &BlobMetrics{
			Version:         "1.0",
			RetentionPolicy: &BlobRetentionPolicy{},
		},
		Cors: &BlobCorsRules{},
	}
}

// blobServiceProperties returns the account's complete service-properties
// document. Every section is always populated: Azure answers Get Blob Service
// Properties with the whole document, and a client that parses it walks each
// section unconditionally.
func blobServiceProperties(account string) BlobServiceProperties {
	props := defaultBlobServiceProperties()
	if cfg, ok := blobServicePropsStore.Get(account); ok {
		props = cfg.Properties
	}
	fillBlobServiceProperties(&props)
	return props
}

// fillBlobServiceProperties populates the sections a stored document leaves
// unset, so the response is always the complete document.
func fillBlobServiceProperties(props *BlobServiceProperties) {
	defaults := defaultBlobServiceProperties()
	if props.Logging == nil {
		props.Logging = defaults.Logging
	}
	if props.Logging.RetentionPolicy == nil {
		props.Logging.RetentionPolicy = &BlobRetentionPolicy{}
	}
	if props.HourMetrics == nil {
		props.HourMetrics = defaults.HourMetrics
	}
	if props.HourMetrics.RetentionPolicy == nil {
		props.HourMetrics.RetentionPolicy = &BlobRetentionPolicy{}
	}
	if props.MinuteMetrics == nil {
		props.MinuteMetrics = defaults.MinuteMetrics
	}
	if props.MinuteMetrics.RetentionPolicy == nil {
		props.MinuteMetrics.RetentionPolicy = &BlobRetentionPolicy{}
	}
	if props.Cors == nil {
		props.Cors = &BlobCorsRules{}
	}
	if props.DeleteRetentionPolicy == nil {
		props.DeleteRetentionPolicy = &BlobRetentionPolicy{}
	}
	if props.StaticWebsite == nil {
		props.StaticWebsite = &BlobStaticWebsite{}
	}
}

// mergeBlobServiceProperties folds a Set Blob Service Properties request into
// the account's current document. Azure leaves a section the request omits
// unchanged, and an empty element — `<Cors />` — clears that section, which is
// exactly the difference between a nil and a non-nil field here.
func mergeBlobServiceProperties(current, requested BlobServiceProperties) BlobServiceProperties {
	if requested.Logging != nil {
		current.Logging = requested.Logging
	}
	if requested.HourMetrics != nil {
		current.HourMetrics = requested.HourMetrics
	}
	if requested.MinuteMetrics != nil {
		current.MinuteMetrics = requested.MinuteMetrics
	}
	if requested.Cors != nil {
		current.Cors = requested.Cors
	}
	if requested.DefaultServiceVersion != "" {
		current.DefaultServiceVersion = requested.DefaultServiceVersion
	}
	if requested.DeleteRetentionPolicy != nil {
		current.DeleteRetentionPolicy = requested.DeleteRetentionPolicy
	}
	if requested.StaticWebsite != nil {
		current.StaticWebsite = requested.StaticWebsite
	}
	return current
}

// blobSoftDeleteDays returns the retention window a soft delete of a BLOB is
// retained for, and whether the account has the policy enabled at all. With the
// policy off, Delete Blob is permanent, exactly as on real Azure.
func blobSoftDeleteDays(account string) (int32, bool) {
	p := blobServiceProperties(account).DeleteRetentionPolicy
	if p == nil || !p.Enabled {
		return 0, false
	}
	days := int32(7)
	if p.Days != nil {
		days = *p.Days
	}
	return days, true
}

// blobContainerSoftDeleteDays is the container-level sibling of
// blobSoftDeleteDays. Azure configures container soft delete on the ARM
// `Microsoft.Storage/storageAccounts/{name}/blobServices/default` resource
// rather than on the data-plane service-properties document, so that is where
// this reads it from — the same resource an operator sets it on.
func blobContainerSoftDeleteDays(account string) (int32, bool) {
	if blobARMServiceProps == nil {
		return 0, false
	}
	if acct, ok := azureStorageAccountByName(account); ok {
		props, ok := blobARMServiceProps.Get(acct.ID)
		if !ok {
			return 0, false
		}
		policy, ok := props["containerDeleteRetentionPolicy"].(map[string]any)
		if !ok {
			return 0, false
		}
		if enabled, _ := policy["enabled"].(bool); !enabled {
			return 0, false
		}
		days := int32(7)
		if raw, ok := policy["days"].(float64); ok && raw > 0 {
			days = int32(raw)
		}
		return days, true
	}
	return 0, false
}

// Leases

const (
	blobLeaseStateAvailable = "available"
	blobLeaseStateLeased    = "leased"
	blobLeaseStateExpired   = "expired"
	blobLeaseStateBreaking  = "breaking"
	blobLeaseStateBroken    = "broken"
)

// blobLeaseState resolves the lease state Azure reports, from the stored facts
// and the clock. A finite lease really runs out; a pending break really
// completes when its break period elapses.
func blobLeaseState(l BlobLease, now time.Time) string {
	switch {
	case l.ID == "" && l.Broken:
		return blobLeaseStateBroken
	case l.ID == "":
		return blobLeaseStateAvailable
	case !l.BreakAt.IsZero():
		if now.Before(l.BreakAt) {
			return blobLeaseStateBreaking
		}
		return blobLeaseStateBroken
	case l.Duration > 0 && !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt):
		return blobLeaseStateExpired
	default:
		return blobLeaseStateLeased
	}
}

// blobLeaseStatus is the locked/unlocked summary that accompanies the state.
func blobLeaseStatus(state string) string {
	if state == blobLeaseStateLeased || state == blobLeaseStateBreaking {
		return "locked"
	}
	return "unlocked"
}

// blobLeaseDurationType is "infinite" or "fixed" while a lease is held, and
// empty otherwise — Azure omits the header when no lease is active.
func blobLeaseDurationType(l BlobLease, state string) string {
	if state != blobLeaseStateLeased && state != blobLeaseStateBreaking {
		return ""
	}
	if l.Duration < 0 {
		return "infinite"
	}
	return "fixed"
}

// blobLeaseHeld reports whether the lease currently locks the resource, so a
// write needs the matching lease ID.
func blobLeaseHeld(l BlobLease, now time.Time) bool {
	return blobLeaseStatus(blobLeaseState(l, now)) == "locked"
}

// writeBlobLeaseHeaders emits the x-ms-lease-* trio Get Blob Properties and
// Get Container Properties carry.
func writeBlobLeaseHeaders(w http.ResponseWriter, l BlobLease, now time.Time) {
	state := blobLeaseState(l, now)
	w.Header().Set("x-ms-lease-state", state)
	w.Header().Set("x-ms-lease-status", blobLeaseStatus(state))
	if d := blobLeaseDurationType(l, state); d != "" {
		w.Header().Set("x-ms-lease-duration", d)
	}
}

// blobLeaseAccessOK enforces the lease on a write. `resource` is "blob" or
// "container" and selects the error codes Azure emits for that resource.
//
// The rules are Azure's: a locked resource needs the matching x-ms-lease-id
// (absent → LeaseIdMissing, wrong → LeaseIdMismatchWith…Operation), and an
// unlocked resource rejects a lease ID that names no lease
// (LeaseNotPresentWith…Operation). Every failure is a 412.
func blobLeaseAccessOK(w http.ResponseWriter, r *http.Request, l BlobLease, resource string) bool {
	now := time.Now()
	requested := r.Header.Get("x-ms-lease-id")
	held := blobLeaseHeld(l, now)
	switch {
	case held && requested == "":
		writeStorageError(w, "LeaseIdMissing",
			"There is currently a lease on the "+resource+" and no lease ID was specified in the request.",
			http.StatusPreconditionFailed)
		return false
	case held && !strings.EqualFold(requested, l.ID):
		writeStorageError(w, blobLeaseMismatchCode(resource),
			"The lease ID specified did not match the lease ID for the "+resource+".",
			http.StatusPreconditionFailed)
		return false
	case !held && requested != "":
		writeStorageError(w, "LeaseNotPresentWith"+blobLeaseResourceSuffix(resource)+"Operation",
			"There is currently no lease on the "+resource+".",
			http.StatusPreconditionFailed)
		return false
	}
	return true
}

func blobLeaseResourceSuffix(resource string) string {
	if resource == "container" {
		return "Container"
	}
	return "Blob"
}

func blobLeaseMismatchCode(resource string) string {
	return "LeaseIdMismatchWith" + blobLeaseResourceSuffix(resource) + "Operation"
}

// blobLeaseActionRequest is the parsed x-ms-lease-* header set of a Lease Blob /
// Lease Container request.
type blobLeaseActionRequest struct {
	action        string
	leaseID       string
	proposedID    string
	duration      int32
	hasDuration   bool
	breakPeriod   int32
	hasBreakPer   bool
	durationRaw   string
	breakPeriodRw string
}

// parseBlobLeaseRequest reads the lease headers, validating them the way Azure
// does: x-ms-lease-action is required, a finite duration must be 15–60 seconds
// (or -1 for infinite), and a break period must be 0–60 seconds.
func parseBlobLeaseRequest(w http.ResponseWriter, r *http.Request) (blobLeaseActionRequest, bool) {
	var req blobLeaseActionRequest
	req.action = strings.ToLower(strings.TrimSpace(r.Header.Get("x-ms-lease-action")))
	if req.action == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-lease-action.",
			http.StatusBadRequest)
		return req, false
	}
	switch req.action {
	case "acquire", "renew", "change", "release", "break":
	default:
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-lease-action.",
			http.StatusBadRequest)
		return req, false
	}
	req.leaseID = r.Header.Get("x-ms-lease-id")
	req.proposedID = r.Header.Get("x-ms-proposed-lease-id")
	if raw := r.Header.Get("x-ms-lease-duration"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || (v != -1 && (v < 15 || v > 60)) {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-lease-duration.",
				http.StatusBadRequest)
			return req, false
		}
		req.duration, req.hasDuration, req.durationRaw = int32(v), true, raw
	}
	if raw := r.Header.Get("x-ms-lease-break-period"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 || v > 60 {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-lease-break-period.",
				http.StatusBadRequest)
			return req, false
		}
		req.breakPeriod, req.hasBreakPer, req.breakPeriodRw = int32(v), true, raw
	}
	return req, true
}

// applyBlobLeaseAction runs one lease verb against the current lease and returns
// the new lease plus the response status and headers. It writes the error and
// returns ok=false when the verb is illegal in the current state.
func applyBlobLeaseAction(w http.ResponseWriter, req blobLeaseActionRequest, cur BlobLease, resource string) (next BlobLease, status int, headers map[string]string, ok bool) {
	now := time.Now()
	state := blobLeaseState(cur, now)
	headers = map[string]string{}

	switch req.action {
	case "acquire":
		if !req.hasDuration {
			writeStorageError(w, "MissingRequiredHeader",
				"An HTTP header that's mandatory for this request is not specified: x-ms-lease-duration.",
				http.StatusBadRequest)
			return cur, 0, nil, false
		}
		switch state {
		case blobLeaseStateLeased:
			// Re-acquiring with the same proposed ID is idempotent; any other
			// acquisition conflicts with the live lease.
			if req.proposedID == "" || !strings.EqualFold(req.proposedID, cur.ID) {
				writeStorageError(w, "LeaseAlreadyPresent",
					"There is already a lease present.", http.StatusConflict)
				return cur, 0, nil, false
			}
		case blobLeaseStateBreaking:
			writeStorageError(w, "LeaseIsBreakingAndCannotBeAcquired",
				"There is currently a pending break operation and the lease cannot be acquired until it is broken.",
				http.StatusConflict)
			return cur, 0, nil, false
		}
		id := req.proposedID
		if id == "" {
			id = generateUUID()
		}
		next = BlobLease{ID: id, Duration: req.duration}
		if req.duration > 0 {
			next.ExpiresAt = now.Add(time.Duration(req.duration) * time.Second)
		}
		headers["x-ms-lease-id"] = id
		return next, http.StatusCreated, headers, true

	case "renew":
		if state == blobLeaseStateBroken {
			writeStorageError(w, "LeaseIsBrokenAndCannotBeRenewed",
				"The lease ID matched, but the lease has been broken explicitly and cannot be renewed.",
				http.StatusConflict)
			return cur, 0, nil, false
		}
		if cur.ID == "" || !strings.EqualFold(req.leaseID, cur.ID) {
			writeStorageError(w, "LeaseIdMismatchWithLeaseOperation",
				"The lease ID specified did not match the lease ID for the "+resource+".",
				http.StatusConflict)
			return cur, 0, nil, false
		}
		next = cur
		next.BreakAt = time.Time{}
		next.Broken = false
		if cur.Duration > 0 {
			next.ExpiresAt = now.Add(time.Duration(cur.Duration) * time.Second)
		}
		headers["x-ms-lease-id"] = next.ID
		return next, http.StatusOK, headers, true

	case "change":
		if cur.ID == "" || !strings.EqualFold(req.leaseID, cur.ID) {
			writeStorageError(w, "LeaseIdMismatchWithLeaseOperation",
				"The lease ID specified did not match the lease ID for the "+resource+".",
				http.StatusConflict)
			return cur, 0, nil, false
		}
		if state == blobLeaseStateBreaking {
			writeStorageError(w, "LeaseIsBreakingAndCannotBeChanged",
				"The lease ID matched, but the lease is currently in breaking state and cannot be changed.",
				http.StatusConflict)
			return cur, 0, nil, false
		}
		if req.proposedID == "" {
			writeStorageError(w, "MissingRequiredHeader",
				"An HTTP header that's mandatory for this request is not specified: x-ms-proposed-lease-id.",
				http.StatusBadRequest)
			return cur, 0, nil, false
		}
		next = cur
		next.ID = req.proposedID
		headers["x-ms-lease-id"] = next.ID
		return next, http.StatusOK, headers, true

	case "release":
		if cur.ID == "" || !strings.EqualFold(req.leaseID, cur.ID) {
			writeStorageError(w, "LeaseIdMismatchWithLeaseOperation",
				"The lease ID specified did not match the lease ID for the "+resource+".",
				http.StatusConflict)
			return cur, 0, nil, false
		}
		return BlobLease{}, http.StatusOK, headers, true

	case "break":
		if state == blobLeaseStateAvailable {
			writeStorageError(w, "LeaseNotPresentWith"+blobLeaseResourceSuffix(resource)+"Operation",
				"There is currently no lease on the "+resource+".", http.StatusConflict)
			return cur, 0, nil, false
		}
		next = cur
		remaining := int32(0)
		if req.hasBreakPer {
			remaining = req.breakPeriod
		} else if cur.Duration > 0 && !cur.ExpiresAt.IsZero() {
			// Without an explicit break period a finite lease breaks when its
			// remaining term runs out; an infinite lease breaks immediately.
			if d := time.Until(cur.ExpiresAt); d > 0 {
				remaining = int32(d.Seconds() + 0.5)
			}
		}
		if state == blobLeaseStateBreaking {
			// A second break can only shorten the pending one.
			if left := int32(time.Until(cur.BreakAt).Seconds() + 0.5); left > 0 && (!req.hasBreakPer || left < remaining) {
				remaining = left
			}
		}
		if remaining <= 0 {
			next = BlobLease{Broken: true}
			remaining = 0
		} else {
			next.BreakAt = now.Add(time.Duration(remaining) * time.Second)
		}
		headers["x-ms-lease-time"] = strconv.Itoa(int(remaining))
		return next, http.StatusAccepted, headers, true
	}
	writeStorageError(w, "InvalidHeaderValue",
		"The value for one of the HTTP headers is not in the correct format: x-ms-lease-action.",
		http.StatusBadRequest)
	return cur, 0, nil, false
}

// Write guards

// blobWriteAllowed enforces the write protections a stored blob carries: a
// locked or unlocked-but-named lease, a legal hold, and an unexpired
// immutability policy. It writes the Azure error and returns false when the
// write must be refused.
func blobWriteAllowed(w http.ResponseWriter, r *http.Request, b BlobObject, exists bool) bool {
	if exists && !blobLeaseAccessOK(w, r, b.Lease, "blob") {
		return false
	}
	if !exists {
		return true
	}
	return blobImmutabilityAllowsWrite(w, r, b)
}

// blobImmutabilityAllowsWrite refuses a write to a blob under legal hold or
// under an unexpired immutability policy. An unlocked policy can be overridden
// by a caller that carries x-ms-immutability-policy-mode: Mutable, which is how
// Azure spells the override.
func blobImmutabilityAllowsWrite(w http.ResponseWriter, r *http.Request, b BlobObject) bool {
	if b.LegalHold {
		writeStorageError(w, "BlobImmutableDueToPolicy",
			"This operation is not permitted as the blob is immutable due to a legal hold.",
			http.StatusConflict)
		return false
	}
	if b.ImmutabilityPolicyExpiry == "" {
		return true
	}
	expiry, err := http.ParseTime(b.ImmutabilityPolicyExpiry)
	if err != nil || !time.Now().Before(expiry) {
		return true
	}
	if strings.EqualFold(b.ImmutabilityPolicyMode, "Unlocked") &&
		strings.EqualFold(r.Header.Get("x-ms-immutability-policy-mode"), "Mutable") {
		return true
	}
	writeStorageError(w, "BlobImmutableDueToPolicy",
		"This operation is not permitted as the blob is immutable due to a policy.",
		http.StatusConflict)
	return false
}

// blobContainerWriteAllowed enforces a container lease on a container-level
// write.
func blobContainerWriteAllowed(w http.ResponseWriter, r *http.Request, c BlobContainerData) bool {
	return blobLeaseAccessOK(w, r, c.Lease, "container")
}

// Blob lookup

// lookupBlob resolves the blob a request addresses, honoring the `?snapshot=`
// query. Soft-deleted blobs are invisible to every operation except Undelete and
// a listing that asked to include them, exactly as on Azure.
func lookupBlob(r *http.Request, account, container, name string) (BlobObject, bool) {
	snapshot := r.URL.Query().Get("snapshot")
	b, ok := blobObjects.Get(blobSnapshotKey(account, container, name, snapshot))
	if !ok || b.Deleted {
		return BlobObject{}, false
	}
	return b, true
}

// blobsInContainer returns every stored record of a container — base blobs,
// snapshots and soft-deleted rows alike — sorted by name then snapshot, which is
// the order Azure lists them in.
func blobsInContainer(account, container string) []BlobObject {
	keys := blobKeysInContainer(account, container)
	out := make([]BlobObject, 0, len(keys))
	for _, key := range keys {
		if b, ok := blobObjects.Get(key); ok {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Snapshot < out[j].Snapshot
	})
	return out
}

// Shared value helpers

func blobNowHTTP() string {
	return time.Now().UTC().Format(http.TimeFormat)
}

// blobETagFor derives the ETag of a stored blob from its bytes plus a
// modification stamp, so two writes of identical bytes still produce distinct
// ETags the way Azure's do.
func blobETagFor(data []byte, stamp string) string {
	h := md5.New()
	h.Write(data)
	h.Write([]byte(stamp))
	return `"0x` + strings.ToUpper(hex.EncodeToString(h.Sum(nil))[:16]) + `"`
}

// blobContentMD5 is the base64 Content-MD5 Azure returns for stored bytes.
func blobContentMD5(data []byte) string {
	sum := md5.Sum(data)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// blobSnapshotStamp is the snapshot identity Azure mints: an ISO-8601 UTC
// instant at 100-nanosecond resolution.
func blobSnapshotStamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// blobRandomKeyMaterial returns 32 fresh random bytes, base64 encoded — the
// shape of a Get User Delegation Key value.
func blobRandomKeyMaterial() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is not a condition the service can paper over.
		panic("blob data plane: crypto/rand: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// blobTouch stamps a modification onto a blob record and returns the record so
// callers can write it back.
func blobTouch(b *BlobObject) {
	b.LastModified = blobNowHTTP()
	b.ETag = blobETagFor(b.Data, b.LastModified+generateUUID())
}

// List Blobs entry shape

// blobListProperties is the <Properties> element of one blob in a List Blobs
// response. Every element name is the wire name azblob's BlobProperties reads.
type blobListProperties struct {
	CreationTime            string `xml:"Creation-Time,omitempty"`
	LastModified            string `xml:"Last-Modified"`
	ETag                    string `xml:"Etag"`
	ContentLength           int64  `xml:"Content-Length"`
	ContentType             string `xml:"Content-Type,omitempty"`
	ContentEncoding         string `xml:"Content-Encoding,omitempty"`
	ContentLanguage         string `xml:"Content-Language,omitempty"`
	ContentMD5              string `xml:"Content-MD5,omitempty"`
	ContentDisposition      string `xml:"Content-Disposition,omitempty"`
	CacheControl            string `xml:"Cache-Control,omitempty"`
	BlobSequenceNumber      *int64 `xml:"x-ms-blob-sequence-number,omitempty"`
	BlobType                string `xml:"BlobType,omitempty"`
	LeaseStatus             string `xml:"LeaseStatus,omitempty"`
	LeaseState              string `xml:"LeaseState,omitempty"`
	LeaseDuration           string `xml:"LeaseDuration,omitempty"`
	CopyID                  string `xml:"CopyId,omitempty"`
	CopyStatus              string `xml:"CopyStatus,omitempty"`
	CopySource              string `xml:"CopySource,omitempty"`
	CopyProgress            string `xml:"CopyProgress,omitempty"`
	CopyCompletionTime      string `xml:"CopyCompletionTime,omitempty"`
	CopyStatusDescription   string `xml:"CopyStatusDescription,omitempty"`
	IncrementalCopy         *bool  `xml:"IncrementalCopy,omitempty"`
	DestinationSnapshot     string `xml:"DestinationSnapshot,omitempty"`
	ServerEncrypted         *bool  `xml:"ServerEncrypted,omitempty"`
	AccessTier              string `xml:"AccessTier,omitempty"`
	AccessTierInferred      *bool  `xml:"AccessTierInferred,omitempty"`
	AccessTierChangeTime    string `xml:"AccessTierChangeTime,omitempty"`
	ExpiresOn               string `xml:"Expiry-Time,omitempty"`
	Sealed                  *bool  `xml:"Sealed,omitempty"`
	LegalHold               *bool  `xml:"LegalHold,omitempty"`
	ImmutabilityPolicyUntil string `xml:"ImmutabilityPolicyUntilDate,omitempty"`
	ImmutabilityPolicyMode  string `xml:"ImmutabilityPolicyMode,omitempty"`
	DeletedTime             string `xml:"DeletedTime,omitempty"`
	RemainingRetentionDays  *int32 `xml:"RemainingRetentionDays,omitempty"`
	TagCount                *int32 `xml:"TagCount,omitempty"`
}

// blobListEntry is one <Blob> element of a List Blobs response.
type blobListEntry struct {
	Name       string               `xml:"Name"`
	Snapshot   string               `xml:"Snapshot,omitempty"`
	Deleted    *bool                `xml:"Deleted,omitempty"`
	Properties blobListProperties   `xml:"Properties"`
	Metadata   *blobMetadataElement `xml:"Metadata,omitempty"`
	Tags       *blobTagsDocument    `xml:"Tags,omitempty"`
}

// blobListEntryFor projects a stored blob into its List Blobs element, honoring
// the `include=` selectors that decide whether metadata and tags are inlined.
func blobListEntryFor(b BlobObject, include map[string]bool) blobListEntry {
	now := time.Now()
	state := blobLeaseState(b.Lease, now)
	encrypted := true
	entry := blobListEntry{
		Name:     b.Name,
		Snapshot: b.Snapshot,
		Properties: blobListProperties{
			CreationTime:            b.CreationTime,
			LastModified:            b.LastModified,
			ETag:                    b.ETag,
			ContentLength:           int64(len(b.Data)),
			ContentType:             b.ContentType,
			ContentEncoding:         b.ContentEncoding,
			ContentLanguage:         b.ContentLanguage,
			ContentMD5:              b.ContentMD5,
			ContentDisposition:      b.ContentDisposition,
			CacheControl:            b.CacheControl,
			BlobType:                b.BlobType,
			LeaseStatus:             blobLeaseStatus(state),
			LeaseState:              state,
			LeaseDuration:           blobLeaseDurationType(b.Lease, state),
			CopyID:                  b.CopyID,
			CopyStatus:              b.CopyStatus,
			CopySource:              b.CopySource,
			CopyProgress:            b.CopyProgress,
			CopyCompletionTime:      b.CopyCompletionTime,
			CopyStatusDescription:   b.CopyStatusDescription,
			DestinationSnapshot:     b.CopyDestinationSnapshot,
			ServerEncrypted:         &encrypted,
			AccessTier:              b.AccessTier,
			AccessTierChangeTime:    b.AccessTierChangeTime,
			ExpiresOn:               b.ExpiresOn,
			ImmutabilityPolicyUntil: b.ImmutabilityPolicyExpiry,
			ImmutabilityPolicyMode:  b.ImmutabilityPolicyMode,
		},
	}
	if b.AccessTierInferred {
		inferred := true
		entry.Properties.AccessTierInferred = &inferred
	}
	if b.BlobType == "PageBlob" {
		seq := b.SequenceNumber
		entry.Properties.BlobSequenceNumber = &seq
	}
	if b.BlobType == "AppendBlob" {
		sealed := b.Sealed
		entry.Properties.Sealed = &sealed
	}
	if b.IncrementalCopy {
		incremental := true
		entry.Properties.IncrementalCopy = &incremental
	}
	if b.LegalHold {
		hold := true
		entry.Properties.LegalHold = &hold
	}
	if b.Deleted {
		deleted := true
		retention := b.RemainingRetentionDays
		entry.Deleted = &deleted
		entry.Properties.DeletedTime = b.DeletedTime
		entry.Properties.RemainingRetentionDays = &retention
	}
	if n := int32(len(b.Tags)); n > 0 {
		entry.Properties.TagCount = &n
	}
	if include["metadata"] {
		entry.Metadata = blobMetadataXML(b.Metadata)
	}
	if include["tags"] && len(b.Tags) > 0 {
		entry.Tags = blobTagsDocumentFor(b.Tags)
	}
	return entry
}
