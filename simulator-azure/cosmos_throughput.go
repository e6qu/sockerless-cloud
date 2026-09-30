package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cosmos request-unit (RU) charge model + throughput-offer data plane.
//
// Real Cosmos returns an `x-ms-request-charge` header on every data-plane
// response: a point read of a ~1 KB item costs ~1 RU, a create/upsert/replace
// of the same item costs ~5 RU, a delete ~5 RU, and a query scales with the
// number of results it materializes. The cost grows with the serialized item
// size. The model below is a small, defensible heuristic anchored to Microsoft's
// documented per-operation RU figures (point read 1 RU/KB, write 5 RU/KB) and
// verified against the emulator's actual x-ms-request-charge in the differential
// (cosmos_differential_test.go, the "request-charge-*" scenarios): the emulator
// charges 1 RU for a small-item point read and ~5–6 RU for a small-item create,
// which the sim matches to the same order of magnitude.
//
// A database's or container's dedicated throughput is one record: the Azure
// Resource Manager throughputSettings/default resource in cosmosThroughputs.
// The data plane's `offers` resource is that record seen through the NoSQL
// API: azcosmos's ReadThroughput and ReplaceThroughput query POST /offers for
// `SELECT * FROM c WHERE c.offerResourceId = '<rid>'`, GET the matched offer by
// its self-link and PUT the updated ThroughputProperties back, and each of
// those reads or writes the record Azure Resource Manager serves.

func registerCosmosThroughput(srv *sim.Server) {
	for _, t := range cosmosThroughputs.List() {
		if res, ok := t.Properties["resource"].(map[string]any); ok {
			etag, _ := res["_etag"].(string)
			cosmosRaiseETagFloor(cosmosETagSeqOf(etag))
		}
	}

	// The SDK addresses an offer by its rid-based self-link, which carries a
	// trailing slash ("offers/<id>/"); register both forms so the GET/PUT match.
	srv.HandleFunc("POST /offers", handleCosmosOffersQuery)
	srv.HandleFunc("GET /offers/{offer}", handleCosmosGetOffer)
	srv.HandleFunc("GET /offers/{offer}/", handleCosmosGetOffer)
	srv.HandleFunc("PUT /offers/{offer}", handleCosmosReplaceOffer)
	srv.HandleFunc("PUT /offers/{offer}/", handleCosmosReplaceOffer)
}

// cosmosDataRID is the `_rid` of a NoSQL database, or a container when coll is
// set.
func cosmosDataRID(account, db, coll string) string {
	if coll == "" {
		return cosmosDatabaseRID(account, db)
	}
	return cosmosCollectionRID(account, db, coll)
}

const cosmosOfferIDPrefix = "offer_"

// cosmosSQLThroughputID is the Azure Resource Manager identifier of the
// throughput record of the NoSQL database, or container when coll is set, that
// the data plane addresses by name.
func cosmosSQLThroughputID(account, db, coll string) (string, bool) {
	acct, ok := cosmosAccountByName(account)
	if !ok {
		return "", false
	}
	id := acct.ID + "/sqlDatabases/" + db
	if coll != "" {
		id += "/containers/" + coll
	}
	return id + "/throughputSettings/default", true
}

var cosmosThroughputsByRID sim.GenerationIndex[CosmosThroughput]

// cosmosThroughputByRID finds the throughput record of the NoSQL resource
// whose data-plane `_rid` is rid.
func cosmosThroughputByRID(rid string) (CosmosThroughput, bool) {
	return cosmosThroughputsByRID.Lookup(cosmosThroughputs, rid, func(t CosmosThroughput) []string {
		if !strings.Contains(t.ID, "/sqlDatabases/") {
			return nil
		}
		account, db, coll, ok := cosmosARMIDNames(t.ID)
		if !ok || db == "" {
			return nil
		}
		return []string{cosmosDataRID(account, db, coll)}
	})
}

// cosmosStampThroughput records a write to a throughput resource: the `_etag`
// and `_ts` both surfaces report, and for a NoSQL resource the `_rid` of the
// resource the throughput belongs to.
func cosmosStampThroughput(id string, resource map[string]any) {
	now := time.Now().UTC().Unix()
	resource["_etag"] = fmt.Sprintf(`"%x-%x"`, now, cosmosETagSeq.Add(1))
	resource["_ts"] = now
	if strings.Contains(id, "/sqlDatabases/") {
		if account, db, coll, ok := cosmosARMIDNames(id); ok && db != "" {
			resource["_rid"] = cosmosDataRID(account, db, coll)
		}
	}
}

