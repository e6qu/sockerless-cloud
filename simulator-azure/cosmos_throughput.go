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
// Throughput offers: azcosmos's container.ReadThroughput()/ReplaceThroughput()
// (and the database equivalents) drive the `offers` resource. The SDK first
// reads the container/database `_rid`, then queries POST /offers for
// `SELECT * FROM c WHERE c.offerResourceId = '<rid>'`, GETs the matched offer by
// its self-link, and (for replace) PUTs the updated ThroughputProperties back.
// These handlers implement that real wire flow so a ThroughputProperties
// (manual RU/s and autoscale max RU/s) round-trips through the official SDK.

// cosmosOffer is a stored throughput offer for a database or container,
// addressed by the owning resource's `_rid` (offerResourceId). It is the
// data-plane analog of the ARM throughputSettings resource.
type cosmosOffer struct {
	Account         string         `json:"-"`
	OfferID         string         `json:"id"`
	RID             string         `json:"_rid"`
	OfferResourceID string         `json:"offerResourceId"`
	ResourceSelf    string         `json:"resource"`
	Self            string         `json:"_self"`
	ETag            string         `json:"_etag"`
	TS              int64          `json:"_ts"`
	OfferType       string         `json:"offerType"`
	OfferVersion    string         `json:"offerVersion"`
	Content         map[string]any `json:"content"`
}

var cosmosOffers sim.Store[cosmosOffer]

func registerCosmosThroughput(srv *sim.Server) {
	cosmosOffers = sim.MakeStore[cosmosOffer](srv.DB(), "cosmos_offers")
	for _, o := range cosmosOffers.List() {
		cosmosRaiseETagFloor(cosmosETagSeqOf(o.ETag))
	}

	// The azcosmos offer machinery POSTs a query to /offers, then GETs/PUTs the
	// matched offer by its rid-based self-link (offers/{offer}). These are reached
	// opaquely by container.ReadThroughput()/ReplaceThroughput(), never by a
	// literal client path, so the differential + SDK throughput tests cover them.
	// The SDK addresses an offer by its rid-based self-link, which carries a
	// trailing slash ("offers/<id>/"); register both forms so the GET/PUT match.
	srv.HandleFunc("POST /offers", handleCosmosOffersQuery)
	srv.HandleFunc("GET /offers/{offer}", handleCosmosGetOffer)
	srv.HandleFunc("GET /offers/{offer}/", handleCosmosGetOffer)
	srv.HandleFunc("PUT /offers/{offer}", handleCosmosReplaceOffer)
	srv.HandleFunc("PUT /offers/{offer}/", handleCosmosReplaceOffer)
}

// cosmosOfferFor returns the dedicated throughput offer for a resource RID, if
// one was provisioned. Real Cosmos only has an offer for a database or container
// that was created WITH dedicated throughput (manual RU/s or autoscale); a
// resource that shares throughput (created without a throughput option) has no
// offer, and ReadThroughput then returns 404 — so the sim must NOT fabricate a
// default offer here.
func cosmosOfferFor(account, rid string) (cosmosOffer, bool) {
	return cosmosOffers.Get(cosmosOfferKey(account, rid))
}

// cosmosOfferKey keys an offer by the owning resource's `_rid` alone. A `_rid`
// is globally unique (it embeds account+database+container), so the offer is
// addressable without the account — which matters because the SDK GETs/PUTs an
// offer at the account-less `/offers/{id}` path, where the account can't be
// recovered from the request when more than one Cosmos account exists.
func cosmosOfferKey(_ /*account*/, rid string) string { return rid }

// cosmosProvisionOfferFromHeaders creates the dedicated throughput offer for a
// just-created database or container whose create request carried a throughput
// header — manual (x-ms-offer-throughput) or autoscale
// (x-ms-cosmos-offer-autopilot-settings). No header means shared throughput and
// no offer (matching real Cosmos). rid is the resource's `_rid`.
func cosmosProvisionOfferFromHeaders(r *http.Request, account, rid string) {
	manual := strings.TrimSpace(r.Header.Get("x-ms-offer-throughput"))
	autopilot := strings.TrimSpace(r.Header.Get("x-ms-cosmos-offer-autopilot-settings"))
	if manual == "" && autopilot == "" {
		return
	}
	var content map[string]any
	switch {
	case manual != "":
		ru, err := strconv.ParseFloat(manual, 64)
		if err != nil {
			return
		}
		content = map[string]any{"offerThroughput": ru}
	default:
		var settings map[string]any
		if err := json.Unmarshal([]byte(autopilot), &settings); err != nil {
			return
		}
		content = map[string]any{"offerAutopilotSettings": settings}
	}
	now := time.Now().UTC().Unix()
	o := cosmosOffer{
		Account:         account,
		OfferID:         "offer_" + rid,
		RID:             "offer_" + rid,
		OfferResourceID: rid,
		ResourceSelf:    rid,
		Self:            "offers/offer_" + rid + "/",
		ETag:            fmt.Sprintf(`"%x-%x"`, now, cosmosETagSeq.Add(1)),
		TS:              now,
		OfferType:       "Invalid",
		OfferVersion:    "V2",
		Content:         content,
	}
	cosmosOffers.Put(cosmosOfferKey(account, rid), o)
}

