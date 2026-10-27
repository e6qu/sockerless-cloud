package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Cosmos DB resource ids (`_rid`) are hierarchical binary ids: a
// database's is 4 bytes, a container's is its database's plus 4, and a
// container child's (item, stored procedure, trigger, user-defined function)
// is its container's plus 8, the last byte's top four bits carrying the kind.
// The service spells an id as base64 with '/' replaced by '-', and addresses a
// resource by id as well as by name: dbs/{dbRid}/colls/{collRid}/docs/{docRid}.

const (
	cosmosRIDKindDocument            byte = 0x0
	cosmosRIDKindUserDefinedFunction byte = 0x6
	cosmosRIDKindTrigger             byte = 0x7
	cosmosRIDKindStoredProcedure     byte = 0x8
)

// Keyed by the resource's data-plane key, with a container's per-kind child
// counters.
type cosmosRIDRecord struct {
	Key      string          `json:"key"`
	RID      []byte          `json:"rid"`
	Children map[byte]uint64 `json:"children,omitempty"`
}

var (
	cosmosRIDs      sim.Store[cosmosRIDRecord]
	cosmosRIDsByRID sim.GenerationIndex[cosmosRIDRecord]
	cosmosRIDMu     sync.Mutex
)

func registerCosmosRIDs(srv *sim.Server) {
	cosmosRIDs = sim.MakeStore[cosmosRIDRecord](srv.DB(), "cosmos_resource_ids")
}

func cosmosEncodeRID(rid []byte) string {
	return strings.ReplaceAll(base64.StdEncoding.EncodeToString(rid), "/", "-")
}

func cosmosDecodeRID(s string) ([]byte, bool) {
	rid, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(s, "-", "/"))
	if err != nil {
		return nil, false
	}
	return rid, true
}

func cosmosRIDRecordByRID(rid []byte) (cosmosRIDRecord, bool) {
	return cosmosRIDsByRID.Lookup(cosmosRIDs, string(rid), func(rec cosmosRIDRecord) []string {
		return []string{string(rec.RID)}
	})
}

// cosmosMintRID mints parent followed by n random bytes, unique across the
// simulator, when key has none.
func cosmosMintRID(key string, parent []byte, n int) []byte {
	cosmosRIDMu.Lock()
	defer cosmosRIDMu.Unlock()
	if rec, ok := cosmosRIDs.Get(key); ok {
		return rec.RID
	}
	for {
		own := make([]byte, n)
		if _, err := rand.Read(own); err != nil {
			panic(fmt.Sprintf("crypto/rand: %v", err))
		}
		// A container's id and a user's share their slot; a set top bit in the slot's
		// last byte marks a user.
		own[n-1] &^= 0x80
		if bytes.Equal(own, make([]byte, n)) {
			continue
		}
		rid := append(append([]byte(nil), parent...), own...)
		if _, taken := cosmosRIDRecordByRID(rid); taken {
			continue
		}
		cosmosRIDs.Put(key, cosmosRIDRecord{Key: key, RID: rid})
		return rid
	}
}

func cosmosDatabaseRIDBytes(account, db string) []byte {
	return cosmosMintRID(cosmosDataDBKey(account, db), nil, 4)
}

func cosmosCollectionRIDBytes(account, db, coll string) []byte {
	return cosmosMintRID(cosmosDataCollKey(account, db, coll), cosmosDatabaseRIDBytes(account, db), 4)
}

func cosmosDatabaseRID(account, db string) string {
	return cosmosEncodeRID(cosmosDatabaseRIDBytes(account, db))
}

func cosmosCollectionRID(account, db, coll string) string {
	return cosmosEncodeRID(cosmosCollectionRIDBytes(account, db, coll))
}