// cosmosProvisionThroughputFromHeaders records the dedicated throughput of a
// NoSQL database or container the data plane just created with a throughput
// header: manual (x-ms-offer-throughput) or autoscale
// (x-ms-cosmos-offer-autopilot-settings). No header means shared throughput and
// no record, as on the service.
func cosmosProvisionThroughputFromHeaders(r *http.Request, account, db, coll string) error {
	manual := strings.TrimSpace(r.Header.Get("x-ms-offer-throughput"))
	autopilot := strings.TrimSpace(r.Header.Get("x-ms-cosmos-offer-autopilot-settings"))
	if manual == "" && autopilot == "" {
		return nil
	}
	resource := map[string]any{}
	if manual != "" {
		ru, err := strconv.ParseFloat(manual, 64)
		if err != nil {
			return fmt.Errorf("x-ms-offer-throughput %q is not a number", manual)
		}
		resource["throughput"] = ru
	} else {
		var settings map[string]any
		if err := json.Unmarshal([]byte(autopilot), &settings); err != nil {
			return fmt.Errorf("x-ms-cosmos-offer-autopilot-settings is not JSON: %w", err)
		}
		resource["autoscaleSettings"] = settings
	}
	id, ok := cosmosSQLThroughputID(account, db, coll)
	if !ok {
		return fmt.Errorf("account %s does not exist", account)
	}
	typ := cosmosTypeBase + "/sqlDatabases/throughputSettings"
	if coll != "" {
		typ = cosmosTypeBase + "/sqlDatabases/containers/throughputSettings"
	}
	cosmosStampThroughput(id, resource)
	cosmosThroughputs.Put(id, CosmosThroughput{
		ID:         id,
		Name:       "default",
		Type:       typ,
		Properties: map[string]any{"resource": resource},
	})
	return nil
}

// cosmosForgetSQLThroughput drops the throughput record of a NoSQL database
// or container the data plane deleted.
func cosmosForgetSQLThroughput(account, db, coll string) {
	if id, ok := cosmosSQLThroughputID(account, db, coll); ok {
		cosmosThroughputs.Delete(id)
		cosmosRUBuckets.Forget(id)
	}
}

// handleCosmosOffersQuery serves the SDK's `SELECT * FROM c WHERE
// c.offerResourceId = '<rid>'` feed query against /offers, returning the matched
// offer in the `Offers` envelope azcosmos unmarshals.
func handleCosmosOffersQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cosmosDataError(w, "BadRequest", "invalid offers query body", http.StatusBadRequest)
		return
	}
	matches := []map[string]any{}
	if rid := cosmosOfferRIDFromQuery(req.Query); rid != "" {
		if t, ok := cosmosThroughputByRID(rid); ok {
			matches = append(matches, cosmosOfferBody(t))
		}
	}
	cosmosWriteDataCharge(w, http.StatusOK, map[string]any{
		"Offers": matches,
		"_rid":   "",
		"_count": len(matches),
	}, cosmosQueryCharge(len(matches)))
}

