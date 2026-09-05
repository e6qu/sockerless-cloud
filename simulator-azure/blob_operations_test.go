package main

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Blob data-plane behaviours the SDK and CLI suites cannot reach: the lease
// state machine resolved from the wall clock (an SDK test would have to sleep
// out a 15-second minimum lease), and the durability of every piece of blob
// state across a simulator restart.

// TestBlobLeaseStateResolvesFromTheClock asserts that a finite lease really
// expires and a pending break really completes — both are derived from the
// stored facts and the current time, never from a flag a handler sets.
func TestBlobLeaseStateResolvesFromTheClock(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		lease    BlobLease
		state    string
		status   string
		duration string
	}{
		{"no lease", BlobLease{}, blobLeaseStateAvailable, "unlocked", ""},
		{
			"infinite lease", BlobLease{ID: "a", Duration: -1},
			blobLeaseStateLeased, "locked", "infinite",
		},
		{
			"finite lease still running",
			BlobLease{ID: "a", Duration: 60, ExpiresAt: now.Add(30 * time.Second)},
			blobLeaseStateLeased, "locked", "fixed",
		},
		{
			"finite lease past its term",
			BlobLease{ID: "a", Duration: 15, ExpiresAt: now.Add(-time.Second)},
			blobLeaseStateExpired, "unlocked", "",
		},
		{
			"break pending",
			BlobLease{ID: "a", Duration: -1, BreakAt: now.Add(20 * time.Second)},
			blobLeaseStateBreaking, "locked", "infinite",
		},
		{
			"break period elapsed",
			BlobLease{ID: "a", Duration: -1, BreakAt: now.Add(-time.Second)},
			blobLeaseStateBroken, "unlocked", "",
		},
		{"broken", BlobLease{Broken: true}, blobLeaseStateBroken, "unlocked", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := blobLeaseState(tc.lease, now)
			if state != tc.state {
				t.Fatalf("state = %q, want %q", state, tc.state)
			}
			if got := blobLeaseStatus(state); got != tc.status {
				t.Fatalf("status = %q, want %q", got, tc.status)
			}
			if got := blobLeaseDurationType(tc.lease, state); got != tc.duration {
				t.Fatalf("duration = %q, want %q", got, tc.duration)
			}
		})
	}
}

// TestBlobExpiredLeaseNoLongerBlocksWrites drives the expiry through the data
// plane: a lease whose term has run out stops locking the blob, so a write with
// no lease ID goes through where it was refused a moment before.
func TestBlobExpiredLeaseNoLongerBlocksWrites(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container, blob = "leaseexpiryacct", "expiry-container", "held.txt"

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob, []byte("v1"),
		map[string]string{"x-ms-blob-type": "BlockBlob"}), http.StatusCreated, "PutBlob")

	rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob+"?comp=lease", nil,
		map[string]string{"x-ms-lease-action": "acquire", "x-ms-lease-duration": "15"})
	assertStatus(t, rec, http.StatusCreated, "AcquireLease")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob, []byte("v2"),
		map[string]string{"x-ms-blob-type": "BlockBlob"}), http.StatusPreconditionFailed,
		"PutBlob over a leased blob without the lease ID")

	// Wind the stored lease past its term, exactly as the clock would.
	stored, ok := blobObjects.Get(blobObjectKey(account, container, blob))
	if !ok {
		t.Fatal("the leased blob is missing from the store")
	}
	stored.Lease.ExpiresAt = time.Now().Add(-time.Second)
	putBlobObject(stored)

	rec = storagePlaneReq(t, srv, http.MethodHead, account, "blob", "/"+container+"/"+blob, nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlobProperties after the lease term ran out")
	if got := rec.Header().Get("x-ms-lease-state"); got != blobLeaseStateExpired {
		t.Fatalf("x-ms-lease-state = %q, want %q", got, blobLeaseStateExpired)
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob, []byte("v2"),
		map[string]string{"x-ms-blob-type": "BlockBlob"}), http.StatusCreated,
		"PutBlob after the lease expired")
}

