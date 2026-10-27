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

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
)

// Differential tests of server-side programming and the change feed against
// Microsoft's Azure Cosmos DB emulator. azcosmos has no client methods for
// them, so the same raw REST code, signed with the master-key algorithm the
// emulator validates, runs against both, differing only in the endpoint and
// key. The emulator is a reference, not a ceiling: a case where the simulator
// is deliberately more faithful goes in cosmosScriptDiffKnownDivergences.

// The vNext emulator answers any stored procedure execution with 400 "Server-side
// script execution is not supported in this emulator", existing procedure or
// not. The simulator executes a subset (createDocument, count, bulk delete)
// with Azure's error shapes (404 for a missing procedure), so those cases are
// divergences asserted exactly. Procedure CRUD agrees and is asserted directly.
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
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	emuEndpoint, release := startCosmosEmulator(t)
	defer release()

	for _, sc := range cosmosScriptDiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			simRes := cosmosCapture(sc.run(t, cosmosSimCoordinate(t, dataPlaneAccount), uniquename.New("sim-"+sc.name)))
			oracleRes := cosmosCapture(sc.run(t, cosmosCoordinate{url: emuEndpoint, key: cosmosEmulatorKey}, uniquename.New("emu-"+sc.name)))
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
	run  func(t *testing.T, endpoint cosmosCoordinate, dbID string) (map[string]any, error)
}

// cosmosCoordinate is all that differs between the simulator and the emulator
// for these scenarios: the endpoint and the account key that signs requests.
type cosmosCoordinate struct {
	url string
	key string
}

// cosmosSimCoordinate addresses the simulator as a client does: the
// documentEndpoint Azure Resource Manager advertises, signed with the key
// listKeys serves.
func cosmosSimCoordinate(t *testing.T, account string) cosmosCoordinate {
	t.Helper()
	return cosmosCoordinate{
		url: cosmosDocumentEndpoint(t, account),
		key: cosmosAccountKey(t, account),
	}
}

// cosmosSign builds the master-key Authorization header
// (https://learn.microsoft.com/rest/api/cosmos-db/access-control-on-cosmosdb-resources).
// resourceLink is the resource for a point operation or the parent for a feed
// or create, with no leading slash; xmsDate is the RFC 1123 date sent in
// x-ms-date.
func cosmosSign(accountKey, method, resourceType, resourceLink, xmsDate string) string {
	key, _ := base64.StdEncoding.DecodeString(accountKey)
	stringToSign := strings.ToLower(method) + "\n" +
		strings.ToLower(resourceType) + "\n" +
		resourceLink + "\n" +
		strings.ToLower(xmsDate) + "\n" + "" + "\n"
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return urlQueryEscape("type=master&ver=1.0&sig=" + sig)
}

// urlQueryEscape escapes the auth token as the SDK does (net/url QueryEscape).
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