// cosmosChildRID appends the container's next number for the kind,
// little-endian, with the kind in the last byte's top four bits.
func cosmosChildRID(account, db, coll string, kind byte) string {
	collRID := cosmosCollectionRIDBytes(account, db, coll)
	key := cosmosDataCollKey(account, db, coll)
	cosmosRIDMu.Lock()
	defer cosmosRIDMu.Unlock()
	var n uint64
	cosmosRIDs.Update(key, func(rec *cosmosRIDRecord) {
		if rec.Children == nil {
			rec.Children = map[byte]uint64{}
		}
		rec.Children[kind]++
		n = rec.Children[kind]
	})
	child := make([]byte, 8)
	binary.LittleEndian.PutUint64(child, n)
	child[7] = child[7]&0x0f | kind<<4
	return cosmosEncodeRID(append(append([]byte(nil), collRID...), child...))
}

func cosmosChildSelf(account, db, coll, segment, rid string) string {
	return "dbs/" + cosmosDatabaseRID(account, db) + "/colls/" + cosmosCollectionRID(account, db, coll) + "/" + segment + "/" + rid + "/"
}

// A resource created again under the same name gets a new id, as on the
// service.
func cosmosDropRIDs(key string) {
	cosmosRIDs.Delete(key)
	for _, entry := range cosmosRIDs.ListPrefix(key + "/") {
		cosmosRIDs.Delete(entry.ID)
	}
}

// Each id segment decodes to an id of its level's length that extends its
// parent's.
func cosmosRIDPath(segments []string) bool {
	if len(segments) < 2 || segments[0] != "dbs" {
		return false
	}
	var parent []byte
	for i, want := range []int{4, 8, 16} {
		at := 1 + 2*i
		if at >= len(segments) {
			break
		}
		if want == 16 && !cosmosRIDChildSegments[segments[at-1]] {
			break
		}
		rid, ok := cosmosDecodeRID(segments[at])
		if !ok || len(rid) != want || !bytes.HasPrefix(rid, parent) {
			return false
		}
		parent = rid
	}
	return true
}

var cosmosRIDChildSegments = map[string]bool{"docs": true, "sprocs": true, "triggers": true, "udfs": true}

func cosmosResolveRIDPath(account string, segments []string) ([]string, bool) {
	out := append([]string(nil), segments...)
	var db, coll string
	for at := 1; at < len(out); at += 2 {
		rid, _ := cosmosDecodeRID(out[at])
		switch at {
		case 1, 3:
			rec, ok := cosmosRIDRecordByRID(rid)
			if !ok {
				return nil, false
			}
			parts := strings.Split(rec.Key, "/")
			if parts[0] != account || len(parts) != (at+1)/2+1 {
				return nil, false
			}
			db = parts[1]
			if at == 3 {
				coll = parts[2]
			}
			out[at] = parts[len(parts)-1]
		case 5:
			name, ok := cosmosChildNameByRID(account, db, coll, out[4], out[at])
			if !ok {
				return nil, false
			}
			out[at] = name
		default:
			return out, true
		}
	}
	return out, true
}

func cosmosChildNameByRID(account, db, coll, segment, rid string) (string, bool) {
	prefix := cosmosDataCollKey(account, db, coll) + "/"
	if segment == "docs" {
		for _, entry := range cosmosDocs.ListPrefix(prefix) {
			if entry.Item.RID == rid {
				return entry.Item.ID, true
			}
		}
		return "", false
	}
	for _, entry := range cosmosScripts.ListPrefix(prefix) {
		if entry.Item.RID == rid && cosmosScriptSelfSeg(entry.Item.Kind) == segment {
			return entry.Item.ID, true
		}
	}
	return "", false
}

// cosmosRewriteRIDRequest answers 404 when an id names nothing.
func cosmosRewriteRIDRequest(w http.ResponseWriter, r *http.Request) bool {
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if !cosmosRIDPath(segments) {
		return true
	}
	named, ok := cosmosResolveRIDPath(cosmosDataAccount(r), segments)
	if !ok {
		cosmosDataError(w, "NotFound", "Resource Not Found. Learn more: https://aka.ms/cosmosdb-tsg-not-found", http.StatusNotFound)
		return false
	}
	r.URL.Path = "/" + strings.Join(named, "/")
	r.URL.RawPath = ""
	return true
}