// TestBlobDataPlaneStateSurvivesRestart writes every kind of blob state the
// data plane keeps — a lease, a snapshot, tags, an immutability policy, page
// ranges, a container ACL — then rebuilds the simulator against the same data
// directory and reads all of it back. Persistent state that only lives for the
// process lifetime is indistinguishable from working state until the process
// restarts.
func TestBlobDataPlaneStateSurvivesRestart(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	dataDir := t.TempDir()
	const account, container = "restartacct", "restart-container"

	boot := func() *sim.Server {
		t.Helper()
		srv, err := buildSimulator(sim.Config{
			Provider: "azure", ListenAddr: ":0", LogLevel: "error",
			Persist: true, DataDir: dataDir,
		})
		if err != nil {
			t.Fatalf("build simulator: %v", err)
		}
		// Long-running operations complete in a goroutine. One still running
		// when this test ends would read and write the stores while the next
		// test rebuilds them.
		t.Cleanup(AwaitAzureAsyncOperations)
		return srv
	}

	srv := boot()
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")

	// A block blob with tags, a lease, an immutability policy and a snapshot.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/kept.txt", []byte("v1"),
		map[string]string{"x-ms-blob-type": "BlockBlob"}), http.StatusCreated, "PutBlob")
	snapRec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/kept.txt?comp=snapshot", nil, nil)
	assertStatus(t, snapRec, http.StatusCreated, "CreateSnapshot")
	snapshot := snapRec.Header().Get("x-ms-snapshot")
	if snapshot == "" {
		t.Fatal("Create Snapshot returned no x-ms-snapshot coordinate")
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/kept.txt", []byte("v2"),
		map[string]string{"x-ms-blob-type": "BlockBlob"}), http.StatusCreated, "PutBlob (overwrite)")

	// Tags are set after the overwrite: Put Blob replaces the whole blob, tag
	// set included, exactly as it does on Azure.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/kept.txt?comp=tags",
		[]byte(`<Tags><TagSet><Tag><Key>project</Key><Value>sockerless</Value></Tag></TagSet></Tags>`), nil),
		http.StatusNoContent, "SetBlobTags")

	leaseRec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/kept.txt?comp=lease", nil,
		map[string]string{"x-ms-lease-action": "acquire", "x-ms-lease-duration": "-1"})
	assertStatus(t, leaseRec, http.StatusCreated, "AcquireLease")
	leaseID := leaseRec.Header().Get("x-ms-lease-id")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"/kept.txt?comp=legalhold", nil,
		map[string]string{"x-ms-legal-hold": "true", "x-ms-lease-id": leaseID}),
		http.StatusOK, "SetLegalHold")

	// A page blob with one written page, so the range set has something in it.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/disk.vhd", nil,
		map[string]string{"x-ms-blob-type": "PageBlob", "x-ms-blob-content-length": "1024"}),
		http.StatusCreated, "CreatePageBlob")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/disk.vhd?comp=page",
		make([]byte, 512), map[string]string{"x-ms-page-write": "update", "x-ms-range": "bytes=512-1023"}),
		http.StatusCreated, "UploadPages")

	// A container ACL and metadata.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"?restype=container&comp=acl",
		[]byte(`<SignedIdentifiers><SignedIdentifier><Id>readers</Id><AccessPolicy>`+
			`<Permission>rl</Permission></AccessPolicy></SignedIdentifier></SignedIdentifiers>`), nil),
		http.StatusOK, "SetContainerAccessPolicy")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"?restype=container&comp=metadata", nil,
		map[string]string{"x-ms-meta-team": "storage"}), http.StatusOK, "SetContainerMetadata")

	// Restart: a fresh server on the same data directory.
	restarted := boot()

	rec := storagePlaneReq(t, restarted, http.MethodHead, account, "blob", "/"+container+"/kept.txt", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlobProperties after restart")
	if got := rec.Header().Get("x-ms-lease-state"); got != blobLeaseStateLeased {
		t.Fatalf("lease state after restart = %q, want %q", got, blobLeaseStateLeased)
	}
	if got := rec.Header().Get("x-ms-legal-hold"); got != "true" {
		t.Fatalf("legal hold after restart = %q, want true", got)
	}
	if got := rec.Header().Get("x-ms-tag-count"); got != "1" {
		t.Fatalf("tag count after restart = %q, want 1", got)
	}

	rec = storagePlaneReq(t, restarted, http.MethodGet, account, "blob", "/"+container+"/kept.txt?comp=tags", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlobTags after restart")
	if !strings.Contains(rec.Body.String(), "<Value>sockerless</Value>") {
		t.Fatalf("tags after restart = %s", rec.Body.String())
	}

	rec = storagePlaneReq(t, restarted, http.MethodGet, account, "blob",
		"/"+container+"/kept.txt?snapshot="+snapshot, nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlob (snapshot) after restart")
	if got := rec.Body.String(); got != "v1" {
		t.Fatalf("snapshot contents after restart = %q, want %q", got, "v1")
	}

	rec = storagePlaneReq(t, restarted, http.MethodGet, account, "blob", "/"+container+"/disk.vhd?comp=pagelist", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetPageRanges after restart")
	var pageList struct {
		XMLName   xml.Name `xml:"PageList"`
		PageRange []struct {
			Start int64 `xml:"Start"`
			End   int64 `xml:"End"`
		} `xml:"PageRange"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &pageList); err != nil {
		t.Fatalf("parse PageList after restart: %v (%s)", err, rec.Body.String())
	}
	if len(pageList.PageRange) != 1 || pageList.PageRange[0].Start != 512 || pageList.PageRange[0].End != 1023 {
		t.Fatalf("page ranges after restart = %+v, want one range 512-1023", pageList.PageRange)
	}

	rec = storagePlaneReq(t, restarted, http.MethodGet, account, "blob",
		"/"+container+"?restype=container&comp=acl", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetContainerAccessPolicy after restart")
	if !strings.Contains(rec.Body.String(), "<Id>readers</Id>") {
		t.Fatalf("container ACL after restart = %s", rec.Body.String())
	}

	rec = storagePlaneReq(t, restarted, http.MethodGet, account, "blob", "/"+container+"?restype=container", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetContainerProperties after restart")
	if got := rec.Header().Get("x-ms-meta-team"); got != "storage" {
		t.Fatalf("container metadata after restart = %q, want %q", got, "storage")
	}
}

// TestBlobTagFilterExpressionIsEvaluated asserts the `where=` grammar of Find
// Blobs by Tags is parsed and applied, and that an expression outside the
// grammar is refused rather than matching everything.
func TestBlobTagFilterExpressionIsEvaluated(t *testing.T) {
	tags := map[string]string{"project": "sockerless", "stage": "beta", "rank": "20"}
	for _, tc := range []struct {
		where string
		match bool
	}{
		{"project = 'sockerless'", true},
		{"project = 'other'", false},
		{"project = 'sockerless' AND stage = 'beta'", true},
		{"project = 'sockerless' AND stage = 'ga'", false},
		{`"project" = 'sockerless'`, true},
		{"rank >= '20'", true},
		{"rank > '20'", false},
		{"missing = 'anything'", false},
	} {
		filter, err := parseBlobTagFilter(tc.where)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.where, err)
		}
		if got := filter.matches(tags); got != tc.match {
			t.Fatalf("%q matched = %v, want %v", tc.where, got, tc.match)
		}
	}

	for _, where := range []string{
		"project = 'a' OR stage = 'b' AND rank = 'c'",
		"(project = 'a')",
		"@container = 'x'",
		"project",
	} {
		if _, err := parseBlobTagFilter(where); err == nil {
			t.Fatalf("%q parsed, but it is outside the supported grammar and must be refused", where)
		}
	}
}
