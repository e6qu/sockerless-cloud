package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Azure Cosmos DB change feed and conflict feed. A documents read carrying
// `A-IM` is a change-feed read:
//
//   - `Incremental feed` (latest version): each item changed since the
//     continuation, once, as it is now; deletes are absent.
//   - `Full-Fidelity Feed` (all versions and deletes): every create, replace
//     and delete, time-to-live deletes included, each with its metadata. Only
//     accounts with continuous backup serve it, for that tier's retention.
//
// The continuation rides in If-None-Match and advances in the response ETag;
// `*` starts from now. A latest-version read without one starts from the
// beginning, or from If-Modified-Since. Nothing new answers 304. The conflict
// feed is always empty: the accounts are single-region, single-writer.

func registerCosmosChangeFeed(srv *sim.Server) {
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/conflicts", handleCosmosListConflicts)
}

type cosmosFeedMode int

const (
	cosmosFeedNone cosmosFeedMode = iota
	cosmosFeedLatestVersion
	cosmosFeedAllVersions
)

func cosmosChangeFeedMode(r *http.Request) cosmosFeedMode {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("A-IM"))) {
	case "incremental feed":
		return cosmosFeedLatestVersion
	case "full-fidelity feed":
		return cosmosFeedAllVersions
	}
	return cosmosFeedNone
}

func cosmosIsChangeFeedRequest(r *http.Request) bool {
	return cosmosChangeFeedMode(r) != cosmosFeedNone
}

// An ETag is "<hex-ts>-<hex-seq>"; the sequence is a process-wide logical
// clock, which orders the change feed and is its continuation.
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

func cosmosContinuationETag(seq uint64) string {
	return `"0-` + strconv.FormatUint(seq, 16) + `"`
}

// cosmosFeedStart: after the continuation's sequence, after the latest write
// for `*`, and from the beginning (second result) when it carries neither.
func cosmosFeedStart(r *http.Request) (uint64, bool) {
	cont := strings.TrimSpace(r.Header.Get("If-None-Match"))
	switch cont {
	case "":
		return 0, false
	case "*":
		return cosmosETagSeq.Load(), true
	}
	return cosmosETagSeqOf(cont), true
}

