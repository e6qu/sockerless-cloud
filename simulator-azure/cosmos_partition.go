package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Partition-key handling and query pagination for the Cosmos data plane.
//
// Real Cosmos identifies an item by (partition-key value, id): two documents
// with the same id in different logical partitions are distinct items, and a
// point operation must address the right partition. The azcosmos SDK carries the
// partition key in the `x-ms-documentdb-partitionkey` header as a JSON array
// (e.g. ["p"], [30], [true], [null]); the container declares its partition-key
// path (e.g. /pk) at create time. These helpers extract, validate, and key on
// that value, faithfully matching the emulator (verified by the differential).

// CosmosDataColl records a data-plane-created collection's partition-key path.
// The azcosmos SDK (and the real REST API) create containers via the DATA plane
// (POST /dbs/{db}/colls), not ARM, so the declared partition key lives here, not
// in the ARM cosmosContainers store.
type CosmosDataColl struct {
	Account string
	DB      string
	Coll    string
	PKPath  string
}

func cosmosDataCollKey(account, db, coll string) string {
	return account + "/" + db + "/" + coll
}

// cosmosContainerPKPath returns the container's declared partition-key path
// (e.g. "/pk"). It consults the data-plane collection store first (where the SDK
// and REST clients declare it), then the ARM container store. ok is false when
// no container was declared anywhere (some raw-HTTP tests POST documents into an
// undeclared collection); callers then key by id alone, preserving that legacy
// reachability.
func cosmosContainerPKPath(account, db, coll string) (string, bool) {
	if dc, ok := cosmosDataColls.Get(cosmosDataCollKey(account, db, coll)); ok && dc.PKPath != "" {
		return dc.PKPath, true
	}
	for _, c := range cosmosContainers.List() {
		a, cdb, ccoll, ok := cosmosARMIDNames(c.ID)
		if !ok || a != account || cdb != db || ccoll != coll {
			continue
		}
		res, _ := c.Properties["resource"].(map[string]any)
		if res == nil {
			return "", false
		}
		pkDef, _ := res["partitionKey"].(map[string]any)
		if pkDef == nil {
			return "", false
		}
		paths, _ := pkDef["paths"].([]any)
		if len(paths) == 0 {
			// paths may have been stored as a []string by the ARM handler.
			if sp, ok := pkDef["paths"].([]string); ok && len(sp) > 0 {
				return sp[0], true
			}
			return "", false
		}
		if p, ok := paths[0].(string); ok && p != "" {
			return p, true
		}
		return "", false
	}
	return "", false
}

// cosmosPKValueFromBody reads the partition-key value out of a document body at
// the container's PK path. A nested path ("/a/b") traverses sub-objects. The
// returned bool reports whether a value was present at the path.
func cosmosPKValueFromBody(body map[string]any, pkPath string) (any, bool) {
	segs := cosmosSplitPatchPath(pkPath)
	if len(segs) == 0 {
		return nil, false
	}
	var cur any = body
	for _, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// cosmosPKFromHeader parses the x-ms-documentdb-partitionkey header (a JSON
// array of one component). ok is false when the header is absent. err is set
// when the header is present but malformed (a 400 to the caller).
func cosmosPKFromHeader(r *http.Request) (value any, ok bool, err error) {
	raw := r.Header.Get("x-ms-documentdb-partitionkey")
	if raw == "" {
		return nil, false, nil
	}
	var arr []any
	if jerr := json.Unmarshal([]byte(raw), &arr); jerr != nil {
		return nil, false, fmt.Errorf("invalid partition key header %q: %v", raw, jerr)
	}
	if len(arr) == 0 {
		// An empty array ([]) is the SDK's "no partition key" sentinel
		// (NewPartitionKey()); treat as absent.
		return nil, false, nil
	}
	return arr[0], true, nil
}

// cosmosCanonPKValue canonicalizes a partition-key value (from a header or a
// document body) to a stable string used in the store key. Numbers from the two
// sources decode differently (float64 vs the header's JSON number), so unify on
// the JSON marshaling of the value.
func cosmosCanonPKValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return "s:" + t
	case bool:
		if t {
			return "b:true"
		}
		return "b:false"
	case float64:
		return "n:" + strconv.FormatFloat(t, 'g', -1, 64)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return "n:" + strconv.FormatFloat(f, 'g', -1, 64)
		}
		return "n:" + string(t)
	}
	b, _ := json.Marshal(v)
	return "j:" + string(b)
}

// cosmosValuesEqualPK reports whether two partition-key values (one possibly
// from the header, one from a body) denote the same logical partition.
func cosmosValuesEqualPK(a, b any) bool {
	return cosmosCanonPKValue(a) == cosmosCanonPKValue(b)
}

// cosmosDocKeyPK builds the store key for a document scoped to its partition.
// When the container has no declared PK path (undeclared collection), pkValue
// is "" and the key collapses to the legacy id-only form so existing reachability
// is preserved.
func cosmosDocKeyPK(account, db, container, pkValue, doc string) string {
	if pkValue == "" {
		return cosmosDocKey(account, db, container, doc)
	}
	return account + "/" + db + "/" + container + "/" + pkValue + "/" + doc
}

