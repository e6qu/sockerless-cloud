package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Azure Cosmos DB identifies an item by (partition-key value, id). azcosmos
// sends the partition key in `x-ms-documentdb-partitionkey` as a JSON array
// (["p"], [30], [true], [null]); the container declares its path (/pk) at
// create. The differential test holds these helpers to the emulator.

// CosmosDataDB exists before anything is stored in it.
type CosmosDataDB struct {
	Account string
	DB      string
}

func cosmosDataDBKey(account, db string) string {
	return account + "/" + db
}

// azcosmos and the REST API create containers through the data plane (POST
// /dbs/{db}/colls), not Azure Resource Manager, so their settings live here.
type CosmosDataColl struct {
	Account string
	DB      string
	Coll    string
	PKPath  string
	// DefaultTTL is in seconds, -1 for items that expire only by their own ttl, and
	// nil when time to live is off.
	DefaultTTL *int64 `json:",omitempty"`
}

func cosmosDataCollKey(account, db, coll string) string {
	return account + "/" + db + "/" + coll
}

// cosmosContainerPKPath consults the data-plane collection store, then the ARM
// container store. ok is false for a collection declared nowhere, which some
// raw-HTTP clients POST into; callers then key by id alone.
func cosmosContainerPKPath(account, db, coll string) (string, bool) {
	if dc, ok := cosmosDataColls.Get(cosmosDataCollKey(account, db, coll)); ok && dc.PKPath != "" {
		return dc.PKPath, true
	}
	if c, ok := cosmosARMContainer(account, db, coll); ok {
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
			// The ARM handler may have stored paths as a []string.
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

// A nested path ("/a/b") traverses sub-objects.
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

// The header is a JSON array of one component; err reports a malformed one.
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
		// An empty array is the SDK's "no partition key" sentinel (NewPartitionKey()).
		return nil, false, nil
	}
	return arr[0], true, nil
}

// Numbers decode differently from a header and a body, so canonicalize on the
// JSON encoding.
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

func cosmosValuesEqualPK(a, b any) bool {
	return cosmosCanonPKValue(a) == cosmosCanonPKValue(b)
}

// Without a declared path pkValue is "" and the key is the id-only form.
func cosmosDocKeyPK(account, db, container, pkValue, doc string) string {
	if pkValue == "" {
		return cosmosDocKey(account, db, container, doc)
	}
	return account + "/" + db + "/" + container + "/" + pkValue + "/" + doc
}

// cosmosResolvePKForWrite enforces Azure Cosmos DB's rule that a supplied header
// must match the value at the container's path inside the document. It returns
// "" for a container with no declared path.
func cosmosResolvePKForWrite(r *http.Request, account, db, coll string, body map[string]any) (pkKeyComponent string, herr *cosmosWriteError) {
	pkPath, declared := cosmosContainerPKPath(account, db, coll)
	headerVal, headerOK, perr := cosmosPKFromHeader(r)
	if perr != nil {
		return "", &cosmosWriteError{code: "BadRequest", msg: perr.Error(), status: http.StatusBadRequest}
	}
	if !declared {
		return "", nil
	}
	bodyVal, bodyOK := cosmosPKValueFromBody(body, pkPath)
	if headerOK {
		// Azure Cosmos DB rejects a header that does not match the document.
		if !bodyOK || !cosmosValuesEqualPK(headerVal, bodyVal) {
			return "", &cosmosWriteError{
				code:   "BadRequest",
				msg:    "PartitionKey extracted from document doesn't match the one specified in the header",
				status: http.StatusBadRequest,
			}
		}
		return cosmosCanonPKValue(headerVal), nil
	}
	if bodyOK {
		return cosmosCanonPKValue(bodyVal), nil
	}
	// Azure Cosmos DB treats a document with no value at the path as the
	// "undefined" partition.
	return cosmosCanonPKValue(nil), nil
}

// cosmosResolvePKForPoint: with the header the lookup is exact; without it the
// caller scans by id through cosmosFindDocByID.
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

// The first match in sorted order wins.
func cosmosFindDocByID(account, db, coll, id string) (CosmosDocument, bool) {
	for _, d := range cosmosDocsFor(account, db, coll) {
		if d.ID == id {
			return d, true
		}
	}
	return CosmosDocument{}, false
}

type cosmosWriteError struct {
	code   string
	msg    string
	status int
}

// -1 means no cap: the header absent, or the SDK's -1 "unbounded" sentinel.
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

// Deletion iterates the store and addresses each document by its exact key.
func cosmosStoredDocKey(d CosmosDocument) string {
	pk := cosmosDocPKComponent(d.Account, d.DB, d.Coll, d)
	return cosmosDocKeyPK(d.Account, d.DB, d.Coll, pk, d.ID)
}

// The same derivation as at write time: the value at the container's path.
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