// cosmosRESTReq issues a signed request and returns the decoded JSON object
// body, the raw body, and the status.
func cosmosRESTReq(t *testing.T, endpoint cosmosCoordinate, method, path, resourceType, resourceLink, body string, extra map[string]string) (map[string]any, int) {
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
		req, reqErr := http.NewRequest(method, strings.TrimRight(endpoint.url, "/")+path, br)
		if reqErr != nil {
			t.Fatalf("new request: %v", reqErr)
		}
		req.Header.Set("x-ms-date", xmsDate)
		req.Header.Set("x-ms-version", "2018-12-31")
		req.Header.Set("Authorization", cosmosSign(endpoint.key, method, resourceType, resourceLink, xmsDate))
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

// cosmosTransientErr matches transient transport errors against the emulator,
// not HTTP-level errors or a refused connection.
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

// cosmosDiffMakeContainer creates a database and a /pk-partitioned container,
// idempotently across the two endpoints' runs.
func cosmosDiffMakeContainer(t *testing.T, endpoint cosmosCoordinate, dbID, coll string) {
	t.Helper()
	cosmosRESTReq(t, endpoint, "POST", "/dbs", "dbs", "",
		fmt.Sprintf(`{"id":%q}`, dbID), nil)
	cosmosRESTReq(t, endpoint, "POST", "/dbs/"+dbID+"/colls", "colls", "dbs/"+dbID,
		fmt.Sprintf(`{"id":%q,"partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, coll), nil)
}

func cosmosScriptDiffScenarios() []cosmosScriptDiffScenario {
	return []cosmosScriptDiffScenario{
		{"sproc-crud", func(t *testing.T, endpoint cosmosCoordinate, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			base := "/" + collLink

			sproc := `{"id":"sp","body":"function(){ var c = getContext().getCollection(); }"}`
			created, cst := cosmosRESTReq(t, endpoint, "POST", base+"/sprocs", "sprocs", collLink, sproc, nil)
			_, gst := cosmosRESTReq(t, endpoint, "GET", base+"/sprocs/sp", "sprocs", collLink+"/sprocs/sp", "", nil)
			// A get after the delete is 404 on both.
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
		{"sproc-create-and-execute", func(t *testing.T, endpoint cosmosCoordinate, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			base := "/" + collLink

			sproc := `{"id":"createItem","body":"function(doc){ var c = getContext().getCollection(); c.createDocument(c.getSelfLink(), doc, function(err, created){ getContext().getResponse().setBody(created); }); }"}`
			if _, st := cosmosRESTReq(t, endpoint, "POST", base+"/sprocs", "sprocs", collLink, sproc, nil); st != http.StatusCreated {
				return map[string]any{"createStatus": st}, nil
			}

			// Executing it creates the document.
			pk := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}
			_, st := cosmosRESTReq(t, endpoint, "POST", base+"/sprocs/createItem", "sprocs",
				collLink+"/sprocs/createItem", `[{"id":"made","pk":"p","v":7}]`, pk)
			if st != http.StatusOK {
				return map[string]any{"execStatus": st}, nil
			}

			got, rst := cosmosRESTReq(t, endpoint, "GET", base+"/docs/made", "docs",
				collLink+"/docs/made", "", pk)
			return map[string]any{
				"readStatus": rst,
				"v":          got["v"],
				"pk":         got["pk"],
			}, nil
		}},
		{"sproc-missing-404", func(t *testing.T, endpoint cosmosCoordinate, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			_, st := cosmosRESTReq(t, endpoint, "POST", "/"+collLink+"/sprocs/nope", "sprocs",
				collLink+"/sprocs/nope", `[]`,
				map[string]string{"x-ms-documentdb-partitionkey": `["p"]`})
			return map[string]any{"status": st}, nil
		}},
		{"changefeed-incremental", func(t *testing.T, endpoint cosmosCoordinate, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			base := "/" + collLink
			pk := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}

			for _, id := range []string{"a", "b"} {
				cosmosRESTReq(t, endpoint, "POST", base+"/docs", "docs", collLink,
					fmt.Sprintf(`{"id":%q,"pk":"p"}`, id), pk)
			}

			// A fresh change feed returns every document.
			feed := map[string]string{"A-IM": "Incremental feed"}
			out, _, cont := cosmosChangeFeedRead(t, endpoint, base, collLink, feed, "")
			firstIDs := changeFeedIDs(out)

			// From the continuation, only a new document comes back.
			cosmosRESTReq(t, endpoint, "POST", base+"/docs", "docs", collLink,
				`{"id":"c","pk":"p"}`, pk)
			out2, _, _ := cosmosChangeFeedRead(t, endpoint, base, collLink, feed, cont)
			incrIDs := changeFeedIDs(out2)

			return map[string]any{"firstReadIDs": firstIDs, "incrementalIDs": incrIDs}, nil
		}},
		{"conflictfeed-empty", func(t *testing.T, endpoint cosmosCoordinate, dbID string) (map[string]any, error) {
			coll := "c"
			cosmosDiffMakeContainer(t, endpoint, dbID, coll)
			collLink := "dbs/" + dbID + "/colls/" + coll
			out, st := cosmosRESTReq(t, endpoint, "GET", "/"+collLink+"/conflicts", "conflicts", collLink, "", nil)
			conflicts, _ := out["Conflicts"].([]any)
			return map[string]any{"status": st, "count": len(conflicts)}, nil
		}},
	}
}

// cosmosChangeFeedRead issues a signed change-feed read, the continuation in
// If-None-Match, and returns the body, the status, and the response etag. A
// 304 yields a nil body.
func cosmosChangeFeedRead(t *testing.T, endpoint cosmosCoordinate, base, collLink string, feed map[string]string, continuation string) (map[string]any, int, string) {
	t.Helper()
	xmsDate := time.Now().UTC().Format(http.TimeFormat)
	req, err := http.NewRequest("GET", strings.TrimRight(endpoint.url, "/")+base+"/docs", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("x-ms-date", xmsDate)
	req.Header.Set("x-ms-version", "2018-12-31")
	req.Header.Set("Authorization", cosmosSign(endpoint.key, "GET", "docs", collLink, xmsDate))
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

// changeFeedIDs returns a change-feed response's document IDs, sorted.
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
