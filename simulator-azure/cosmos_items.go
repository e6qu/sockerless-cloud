package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// cosmosCollLocks serializes the writes to one container. A write reads the
// item, checks the request's preconditions, stores the new version and records
// it in the change log as one step, so an If-Match cannot pass against a
// version another writer is replacing, and the change feed never shows a later
// sequence number before an earlier one.
var cosmosCollLocks kvstore.RWLocks

// cosmosChanges records every item write and delete per container, which the
// all-versions-and-deletes change feed reads. Azure Cosmos DB keeps those
// versions for the account's continuous-backup retention, at most 30 days.
var cosmosChanges *kvstore.ChangeLog[CosmosDocument]

const (
	cosmosChangeRetention   = 30 * 24 * time.Hour
	cosmosTTLSweepInterval  = 5 * time.Second
	cosmosTTLSweeperName    = "Azure Cosmos DB time-to-live sweeper"
	cosmosThrottledMessage  = "Request rate is large. More Request Units may be needed, so no changes were made. Please retry this request later. Learn more: http://aka.ms/cosmosdb-error-429"
	cosmosThrottledSubState = "3200"
)

// cosmosNow is the clock expiry and throughput are judged by; tests substitute it.
var cosmosNow = time.Now

func registerCosmosItems(srv *sim.Server) {
	cosmosChanges = kvstore.NewChangeLog(
		sim.MakeStore[kvstore.Change[CosmosDocument]](srv.DB(), "cosmos_changes"), cosmosChangeRetention)
	cosmosRaiseETagFloor(cosmosChanges.LastSeq())
	kvstore.StartSweeper(srv, cosmosTTLSweeperName, cosmosTTLSweepInterval, func(now time.Time) {
		cosmosSweepExpired(now)
		cosmosChanges.Prune(now)
	})
}

func cosmosLockColl(account, db, coll string) func() {
	return cosmosCollLocks.Lock(true, cosmosDataCollKey(account, db, coll))
}

