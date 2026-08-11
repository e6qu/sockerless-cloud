package azure_sdk_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// Differential testing of the Cosmos server-side-programming and change-feed
// slices against Microsoft's own Cosmos DB emulator. The azcosmos SDK has no
// client methods for sprocs / change feed, so — like the SDK tests — these drive
// the REST data plane directly, but here the SAME raw-REST code runs against the
// sim AND the emulator (differing only in the endpoint), and the observable
// outcome must match. The emulator validates the master-key signature, so these
// requests are signed with the real Cosmos shared-key algorithm (the sim ignores
// the signature, so the identical client code reaches both).
//
// As elsewhere, the emulator is a REFERENCE, not a ceiling: a case where the sim
// is intentionally more faithful is recorded in cosmosScriptDiffKnownDivergences
// and asserted exactly — the sim is never regressed to match the oracle.

// The vNext Cosmos emulator does NOT support server-side script execution — it
// returns `400 BadRequest "Server-side script execution is not supported in this
// emulator"` for any sproc execution, regardless of whether the sproc exists.
// The sim implements a faithful execution subset (createDocument / count / bulk
// delete) and the real-Cosmos error shapes (404 for a missing sproc), so it is
// intentionally MORE faithful than the emulator on these. These cases are
// recorded as documented divergences and asserted exactly — the sim is never
// regressed to the emulator's "unsupported" behaviour. Sproc CRUD (which the
// emulator DOES support) agrees and is asserted directly.
var cosmosScriptDiffKnownDivergences = map[string]cosmosDiffDivergence{
	"sproc-create-and-execute": {
		sim:    cosmosDiffResult{value: `[["pk","p"],["readStatus",200],["v","7"]]`},
		oracle: cosmosDiffResult{value: `[["execStatus",400]]`},
		reason: "the vNext emulator does not support server-side script execution; the sim really executes the createDocument sproc",
	},
	"sproc-missing-404": {
		sim:    cosmosDiffResult{value: `[["status",404]]`},
		oracle: cosmosDiffResult{value: `[["status",400]]`},
		reason: "real Cosmos returns 404 for a missing sproc; the emulator returns 400 because it rejects all script execution before resolving the sproc",
	},
}

func TestCosmosScripts_DifferentialVsEmulator(t *testing.T) {
	emuEndpoint, stop := startCosmosEmulator(t)
	defer stop()

	for _, sc := range cosmosScriptDiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			simRes := cosmosCapture(sc.run(t, baseURL+"/", "sim-"+sc.name))
			oracleRes := cosmosCapture(sc.run(t, emuEndpoint, "emu-"+sc.name))
			if div, ok := cosmosScriptDiffKnownDivergences[sc.name]; ok {
				cosmosAssertEqual(t, "sim (documented divergence: "+div.reason+")", div.sim, simRes)
				cosmosAssertEqual(t, "emulator (documented divergence: "+div.reason+")", div.oracle, oracleRes)
				return
			}
			if simRes != oracleRes {
				t.Errorf("differential mismatch for %q:\n  sim:      %+v\n  emulator: %+v\n"+
					"If the sim is the MORE faithful one, add a cosmosScriptDiffKnownDivergences entry (do not regress the sim).",
					sc.name, simRes, oracleRes)
			}
		})
	}
}

type cosmosScriptDiffScenario struct {
	name string
	run  func(t *testing.T, endpoint, dbID string) (map[string]any, error)
}

// ── Cosmos shared-key signing (the real algorithm; the emulator validates it) ──

// cosmosSign builds the Authorization header for a Cosmos REST request per
// https://learn.microsoft.com/rest/api/cosmos-db/access-control-on-cosmosdb-resources
// resourceType is the child resource type ("sprocs"/"docs"/"colls"/"dbs"),
// resourceLink is the resource (point op) or parent (feed/create) link with no
// leading slash, xmsDate is the RFC1123-GMT date echoed in the x-ms-date header.
func cosmosSign(method, resourceType, resourceLink, xmsDate string) string {
	key, _ := base64.StdEncoding.DecodeString(cosmosEmulatorKey)
	stringToSign := strings.ToLower(method) + "\n" +
		strings.ToLower(resourceType) + "\n" +
		resourceLink + "\n" +
		strings.ToLower(xmsDate) + "\n" + "" + "\n"
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return urlQueryEscape("type=master&ver=1.0&sig=" + sig)
}

