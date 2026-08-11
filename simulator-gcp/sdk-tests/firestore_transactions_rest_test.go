package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFirestore_TransactionRESTEndpoints drives the Firestore transaction
// endpoints directly over REST (a real Firestore client surface), covering the
// routes the high-level RunTransaction test exercises plus the invalid-token
// rejection the high-level client can't easily trigger:
//
//	POST /v1/projects/{project}/databases/{database}/documents:beginTransaction
//	POST /v1/projects/{project}/databases/{database}/documents:rollback
//	POST /v1/projects/{project}/databases/{database}/documents:commit (transactional)
func TestFirestore_TransactionRESTEndpoints(t *testing.T) {
	const db = "/v1/projects/test-project/databases/(default)/documents"

	post := func(suffix, body string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Post(baseURL+suffix, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	// beginTransaction → opaque token.
	resp, body := post(db+":beginTransaction", `{"options":{"readWrite":{}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "beginTransaction body=%s", body)
	var begun struct {
		Transaction string `json:"transaction"`
	}
	require.NoError(t, json.Unmarshal(body, &begun))
	require.NotEmpty(t, begun.Transaction, "beginTransaction must return a token")

	// Transactional commit with that token applies the write and retires the token.
	docName := "projects/test-project/databases/(default)/documents/txn-rest/d1"
	commitBody := `{"transaction":"` + begun.Transaction + `","writes":[{"update":{"name":"` + docName + `","fields":{"v":{"integerValue":"7"}}}}]}`
	resp, body = post(db+":commit", commitBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, "commit body=%s", body)

	// Committing again with the now-retired token is rejected, not silently accepted.
	resp, body = post(db+":commit", commitBody)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "reused-token commit must fail; body=%s", body)
	assert.Contains(t, string(body), "INVALID_ARGUMENT")

	// rollback retires a fresh token; a subsequent rollback of it is rejected.
	resp, body = post(db+":beginTransaction", `{}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "beginTransaction(2) body=%s", body)
	var begun2 struct {
		Transaction string `json:"transaction"`
	}
	require.NoError(t, json.Unmarshal(body, &begun2))

	resp, body = post(db+":rollback", `{"transaction":"`+begun2.Transaction+`"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "rollback body=%s", body)

	resp, body = post(db+":rollback", `{"transaction":"`+begun2.Transaction+`"}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "rollback of retired token must fail; body=%s", body)
	assert.Contains(t, string(body), "INVALID_ARGUMENT")
}