// cosmosDocsFor returns a container's live items, ordered by id.
func cosmosDocsFor(account, db, coll string) []CosmosDocument {
	ttl := cosmosContainerTTLOf(account, db, coll)
	now := cosmosNow()
	docs := make([]CosmosDocument, 0)
	for _, entry := range cosmosDocs.ListPrefix(cosmosDataCollKey(account, db, coll) + "/") {
		if !ttl.expired(entry.Item, now) {
			docs = append(docs, entry.Item)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return docs
}

// cosmosLiveDoc reads the item at key unless its time to live has run out:
// Azure Cosmos DB stops returning an expired item at once, whenever the
// background deletion gets to it.
func cosmosLiveDoc(key string) (CosmosDocument, bool) {
	doc, ok := cosmosDocs.Get(key)
	if !ok || cosmosContainerTTLOf(doc.Account, doc.DB, doc.Coll).expired(doc, cosmosNow()) {
		return CosmosDocument{}, false
	}
	return doc, true
}

// cosmosStoreDocKey stores a document under an explicit, partition-scoped store
// key (built by cosmosDocKeyPK) so two docs with the same id in different
// partitions remain distinct items. The caller holds the container's lock.
func cosmosStoreDocKey(key, account, db, coll, id string, body map[string]any) CosmosDocument {
	now := cosmosNow().UTC()
	previous, existed := cosmosDocs.Get(key)
	if existed && cosmosContainerTTLOf(account, db, coll).expired(previous, now) {
		cosmosRecordDelete(previous, key, true, now)
		existed = false
	}
	seq := cosmosETagSeq.Add(1)
	rid := previous.RID
	if !existed {
		rid = cosmosChildRID(account, db, coll, cosmosRIDKindDocument)
	}
	doc := CosmosDocument{
		ID:      id,
		Account: account,
		DB:      db,
		Coll:    coll,
		Body:    body,
		ETag:    fmt.Sprintf(`"%x-%x"`, now.Unix(), seq),
		RID:     rid,
		Self:    cosmosChildSelf(account, db, coll, "docs", rid),
		TS:      now.Unix(),
	}
	cosmosDocs.Put(key, doc)
	change := kvstore.Change[CosmosDocument]{Seq: seq, Key: key, Op: kvstore.OpCreate, Current: &doc, At: now}
	if existed {
		change.Op, change.Previous = kvstore.OpReplace, &previous
	}
	cosmosChanges.Append(cosmosDataCollKey(account, db, coll), change)
	return doc
}

// cosmosDeleteDoc deletes the item at key. The caller holds the container's lock.
func cosmosDeleteDoc(doc CosmosDocument, key string) {
	cosmosRecordDelete(doc, key, false, cosmosNow().UTC())
}

func cosmosRecordDelete(doc CosmosDocument, key string, expired bool, now time.Time) {
	cosmosDocs.Delete(key)
	cosmosChanges.Append(cosmosDataCollKey(doc.Account, doc.DB, doc.Coll), kvstore.Change[CosmosDocument]{
		Seq: cosmosETagSeq.Add(1), Key: key, Op: kvstore.OpDelete, Previous: &doc, At: now, Expired: expired,
	})
}

// cosmosDropItems deletes every item and every recorded change under prefix —
// a container's or a database's — as deleting that resource does.
func cosmosDropItems(prefix string) {
	cosmosDropRIDs(strings.TrimSuffix(prefix, "/"))
	for _, entry := range cosmosDocs.ListPrefix(prefix) {
		cosmosDocs.Delete(entry.ID)
	}
	cosmosChanges.Drop(prefix)
}

// cosmosContainerTTL is a container's time-to-live setting. Azure Cosmos DB
// expires nothing in a container without `defaultTtl`; with it, an item's own
// `ttl` overrides the default, and -1 on either means never.
type cosmosContainerTTL struct {
	enabled bool
	seconds int64
}

func (c cosmosContainerTTL) expired(doc CosmosDocument, now time.Time) bool {
	if !c.enabled {
		return false
	}
	ttl := c.seconds
	if raw, ok := doc.Body["ttl"]; ok && raw != nil {
		if n, ok := cosmosNumberOf(raw); ok {
			ttl = int64(n)
		}
	}
	return ttl > 0 && now.Unix() >= doc.TS+ttl
}

func cosmosContainerTTLOf(account, db, coll string) cosmosContainerTTL {
	if dc, ok := cosmosDataColls.Get(cosmosDataCollKey(account, db, coll)); ok && dc.DefaultTTL != nil {
		return cosmosContainerTTL{enabled: true, seconds: *dc.DefaultTTL}
	}
	if c, ok := cosmosARMContainer(account, db, coll); ok {
		res, _ := c.Properties["resource"].(map[string]any)
		if n, ok := cosmosNumberOf(res["defaultTtl"]); ok {
			return cosmosContainerTTL{enabled: true, seconds: int64(n)}
		}
	}
	return cosmosContainerTTL{}
}

// cosmosSweepExpired deletes every expired item of every container with a time
// to live, as the service's background deletion does, and returns how many.
func cosmosSweepExpired(now time.Time) int {
	containers := map[string][3]string{}
	for _, dc := range cosmosDataColls.List() {
		if dc.DefaultTTL != nil {
			containers[cosmosDataCollKey(dc.Account, dc.DB, dc.Coll)] = [3]string{dc.Account, dc.DB, dc.Coll}
		}
	}
	for _, c := range cosmosContainers.List() {
		if account, db, coll, ok := cosmosARMIDNames(c.ID); ok && coll != "" {
			containers[cosmosDataCollKey(account, db, coll)] = [3]string{account, db, coll}
		}
	}
	deleted := 0
	for stream, names := range containers {
		ttl := cosmosContainerTTLOf(names[0], names[1], names[2])
		if !ttl.enabled {
			continue
		}
		for _, entry := range cosmosDocs.ListPrefix(stream + "/") {
			if !ttl.expired(entry.Item, now) {
				continue
			}
			release := cosmosLockColl(names[0], names[1], names[2])
			if doc, ok := cosmosDocs.Get(entry.ID); ok && ttl.expired(doc, now) {
				cosmosRecordDelete(doc, entry.ID, true, now.UTC())
				deleted++
			}
			release()
		}
	}
	return deleted
}

var (
	cosmosAccountsByName   sim.GenerationIndex[CosmosAccount]
	cosmosARMContainersIdx sim.GenerationIndex[CosmosSQLContainer]
	cosmosSQLThroughputIdx sim.GenerationIndex[CosmosThroughput]
)

// cosmosAccountByName resolves the account a data-plane host names.
func cosmosAccountByName(name string) (CosmosAccount, bool) {
	return cosmosAccountsByName.Lookup(cosmosAccounts, name, func(a CosmosAccount) []string {
		return []string{a.Name}
	})
}

// cosmosARMContainer finds a container created through Azure Resource Manager
// by the names the data plane addresses it by. An index key of account/db
// answers whether the database holds any container.
func cosmosARMContainer(account, db, coll string) (CosmosSQLContainer, bool) {
	return cosmosARMContainersIdx.Lookup(cosmosContainers, cosmosDataCollKey(account, db, coll), cosmosARMContainerKeys)
}

func cosmosARMDatabaseHasContainers(account, db string) bool {
	_, ok := cosmosARMContainersIdx.Lookup(cosmosContainers, cosmosDataDBKey(account, db), cosmosARMContainerKeys)
	return ok
}

func cosmosARMContainerKeys(c CosmosSQLContainer) []string {
	account, db, coll, ok := cosmosARMIDNames(c.ID)
	if !ok || db == "" || coll == "" {
		return nil
	}
	return []string{cosmosDataDBKey(account, db), cosmosDataCollKey(account, db, coll)}
}

// cosmosProvisionedThroughput is the throughput a container's requests spend:
// its own dedicated RU/s, or else its database's, which the database's
// containers share. The key names the resource whose budget it is. A container
// with neither — a serverless account's, or a shared-throughput database's
// with no offer — has no provisioned budget.
func cosmosProvisionedThroughput(account, db, coll string) (string, kvstore.Limit, bool) {
	for _, key := range []string{cosmosDataCollKey(account, db, coll), cosmosDataDBKey(account, db)} {
		t, ok := cosmosSQLThroughputIdx.Lookup(cosmosThroughputs, key, cosmosSQLThroughputKeys)
		if !ok {
			continue
		}
		res, _ := t.Properties["resource"].(map[string]any)
		if ru := cosmosRUPerSecond(res, "throughput", "autoscaleSettings"); ru > 0 {
			return t.ID, kvstore.Limit{Rate: ru, Burst: ru}, true
		}
	}
	return "", kvstore.Limit{}, false
}

func cosmosSQLThroughputKeys(t CosmosThroughput) []string {
	if !strings.Contains(t.ID, "/sqlDatabases/") {
		return nil
	}
	account, db, coll, ok := cosmosARMIDNames(t.ID)
	if !ok || db == "" {
		return nil
	}
	if coll != "" {
		return []string{cosmosDataCollKey(account, db, coll)}
	}
	return []string{cosmosDataDBKey(account, db)}
}

// cosmosRUPerSecond reads manual RU/s, or an autoscale maximum: autoscale
// scales up to its maximum instantly, so the maximum is the rate that throttles.
func cosmosRUPerSecond(content map[string]any, manualField, autoscaleField string) float64 {
	if ru, ok := cosmosNumberOf(content[manualField]); ok && ru > 0 {
		return ru
	}
	if settings, ok := content[autoscaleField].(map[string]any); ok {
		if ru, ok := cosmosNumberOf(settings["maxThroughput"]); ok {
			return ru
		}
	}
	return 0
}

// cosmosMetered spends a container request's charge from the container's
// provisioned throughput. Azure Cosmos DB learns a request's charge only by
// running it, so it admits a request while the budget is positive, debits the
// charge afterwards, and answers 429 with x-ms-retry-after-ms while the debt
// is outstanding. The SDKs retry exactly that.
func cosmosMetered(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
		key, limit, provisioned := cosmosProvisionedThroughput(account, db, coll)
		if !provisioned {
			next(w, r)
			return
		}
		if admitted, wait := cosmosRUBuckets.Admit(key, limit, cosmosNow()); !admitted {
			w.Header().Set("x-ms-retry-after-ms", strconv.FormatInt(wait.Milliseconds(), 10))
			w.Header().Set("x-ms-substatus", cosmosThrottledSubState)
			w.Header().Set("x-ms-request-charge", "0")
			cosmosDataError(w, "TooManyRequests", cosmosThrottledMessage, http.StatusTooManyRequests)
			return
		}
		next(w, r)
		if charge, err := strconv.ParseFloat(w.Header().Get("x-ms-request-charge"), 64); err == nil && charge > 0 {
			cosmosRUBuckets.Charge(key, limit, charge, cosmosNow())
		}
	}
}

var cosmosRUBuckets kvstore.Buckets

// cosmosForgetContainer drops what a deleted container leaves behind: its
// settings, its dedicated throughput offer and that offer's budget.
func cosmosForgetContainer(account, db, coll string) {
	cosmosDataColls.Delete(cosmosDataCollKey(account, db, coll))
	cosmosForgetSQLThroughput(account, db, coll)
}

// cosmosIfMatch evaluates If-Match against an existing resource's ETag; an
// absent header sets no condition.
func cosmosIfMatch(r *http.Request, etag string) bool {
	header := r.Header.Get("If-Match")
	return header == "" || blobstore.ETagListMatches(header, etag)
}

func cosmosPreconditionFailed(w http.ResponseWriter) {
	cosmosDataError(w, "PreconditionFailed",
		"Operation cannot be performed because one of the specified precondition is not met.",
		http.StatusPreconditionFailed)
}