// urlQueryEscape escapes the auth token the way the SDK does (net/url
// QueryEscape), without importing net/url at call sites.
func urlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// cosmosRESTReq issues a signed Cosmos REST request against endpoint and returns
// the decoded body (for a JSON object response), the raw body, and the status.
func cosmosRESTReq(t *testing.T, endpoint, method, path, resourceType, resourceLink, body string, extra map[string]string) (map[string]any, int) {
	t.Helper()
	xmsDate := time.Now().UTC().Format(http.TimeFormat)

	var resp *http.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		var br io.Reader
		if body != "" {
			br = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, strings.TrimRight(endpoint, "/")+path, br)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("x-ms-date", xmsDate)
		req.Header.Set("x-ms-version", "2018-12-31")
		req.Header.Set("Authorization", cosmosSign(method, resourceType, resourceLink, xmsDate))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}
		if !cosmosTransientErr(err) {
			t.Fatalf("do request: %v", err)
		}
		t.Logf("cosmosRESTReq transient error (attempt %d): %v", attempt+1, err)
	}
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, resp.StatusCode
}

// cosmosTransientErr reports whether err is a transient transport error against
// the Cosmos emulator that is worth retrying. It intentionally does NOT match
// HTTP-level errors (4xx/5xx responses are returned, not errors) or permanent
// failures like connection refused.
func cosmosTransientErr(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Temporary() || ne.Timeout()
	}
	return false
}

