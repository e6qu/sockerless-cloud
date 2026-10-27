package gcp_sdk_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFirestore_RunQueryStreamsChunked: Firestore's REST runQuery and batchGet
// stream a JSON array with chunked transfer-encoding, one element per chunk,
// not a buffered body with a Content-Length. The high-level SDK buffers the
// whole array, so a raw HTTP client inspects the framing.
func TestFirestore_RunQueryStreamsChunked(t *testing.T) {
	t.Parallel()
	const db = "/v1/projects/test-project/databases/(default)/documents"

	// Three documents, so runQuery and batchGet each return several elements.
	seed := func(id string, n int) {
		body := `{"fields":{"n":{"integerValue":"` + strconv.Itoa(n) + `"}}}`
		resp, err := http.Post(baseURL+db+"/streamq?documentId="+id, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	seed("a", 1)
	seed("b", 2)
	seed("c", 3)

	query := `{"structuredQuery":{"from":[{"collectionId":"streamq"}],"orderBy":[{"field":{"fieldPath":"n"},"direction":"ASCENDING"}]}}`
	req, err := http.NewRequest(http.MethodPost, baseURL+db+":runQuery", strings.NewReader(query))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	// Go's net/http reports a chunked response as TransferEncoding ["chunked"]
	// with ContentLength -1; a buffered body would carry a fixed Content-Length.
	assert.Equal(t, []string{"chunked"}, resp.TransferEncoding, "runQuery must stream (chunked), not buffer")
	assert.EqualValues(t, -1, resp.ContentLength, "a streamed response carries no fixed Content-Length")

	body, err := io.ReadAll(bufio.NewReader(resp.Body))
	require.NoError(t, err)

	// The fully-received body is a valid JSON array of RunQueryResponse elements,
	// ordered by the query — identical content to the buffered form.
	var elems []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &elems), "streamed body must be a valid JSON array: %s", body)
	require.Len(t, elems, 3, "three matching documents → three stream elements")
	for _, el := range elems {
		_, hasDoc := el["document"]
		_, hasReadTime := el["readTime"]
		assert.True(t, hasDoc && hasReadTime, "each element is a RunQueryResponse with document + readTime")
	}
}