// cosmosResolvePKForWrite determines the partition-key value for a create/upsert/
// replace, enforcing the real-Cosmos rule that a supplied header must match the
// value at the container's PK path inside the document. It returns the canonical
// pk-key component for the store key. When the container has no declared PK path,
// it returns "" (legacy id-only keying).
func cosmosResolvePKForWrite(r *http.Request, account, db, coll string, body map[string]any) (pkKeyComponent string, herr *cosmosWriteError) {
	pkPath, declared := cosmosContainerPKPath(account, db, coll)
	headerVal, headerOK, perr := cosmosPKFromHeader(r)
	if perr != nil {
		return "", &cosmosWriteError{code: "BadRequest", msg: perr.Error(), status: http.StatusBadRequest}
	}
	if !declared {
		// No declared PK path → key by id alone (legacy raw-HTTP collections).
		return "", nil
	}
	bodyVal, bodyOK := cosmosPKValueFromBody(body, pkPath)
	if headerOK {
		// Real Cosmos rejects a header that doesn't match the document's value.
		if !bodyOK || !cosmosValuesEqualPK(headerVal, bodyVal) {
			return "", &cosmosWriteError{
				code:   "BadRequest",
				msg:    "PartitionKey extracted from document doesn't match the one specified in the header",
				status: http.StatusBadRequest,
			}
		}
		return cosmosCanonPKValue(headerVal), nil
	}
	// No header (raw-HTTP write): derive the partition from the document body.
	if bodyOK {
		return cosmosCanonPKValue(bodyVal), nil
	}
	// Declared PK path but the document carries no value there: real Cosmos
	// treats this as the "undefined" partition. Use a stable sentinel.
	return cosmosCanonPKValue(nil), nil
}

// cosmosResolvePKForPoint determines the store key for a point read/replace/
// delete/patch addressed by (partition key, id). When the SDK sends the pk
// header the lookup is exact; without it (legacy raw-HTTP) the caller falls back
// to an id scan via cosmosFindDocByID.
func cosmosResolvePKForPoint(r *http.Request, account, db, coll string) (pkKeyComponent string, hasHeader bool, herr *cosmosWriteError) {
	_, declared := cosmosContainerPKPath(account, db, coll)
	headerVal, headerOK, perr := cosmosPKFromHeader(r)
	if perr != nil {
		return "", false, &cosmosWriteError{code: "BadRequest", msg: perr.Error(), status: http.StatusBadRequest}
	}
	if !declared || !headerOK {
		return "", false, nil
	}
	return cosmosCanonPKValue(headerVal), true, nil
}

// cosmosFindDocByID scans a collection for a document with the given id,
// ignoring the partition (the legacy raw-HTTP point-read path that omits the pk
// header). The first match by sorted order is returned.
func cosmosFindDocByID(account, db, coll, id string) (CosmosDocument, bool) {
	for _, d := range cosmosDocsFor(account, db, coll) {
		if d.ID == id {
			return d, true
		}
	}
	return CosmosDocument{}, false
}

// cosmosWriteError carries a Cosmos data-plane error to the calling HTTP handler.
type cosmosWriteError struct {
	code   string
	msg    string
	status int
}

// ── query pagination ──────────────────────────────────────────────────────────

// cosmosMaxItemCount reads the x-ms-max-item-count request header. -1 means no
// cap (the header absent or -1, the SDK's "unbounded" sentinel).
func cosmosMaxItemCount(r *http.Request) int {
	raw := r.Header.Get("x-ms-max-item-count")
	if raw == "" {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// cosmosContinuationOffset decodes the x-ms-continuation request header into the
// next result offset. An absent header is offset 0; a malformed token is a 400.
func cosmosContinuationOffset(r *http.Request) (int, error) {
	raw := r.Header.Get("x-ms-continuation")
	if raw == "" {
		return 0, nil
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid continuation token")
	}
	n, err := strconv.Atoi(string(dec))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid continuation token")
	}
	return n, nil
}

// cosmosEncodeContinuation encodes a result offset into an opaque base64 token
// for the x-ms-continuation response header.
func cosmosEncodeContinuation(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// cosmosStoredDocKey reconstructs the partition-scoped store key for an
// already-stored document (used by collection/database deletion, which iterates
// the store and must address each doc by its exact key).
func cosmosStoredDocKey(d CosmosDocument) string {
	pk := cosmosDocPKComponent(d.Account, d.DB, d.Coll, d)
	return cosmosDocKeyPK(d.Account, d.DB, d.Coll, pk, d.ID)
}

// cosmosDocPKComponent returns the canonical partition-key component a stored
// document belongs to, for scoping a single-partition query. It mirrors the
// write-time derivation: read the value at the container's PK path.
func cosmosDocPKComponent(account, db, coll string, d CosmosDocument) string {
	pkPath, declared := cosmosContainerPKPath(account, db, coll)
	if !declared {
		return ""
	}
	if v, ok := cosmosPKValueFromBody(d.Body, pkPath); ok {
		return cosmosCanonPKValue(v)
	}
	return cosmosCanonPKValue(nil)
}
