package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// azcosmos v1.4.2 has no change-feed pager, so these tests drive the REST data
// plane as the feed works on the wire: a documents read with
// `A-IM: Incremental feed`, paged by `x-ms-max-item-count`, the continuation
// sent in `If-None-Match` and returned in `etag`. The conflict feed
// (/dbs/{db}/colls/{coll}/conflicts) of a single-region account is empty.

// cosmosChangeFeedPage returns the documents, the advanced continuation (the
// response etag) and the HTTP status.
func cosmosChangeFeedPage(t *testing.T, account, coll, continuation string, maxItems int) ([]map[string]any, string, int) {
	t.Helper()
	req, err := http.NewRequest("GET", baseURL+"/dbs/cfdb/colls/"+coll+"/docs", nil)
	require.NoError(t, err)
	req.Host = cosmosDataPlaneHost(t, account)
	req.Header.Set("A-IM", "Incremental feed")
	if continuation != "" {
		req.Header.Set("If-None-Match", continuation)
	}
	if maxItems > 0 {
		req.Header.Set("x-ms-max-item-count", itoa(maxItems))
	}
	cosmosSignDataPlane(t, req, cosmosAccountKey(t, account))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	etag := resp.Header.Get("etag")
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, resp.StatusCode
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out struct {
		Documents []map[string]any `json:"Documents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Documents, etag, resp.StatusCode
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func cosmosSeedDoc(t *testing.T, account, coll, doc string) {
	t.Helper()
	resp := cosmosScript(t, "POST", account, "/dbs/cfdb/colls/"+coll+"/docs", doc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

// TestCosmosChangeFeed_Incremental: a fresh feed returns every document; from
// the returned continuation only a newly created one comes back; with nothing
// changed the read is 304 Not Modified.
func TestCosmosChangeFeed_Incremental(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmoschangefeed")
	coll := "events"

	cosmosSeedDoc(t, account, coll, `{"id":"e1","pk":"p","seq":1}`)
	cosmosSeedDoc(t, account, coll, `{"id":"e2","pk":"p","seq":2}`)

	// Both documents, in creation order.
	docs, cont, _ := cosmosChangeFeedPage(t, account, coll, "", 0)
	require.Len(t, docs, 2, "fresh feed returns every document")
	assert.Equal(t, "e1", docs[0]["id"])
	assert.Equal(t, "e2", docs[1]["id"])
	require.NotEmpty(t, cont, "feed advances a continuation")

	docs, cont2, status := cosmosChangeFeedPage(t, account, coll, cont, 0)
	assert.Equal(t, http.StatusNotModified, status, "no changes → 304 Not Modified")
	require.Empty(t, docs)

	cosmosSeedDoc(t, account, coll, `{"id":"e3","pk":"p","seq":3}`)
	docs, cont3, status := cosmosChangeFeedPage(t, account, coll, cont2, 0)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, docs, 1, "only the new document is returned incrementally")
	assert.Equal(t, "e3", docs[0]["id"])
	require.NotEmpty(t, cont3)

	_, _, status = cosmosChangeFeedPage(t, account, coll, cont3, 0)
	assert.Equal(t, http.StatusNotModified, status)
}

// TestCosmosChangeFeed_Paginated walks every document one page at a time
// through the advancing continuation, in order.
func TestCosmosChangeFeed_Paginated(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmoschangefeedpage")
	coll := "log"

	want := []string{"a", "b", "c", "d"}
	for _, id := range want {
		cosmosSeedDoc(t, account, coll, `{"id":"`+id+`","pk":"p"}`)
	}

	var got []string
	cont := ""
	for i := 0; i < 10; i++ {
		docs, next, status := cosmosChangeFeedPage(t, account, coll, cont, 1)
		if status == http.StatusNotModified {
			break
		}
		if len(docs) == 0 {
			break
		}
		require.LessOrEqual(t, len(docs), 1, "max-item-count=1 caps the page")
		for _, d := range docs {
			id, _ := d["id"].(string)
			got = append(got, id)
		}
		cont = next
	}
	assert.Equal(t, want, got, "the feed walks every doc in order, one per page")
}

// TestCosmosConflictFeed_EmptySingleRegion: the conflict feed of a
// single-region account is empty and correctly shaped.
func TestCosmosConflictFeed_EmptySingleRegion(t *testing.T) {
	t.Parallel()
	account := "sdkcosmosconflicts"

	req, err := http.NewRequest("GET", baseURL+"/dbs/cfdb/colls/items/conflicts", nil)
	require.NoError(t, err)
	req.Host = cosmosDataPlaneHost(t, account)
	cosmosSignDataPlane(t, req, cosmosAccountKey(t, account))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Conflicts []map[string]any `json:"Conflicts"`
		Count     int              `json:"_count"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Empty(t, out.Conflicts, "single-region account has no conflicts")
	assert.Equal(t, 0, out.Count)
}