// cosmosOfferRIDFromQuery extracts the rid from the SDK's offer query
// `... WHERE c.offerResourceId = '<rid>'`.
func cosmosOfferRIDFromQuery(query string) string {
	const marker = "offerResourceId"
	i := strings.Index(query, marker)
	if i < 0 {
		return ""
	}
	rest := query[i+len(marker):]
	first := strings.IndexByte(rest, '\'')
	if first < 0 {
		return ""
	}
	rest = rest[first+1:]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// cosmosThroughputByOfferID finds the throughput record an offer id names.
func cosmosThroughputByOfferID(offerID string) (CosmosThroughput, bool) {
	rid, ok := strings.CutPrefix(offerID, cosmosOfferIDPrefix)
	if !ok {
		return CosmosThroughput{}, false
	}
	return cosmosThroughputByRID(rid)
}

func handleCosmosGetOffer(w http.ResponseWriter, r *http.Request) {
	t, ok := cosmosThroughputByOfferID(sim.PathParam(r, "offer"))
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	cosmosWriteDataCharge(w, http.StatusOK, cosmosOfferBody(t), 1.0)
}

func handleCosmosReplaceOffer(w http.ResponseWriter, r *http.Request) {
	t, ok := cosmosThroughputByOfferID(sim.PathParam(r, "offer"))
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	var req struct {
		Content map[string]any `json:"content"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cosmosDataError(w, "BadRequest", "invalid offer body", http.StatusBadRequest)
		return
	}
	resource, _ := t.Properties["resource"].(map[string]any)
	if resource == nil {
		resource = map[string]any{}
	}
	etag, _ := resource["_etag"].(string)
	if !cosmosIfMatch(r, etag) {
		cosmosPreconditionFailed(w)
		return
	}
	switch {
	case req.Content["offerThroughput"] != nil:
		resource["throughput"] = req.Content["offerThroughput"]
		delete(resource, "autoscaleSettings")
	case req.Content["offerAutopilotSettings"] != nil:
		resource["autoscaleSettings"] = req.Content["offerAutopilotSettings"]
		delete(resource, "throughput")
	default:
		cosmosDataError(w, "BadRequest", "offer content must specify offerThroughput or offerAutopilotSettings", http.StatusBadRequest)
		return
	}
	cosmosStampThroughput(t.ID, resource)
	if t.Properties == nil {
		t.Properties = map[string]any{}
	}
	t.Properties["resource"] = resource
	cosmosThroughputs.Put(t.ID, t)
	cosmosWriteDataCharge(w, http.StatusOK, cosmosOfferBody(t), 1.0)
}

// cosmosOfferBody renders a NoSQL resource's throughput record as the offer
// the data plane serves.
func cosmosOfferBody(t CosmosThroughput) map[string]any {
	resource, _ := t.Properties["resource"].(map[string]any)
	rid, _ := resource["_rid"].(string)
	content := map[string]any{}
	if v := resource["throughput"]; v != nil {
		content["offerThroughput"] = v
	}
	if v := resource["autoscaleSettings"]; v != nil {
		content["offerAutopilotSettings"] = v
	}
	offerID := cosmosOfferIDPrefix + rid
	return map[string]any{
		"id":              offerID,
		"_rid":            offerID,
		"_self":           "offers/" + offerID + "/",
		"_etag":           resource["_etag"],
		"_ts":             resource["_ts"],
		"resource":        rid,
		"offerResourceId": rid,
		"offerType":       "Invalid",
		"offerVersion":    "V2",
		"content":         content,
	}
}

// ── RU charge model ──────────────────────────────────────────────────────────

// cosmosItemSizeKB returns the serialized item size in KB (minimum one), the
// scale factor real Cosmos's RU figures are quoted against.
func cosmosItemSizeKB(body map[string]any) float64 {
	if body == nil {
		return 1
	}
	b, err := json.Marshal(body)
	if err != nil {
		return 1
	}
	kb := float64(len(b)) / 1024.0
	if kb < 1 {
		return 1
	}
	return kb
}

// cosmosReadCharge is the RU cost of a point read: ~1 RU per KB (Microsoft's
// documented figure for a point read by id+partition key).
func cosmosReadCharge(body map[string]any) float64 {
	return cosmosRound(1.0 * cosmosItemSizeKB(body))
}

// cosmosWriteCharge is the RU cost of a create/upsert/replace/patch: writes cost
// ~5x a read of the same item, matching the emulator's ~5–6 RU for a small item.
func cosmosWriteCharge(body map[string]any) float64 {
	return cosmosRound(5.0 * cosmosItemSizeKB(body))
}

// cosmosDeleteCharge is the RU cost of a point delete (~5 RU, write-class).
func cosmosDeleteCharge() float64 { return 5.0 }

// cosmosQueryCharge scales with the number of rows the query materialized: a
// small base plus a per-result increment. An empty result still costs the base.
func cosmosQueryCharge(results int) float64 {
	return cosmosRound(2.3 + 0.5*float64(results))
}

// cosmosMetadataCharge is the flat ~1 RU cost of a small metadata read
// (database/collection/offer GET, list).
const cosmosMetadataCharge = 1.0

// cosmosRound rounds an RU charge to the two-decimal precision real Cosmos
// reports in x-ms-request-charge.
func cosmosRound(v float64) float64 {
	return math.Round(v*100) / 100
}

// cosmosFormatCharge renders an RU charge the way real Cosmos does (a decimal
// with no trailing-zero noise), e.g. "1", "5.71", "2.8".
func cosmosFormatCharge(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