// handleCosmosOffersQuery serves the SDK's `SELECT * FROM c WHERE
// c.offerResourceId = '<rid>'` feed query against /offers, returning the matched
// offer in the `Offers` envelope azcosmos unmarshals.
func handleCosmosOffersQuery(w http.ResponseWriter, r *http.Request) {
	account := cosmosDataAccount(r)
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cosmosDataError(w, "BadRequest", "invalid offers query body", http.StatusBadRequest)
		return
	}
	rid := cosmosOfferRIDFromQuery(req.Query)
	var matches []map[string]any
	if rid != "" {
		if o, ok := cosmosOfferFor(account, rid); ok {
			matches = append(matches, cosmosOfferBody(o))
		}
	}
	if matches == nil {
		matches = []map[string]any{}
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

func handleCosmosGetOffer(w http.ResponseWriter, r *http.Request) {
	account := cosmosDataAccount(r)
	offerID := sim.PathParam(r, "offer")
	o, ok := cosmosOfferByID(account, offerID)
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	cosmosWriteDataCharge(w, http.StatusOK, cosmosOfferBody(o), 1.0)
}

func handleCosmosReplaceOffer(w http.ResponseWriter, r *http.Request) {
	account := cosmosDataAccount(r)
	offerID := sim.PathParam(r, "offer")
	o, ok := cosmosOfferByID(account, offerID)
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	var req struct {
		Content      map[string]any `json:"content"`
		OfferType    string         `json:"offerType"`
		OfferVersion string         `json:"offerVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cosmosDataError(w, "BadRequest", "invalid offer body", http.StatusBadRequest)
		return
	}
	if !cosmosIfMatchOK(r, o.ETag) {
		cosmosDataError(w, "PreconditionFailed",
			"Operation cannot be performed because one of the specified precondition is not met.",
			http.StatusPreconditionFailed)
		return
	}
	if req.Content == nil || (req.Content["offerThroughput"] == nil && req.Content["offerAutopilotSettings"] == nil) {
		cosmosDataError(w, "BadRequest", "offer content must specify offerThroughput or offerAutopilotSettings", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Unix()
	o.Content = req.Content
	if req.OfferVersion != "" {
		o.OfferVersion = req.OfferVersion
	}
	o.ETag = fmt.Sprintf(`"%x-%x"`, now, cosmosETagSeq.Add(1))
	o.TS = now
	cosmosOffers.Put(cosmosOfferKey(account, o.OfferResourceID), o)
	cosmosWriteDataCharge(w, http.StatusOK, cosmosOfferBody(o), 1.0)
}

// cosmosOfferByID finds an offer for an account by its offer id (the rid-based
// id the SDK GETs/PUTs after the feed query).
func cosmosOfferByID(account, offerID string) (cosmosOffer, bool) {
	// Match by the globally-unique offer id / rid alone; the account isn't
	// recoverable from the account-less /offers/{id} path (see cosmosOfferKey).
	for _, o := range cosmosOffers.List() {
		if o.OfferID == offerID || o.RID == offerID {
			return o, true
		}
	}
	return cosmosOffer{}, false
}

func cosmosOfferBody(o cosmosOffer) map[string]any {
	return map[string]any{
		"id":              o.OfferID,
		"_rid":            o.RID,
		"_self":           o.Self,
		"_etag":           o.ETag,
		"_ts":             o.TS,
		"resource":        o.ResourceSelf,
		"offerResourceId": o.OfferResourceID,
		"offerType":       o.OfferType,
		"offerVersion":    o.OfferVersion,
		"content":         o.Content,
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
