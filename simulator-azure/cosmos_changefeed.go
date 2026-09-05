package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cosmos DB change feed + conflict feed.
//
// Change feed: a read of the container's documents in the order they were
// created/changed, advanced by an opaque continuation. A client opts in with the
// `A-IM: Incremental feed` request header; the SDK / REST client then carries a
// continuation in `If-None-Match`, and the response advances the continuation in
// `etag`. Faithful incremental semantics: a fresh read (no continuation) returns
// every document; after consuming, a subsequent read with the returned
// continuation returns only documents created/changed since — and 304 Not
// Modified when nothing changed.
//
// The change feed shares the documents route (GET .../docs) with the plain list
// handler; handleCosmosDataListDocs delegates here when the A-IM header is
// present (the only edit to cosmos.go beyond route registration).
//
// Conflict feed: the /conflicts resource read. Single-writer (single-region) is
// the only mode the sim runs, so the conflict feed is always empty — which is
// the correct, faithful result for a single-region account; the resource and
// its response shape exist and round-trip.

func registerCosmosChangeFeed(srv *sim.Server) {
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/conflicts", handleCosmosListConflicts)
}

// cosmosIsChangeFeedRequest reports whether the documents GET is a change-feed
// read (the `A-IM: Incremental feed` header real Cosmos uses to switch the docs
// read into change-feed mode).
func cosmosIsChangeFeedRequest(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("A-IM")), "Incremental feed")
}

// cosmosETagSeqOf extracts the monotonic sequence component the document ETag
// encodes ("<hex-ts>-<hex-seq>"). It is a strictly-increasing logical clock
// across every write in the process, so it orders the change feed and serves as
// the continuation cursor. A document with an unparseable ETag sorts last.
func cosmosETagSeqOf(etag string) uint64 {
	s := strings.Trim(etag, `"`)
	i := strings.LastIndexByte(s, '-')
	if i < 0 {
		return 0
	}
	n, err := strconv.ParseUint(s[i+1:], 16, 64)
	if err != nil {
		return 0
	}
	return n
}

// handleCosmosChangeFeed serves the incremental change feed for a collection,
// scoped to a single partition when the partition-key header is present (real
// Cosmos change feed is per-partition-key-range; a pk header pins one logical
// partition). The continuation is the last-seen ETag sequence; documents with a
// greater sequence are returned in ascending sequence order.
func handleCosmosChangeFeed(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")

	// Optional single-partition scoping (the pk header).
	pkComponent, scoped, werr := cosmosResolvePKForPoint(r, account, db, coll)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}

	docs := cosmosDocsFor(account, db, coll)
	if scoped {
		filtered := docs[:0:0]
		for _, d := range docs {
			if cosmosDocPKComponent(account, db, coll, d) == pkComponent {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}

	// The continuation rides in If-None-Match as the last-consumed ETag.
	// Absent → a fresh feed (all documents). Real Cosmos accepts both the raw
	// ETag and a session-token-shaped continuation; the sim uses the ETag.
	var sinceSeq uint64
	if cont := strings.Trim(r.Header.Get("If-None-Match"), `"`); cont != "" {
		sinceSeq = cosmosETagSeqOf(cont)
	}

	// Order by the monotonic ETag sequence and return only changes after the
	// continuation.
	sort.Slice(docs, func(i, j int) bool {
		return cosmosETagSeqOf(docs[i].ETag) < cosmosETagSeqOf(docs[j].ETag)
	})
	changed := docs[:0:0]
	for _, d := range docs {
		if cosmosETagSeqOf(d.ETag) > sinceSeq {
			changed = append(changed, d)
		}
	}

	// max-item-count caps a single page; the continuation advances to the last
	// returned document so the next read resumes after it.
	maxItems := cosmosMaxItemCount(r)
	page := changed
	if maxItems >= 0 && maxItems < len(page) {
		page = page[:maxItems]
	}

	// The advanced continuation = the highest ETag seq in the page (or the
	// incoming continuation when the page is empty, so the cursor never rewinds).
	nextSeq := sinceSeq
	if len(page) > 0 {
		nextSeq = cosmosETagSeqOf(page[len(page)-1].ETag)
	}
	// Render the continuation as an ETag-shaped token the client echoes back in
	// If-None-Match on the next read.
	nextETag := `"0-` + strconv.FormatUint(nextSeq, 16) + `"`

	// Nothing new since the continuation → 304 Not Modified, the real Cosmos
	// "no changes" response (the client keeps its existing continuation).
	if len(page) == 0 && sinceSeq > 0 {
		w.Header().Set("etag", nextETag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	out := make([]map[string]any, 0, len(page))
	for _, d := range page {
		out = append(out, cosmosDocBody(d))
	}
	w.Header().Set("etag", nextETag)
	w.Header().Set("x-ms-item-count", strconv.Itoa(len(out)))
	cosmosWriteData(w, http.StatusOK, map[string]any{"Documents": out, "_count": len(out)})
}

// handleCosmosListConflicts serves the conflict-feed read. The sim is
// single-writer / single-region, so there are never any conflicts to resolve —
// an empty conflict feed is the correct faithful result. The resource and its
// response shape (Conflicts/_count) exist and round-trip.
func handleCosmosListConflicts(w http.ResponseWriter, r *http.Request) {
	cosmosWriteData(w, http.StatusOK, map[string]any{
		"Conflicts": []map[string]any{},
		"_count":    0,
	})
}
