package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A data-plane request may override the account's consistency level with
// `x-ms-consistency-level`; Azure Cosmos DB refuses one stronger than the
// account's default with 400. Under Session consistency every write returns an
// `x-ms-session-token` "<pkRangeId>:<lsn>" whose per-partition LSN only
// advances, and a read sending it back observes at least that version.

var cosmosConsistencyRank = map[string]int{
	"eventual":         0,
	"consistentprefix": 1,
	"session":          2,
	"boundedstaleness": 3,
	"strong":           4,
}

// The ARM account row carries the consistencyPolicy; without one, Session.
func cosmosAccountMaxConsistency(account string) string {
	if a, ok := cosmosAccountByName(account); ok {
		if cp, ok := a.Properties["consistencyPolicy"].(map[string]any); ok {
			if lvl, ok := cp["defaultConsistencyLevel"].(string); ok && lvl != "" {
				return lvl
			}
		}
	}
	return "Session"
}

func cosmosCheckConsistency(r *http.Request, account string) *cosmosWriteError {
	requested := strings.TrimSpace(r.Header.Get("x-ms-consistency-level"))
	if requested == "" {
		return nil
	}
	reqRank, ok := cosmosConsistencyRank[strings.ToLower(requested)]
	if !ok {
		return &cosmosWriteError{
			code:   "BadRequest",
			msg:    fmt.Sprintf("Invalid consistency level %q.", requested),
			status: http.StatusBadRequest,
		}
	}
	maxLevel := cosmosAccountMaxConsistency(account)
	maxRank := cosmosConsistencyRank[strings.ToLower(maxLevel)]
	if reqRank > maxRank {
		return &cosmosWriteError{
			code: "BadRequest",
			msg: fmt.Sprintf(
				"Requested consistency level %q cannot be satisfied: the account's maximum consistency level is %q.",
				requested, maxLevel),
			status: http.StatusBadRequest,
		}
	}
	return nil
}

func cosmosGuardConsistency(w http.ResponseWriter, r *http.Request, account string) bool {
	if werr := cosmosCheckConsistency(r, account); werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return true
	}
	return false
}

// The sync.Maps are working copies; cosmosSessions keeps LSNs and range IDs
// monotonic across a SIM_PERSIST restart, as Azure Cosmos DB never rewinds a
// partition's LSN.
var (
	cosmosPartitionLSNs   sync.Map // string -> *atomic.Uint64
	cosmosPKRangeAssign   sync.Map // string (account/db/coll/pk) -> int (stable range id)
	cosmosPKRangeNextID   atomic.Uint64
	cosmosPKRangeAssignMu sync.Mutex
	cosmosSessions        sim.Store[cosmosSessionRow]
)

// Key repeats the store key because List carries values only.
type cosmosSessionRow struct {
	Key     string `json:"key"`
	RangeID int    `json:"rangeId"`
	LSN     uint64 `json:"lsn"`
}

// cosmosInitSessionState seeds the next range ID above every persisted one.
func cosmosInitSessionState(srv *sim.Server) {
	cosmosSessions = sim.MakeStore[cosmosSessionRow](srv.DB(), "cosmos_session_partitions")
	var nextID uint64
	for _, row := range cosmosSessions.List() {
		cosmosPKRangeAssign.Store(row.Key, row.RangeID)
		counter := &atomic.Uint64{}
		counter.Store(row.LSN)
		cosmosPartitionLSNs.Store(row.Key, counter)
		if uint64(row.RangeID)+1 > nextID {
			nextID = uint64(row.RangeID) + 1
		}
	}
	if nextID > cosmosPKRangeNextID.Load() {
		cosmosPKRangeNextID.Store(nextID)
	}
}

// Keep the stored LSN monotonic when concurrent writers persist out of order.
func cosmosPersistSession(key string, rangeID int, lsn uint64) {
	if !cosmosSessions.Update(key, func(row *cosmosSessionRow) {
		row.Key = key
		row.RangeID = rangeID
		if lsn > row.LSN {
			row.LSN = lsn
		}
	}) {
		cosmosSessions.Put(key, cosmosSessionRow{Key: key, RangeID: rangeID, LSN: lsn})
	}
}

func cosmosSessionPartitionKey(account, db, coll, pkComponent string) string {
	return account + "/" + db + "/" + coll + "/" + pkComponent
}

// Each logical partition maps to its own stable range ID.
func cosmosPKRangeID(key string) int {
	if v, ok := cosmosPKRangeAssign.Load(key); ok {
		if id, ok := v.(int); ok {
			return id
		}
	}
	cosmosPKRangeAssignMu.Lock()
	defer cosmosPKRangeAssignMu.Unlock()
	if v, ok := cosmosPKRangeAssign.Load(key); ok {
		if id, ok := v.(int); ok {
			return id
		}
	}
	id := int(cosmosPKRangeNextID.Add(1) - 1)
	cosmosPKRangeAssign.Store(key, id)
	cosmosPersistSession(key, id, cosmosCurrentSessionLSN(key))
	return id
}

func cosmosAdvanceSessionLSN(key string) uint64 {
	v, _ := cosmosPartitionLSNs.LoadOrStore(key, &atomic.Uint64{})
	c, ok := v.(*atomic.Uint64)
	if !ok {
		return 0
	}
	lsn := c.Add(1)
	cosmosPersistSession(key, cosmosPKRangeID(key), lsn)
	return lsn
}

// cosmosCurrentSessionLSN returns the current per-partition LSN (0 if no write
// has happened yet) without advancing it.
func cosmosCurrentSessionLSN(key string) uint64 {
	v, ok := cosmosPartitionLSNs.Load(key)
	if !ok {
		return 0
	}
	c, ok := v.(*atomic.Uint64)
	if !ok {
		return 0
	}
	return c.Load()
}

func cosmosSessionToken(rangeID int, lsn uint64) string {
	return strconv.Itoa(rangeID) + ":" + strconv.FormatUint(lsn, 10)
}

func cosmosSetWriteSession(w http.ResponseWriter, account, db, coll, pkComponent string) {
	key := cosmosSessionPartitionKey(account, db, coll, pkComponent)
	lsn := cosmosAdvanceSessionLSN(key)
	w.Header().Set("x-ms-session-token", cosmosSessionToken(cosmosPKRangeID(key), lsn))
}

// A read returns the current partition LSN, at least the version the client's
// token requested.
func cosmosEchoReadSession(w http.ResponseWriter, r *http.Request, account, db, coll, pkComponent string) {
	key := cosmosSessionPartitionKey(account, db, coll, pkComponent)
	lsn := cosmosCurrentSessionLSN(key)
	if client := r.Header.Get("x-ms-session-token"); client != "" {
		if _, clientLSN, ok := cosmosParseSessionToken(client); ok && clientLSN > lsn {
			lsn = clientLSN
		}
	}
	w.Header().Set("x-ms-session-token", cosmosSessionToken(cosmosPKRangeID(key), lsn))
}

func cosmosParseSessionToken(tok string) (rangeID int, lsn uint64, ok bool) {
	i := strings.LastIndexByte(tok, ':')
	if i < 0 {
		return 0, 0, false
	}
	rid, err1 := strconv.Atoi(tok[:i])
	l, err2 := strconv.ParseUint(tok[i+1:], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return rid, l, true
}