func handleCosmosChangeFeed(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	pkComponent, scoped, werr := cosmosResolvePKForPoint(r, account, db, coll)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	inPartition := func(d CosmosDocument) bool {
		return !scoped || cosmosDocPKComponent(account, db, coll, d) == pkComponent
	}
	sinceSeq, resumed := cosmosFeedStart(r)
	if cosmosChangeFeedMode(r) == cosmosFeedAllVersions {
		cosmosAllVersionsFeed(w, r, account, db, coll, sinceSeq, resumed, inPartition)
		return
	}

	var since time.Time
	if !resumed {
		if raw := r.Header.Get("If-Modified-Since"); raw != "" {
			parsed, err := http.ParseTime(raw)
			if err != nil {
				cosmosDataError(w, "BadRequest", "The If-Modified-Since header is not a valid HTTP date: "+raw, http.StatusBadRequest)
				return
			}
			since = parsed
		}
	}
	changed := make([]CosmosDocument, 0)
	for _, d := range cosmosDocsFor(account, db, coll) {
		if inPartition(d) && cosmosETagSeqOf(d.ETag) > sinceSeq && (since.IsZero() || d.TS >= since.Unix()) {
			changed = append(changed, d)
		}
	}
	sort.Slice(changed, func(i, j int) bool {
		return cosmosETagSeqOf(changed[i].ETag) < cosmosETagSeqOf(changed[j].ETag)
	})
	page := changed
	if maxItems := cosmosMaxItemCount(r); maxItems >= 0 && maxItems < len(page) {
		page = page[:maxItems]
	}
	nextSeq := sinceSeq
	if len(page) > 0 {
		nextSeq = cosmosETagSeqOf(page[len(page)-1].ETag)
	}
	if len(page) == 0 && resumed {
		w.Header().Set("etag", cosmosContinuationETag(nextSeq))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, d := range page {
		out = append(out, cosmosDocBody(d))
	}
	w.Header().Set("etag", cosmosContinuationETag(nextSeq))
	w.Header().Set("x-ms-item-count", strconv.Itoa(len(out)))
	cosmosWriteData(w, http.StatusOK, map[string]any{"Documents": out, "_count": len(out)})
}

// An account without continuous backup cannot serve the all-versions mode.
func cosmosAccountChangeRetention(account string) (time.Duration, bool) {
	a, ok := cosmosAccountByName(account)
	if !ok {
		return 0, false
	}
	policy, _ := a.Properties["backupPolicy"].(map[string]any)
	if !strings.EqualFold(cosmosString(policy["type"]), "Continuous") {
		return 0, false
	}
	if props, _ := policy["continuousModeProperties"].(map[string]any); strings.EqualFold(cosmosString(props["tier"]), "Continuous7Days") {
		return 7 * 24 * time.Hour, true
	}
	return cosmosChangeRetention, true
}

func cosmosString(v any) string {
	s, _ := v.(string)
	return s
}

func cosmosAllVersionsFeed(w http.ResponseWriter, r *http.Request, account, db, coll string, sinceSeq uint64, resumed bool, inPartition func(CosmosDocument) bool) {
	retention, continuous := cosmosAccountChangeRetention(account)
	if !continuous {
		cosmosDataError(w, "BadRequest",
			"The all versions and deletes change feed mode requires continuous backup on the account.", http.StatusBadRequest)
		return
	}
	if !resumed {
		cosmosDataError(w, "BadRequest",
			"The all versions and deletes change feed mode starts from now or from a continuation; set If-None-Match.", http.StatusBadRequest)
		return
	}
	pkPath, _ := cosmosContainerPKPath(account, db, coll)
	cutoff := cosmosNow().Add(-retention)
	maxItems := cosmosMaxItemCount(r)
	out := make([]map[string]any, 0)
	nextSeq := sinceSeq
	for _, change := range cosmosChanges.Read(cosmosDataCollKey(account, db, coll), sinceSeq, 0) {
		if maxItems >= 0 && len(out) == maxItems {
			break
		}
		nextSeq = change.Seq
		subject := change.Current
		if subject == nil {
			subject = change.Previous
		}
		if change.At.Before(cutoff) || subject == nil || !inPartition(*subject) {
			continue
		}
		out = append(out, cosmosChangeFeedEntry(change, pkPath))
	}
	if len(out) == 0 {
		w.Header().Set("etag", cosmosContinuationETag(nextSeq))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("etag", cosmosContinuationETag(nextSeq))
	w.Header().Set("x-ms-item-count", strconv.Itoa(len(out)))
	cosmosWriteData(w, http.StatusOK, map[string]any{"Documents": out, "_count": len(out)})
}

// The item after the change goes in `current`, and `metadata` carries the log
// sequence number, conflict-resolution timestamp, operation type, the replaced
// version's sequence number and whether time to live caused it. A delete names
// the deleted item's id and partition key in its metadata instead.
func cosmosChangeFeedEntry(change kvstore.Change[CosmosDocument], pkPath string) map[string]any {
	metadata := map[string]any{
		"lsn":               change.Seq,
		"crts":              change.At.Unix(),
		"operationType":     string(change.Op),
		"timeToLiveExpired": change.Expired,
	}
	if change.Previous != nil {
		metadata["previousImageLSN"] = cosmosETagSeqOf(change.Previous.ETag)
	}
	entry := map[string]any{"metadata": metadata}
	if change.Current != nil {
		entry["current"] = cosmosDocBody(*change.Current)
		return entry
	}
	metadata["id"] = change.Previous.ID
	if pkPath != "" {
		if value, ok := cosmosPKValueFromBody(change.Previous.Body, pkPath); ok {
			metadata["partitionKey"] = map[string]any{strings.TrimPrefix(pkPath, "/"): value}
		}
	}
	return entry
}

func handleCosmosListConflicts(w http.ResponseWriter, r *http.Request) {
	cosmosWriteData(w, http.StatusOK, map[string]any{
		"Conflicts": []map[string]any{},
		"_count":    0,
	})
}