// cosmosDiffMakeContainer creates a database + a /pk-partitioned container over
// REST against endpoint (idempotent across the two endpoints' runs).
func cosmosDiffMakeContainer(t *testing.T, endpoint, dbID, coll string) {
	t.Helper()
	cosmosRESTReq(t, endpoint, "POST", "/dbs", "dbs", "",
		fmt.Sprintf(`{"id":%q}`, dbID), nil)
	cosmosRESTReq(t, endpoint, "POST", "/dbs/"+dbID+"/colls", "colls", "dbs/"+dbID,
		fmt.Sprintf(`{"id":%q,"partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, coll), nil)
}

func cosmosScriptDiffScenarios() []cosmosScriptDiffScenario {
	return []cosmosScriptDiffScenario{
		{"sproc-crud", func(t *testing.T, endpoint, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			base := "/" + collLink

			sproc := `{"id":"sp","body":"function(){ var c = getContext().getCollection(); }"}`
			created, cst := cosmosRESTReq(t, endpoint, "POST", base+"/sprocs", "sprocs", collLink, sproc, nil)
			// Get it back.
			_, gst := cosmosRESTReq(t, endpoint, "GET", base+"/sprocs/sp", "sprocs", collLink+"/sprocs/sp", "", nil)
			// Delete it; a subsequent get is 404 on both.
			_, dst := cosmosRESTReq(t, endpoint, "DELETE", base+"/sprocs/sp", "sprocs", collLink+"/sprocs/sp", "", nil)
			_, g2 := cosmosRESTReq(t, endpoint, "GET", base+"/sprocs/sp", "sprocs", collLink+"/sprocs/sp", "", nil)
			return map[string]any{
				"createStatus":   cst,
				"id":             created["id"],
				"getStatus":      gst,
				"deleteStatus":   dst,
				"getAfterDelete": g2,
			}, nil
		}},
		{"sproc-create-and-execute", func(t *testing.T, endpoint, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			base := "/" + collLink

			// Create a createDocument sproc.
			sproc := `{"id":"createItem","body":"function(doc){ var c = getContext().getCollection(); c.createDocument(c.getSelfLink(), doc, function(err, created){ getContext().getResponse().setBody(created); }); }"}`
			if _, st := cosmosRESTReq(t, endpoint, "POST", base+"/sprocs", "sprocs", collLink, sproc, nil); st != http.StatusCreated {
				return map[string]any{"createStatus": st}, nil
			}

			// Execute it; the document must really be created.
			pk := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}
			_, st := cosmosRESTReq(t, endpoint, "POST", base+"/sprocs/createItem", "sprocs",
				collLink+"/sprocs/createItem", `[{"id":"made","pk":"p","v":7}]`, pk)
			if st != http.StatusOK {
				return map[string]any{"execStatus": st}, nil
			}

			// Read the created doc back and report its user fields.
			got, rst := cosmosRESTReq(t, endpoint, "GET", base+"/docs/made", "docs",
				collLink+"/docs/made", "", pk)
			return map[string]any{
				"readStatus": rst,
				"v":          got["v"],
				"pk":         got["pk"],
			}, nil
		}},
		{"sproc-missing-404", func(t *testing.T, endpoint, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			_, st := cosmosRESTReq(t, endpoint, "POST", "/"+collLink+"/sprocs/nope", "sprocs",
				collLink+"/sprocs/nope", `[]`,
				map[string]string{"x-ms-documentdb-partitionkey": `["p"]`})
			return map[string]any{"status": st}, nil
		}},
		{"changefeed-incremental", func(t *testing.T, endpoint, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			base := "/" + collLink
			pk := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}

			for _, id := range []string{"a", "b"} {
				cosmosRESTReq(t, endpoint, "POST", base+"/docs", "docs", collLink,
					fmt.Sprintf(`{"id":%q,"pk":"p"}`, id), pk)
			}

			// Fresh change feed: every doc.
			feed := map[string]string{"A-IM": "Incremental feed"}
			out, _, cont := cosmosChangeFeedRead(t, endpoint, base, collLink, feed, "")
			firstIDs := changeFeedIDs(out)

			// A new document → only it comes back from the continuation.
			cosmosRESTReq(t, endpoint, "POST", base+"/docs", "docs", collLink,
				`{"id":"c","pk":"p"}`, pk)
			out2, _, _ := cosmosChangeFeedRead(t, endpoint, base, collLink, feed, cont)
			incrIDs := changeFeedIDs(out2)

			return map[string]any{"firstReadIDs": firstIDs, "incrementalIDs": incrIDs}, nil
		}},
		{"conflictfeed-empty", func(t *testing.T, endpoint, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			out, st := cosmosRESTReq(t, endpoint, "GET", "/"+collLink+"/conflicts", "conflicts", collLink, "", nil)
			conflicts, _ := out["Conflicts"].([]any)
			return map[string]any{"status": st, "count": len(conflicts)}, nil
		}},
	}
}

// cosmosChangeFeedRead issues a signed change-feed read (optionally with a
// continuation in If-None-Match) and returns the decoded body, the status, and
// the response etag (the advanced continuation). A 304 yields a nil body.
func cosmosChangeFeedRead(t *testing.T, endpoint, base, collLink string, feed map[string]string, continuation string) (map[string]any, int, string) {
	t.Helper()
	xmsDate := time.Now().UTC().Format(http.TimeFormat)
	req, err := http.NewRequest("GET", strings.TrimRight(endpoint, "/")+base+"/docs", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-ms-date", xmsDate)
	req.Header.Set("x-ms-version", "2018-12-31")
	req.Header.Set("Authorization", cosmosSign("GET", "docs", collLink, xmsDate))
	for k, v := range feed {
		req.Header.Set(k, v)
	}
	if continuation != "" {
		req.Header.Set("If-None-Match", continuation)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	etag := resp.Header.Get("etag")
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, resp.StatusCode, etag
}

// changeFeedIDs extracts the document ids from a change-feed Documents response,
// sorted for a stable comparison.
func changeFeedIDs(out map[string]any) []any {
	docs, _ := out["Documents"].([]any)
	var ids []any
	for _, d := range docs {
		if m, ok := d.(map[string]any); ok {
			ids = append(ids, m["id"])
		}
	}
	sort.Slice(ids, func(i, j int) bool { return fmt.Sprint(ids[i]) < fmt.Sprint(ids[j]) })
	return ids
}
