package main

import (
	"encoding/base64"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Firestore transactions as the cloud.google.com/go/firestore client's
// RunTransaction drives them: beginTransaction issues an opaque token pinning
// a read snapshot, a read carrying the token reports that snapshot time,
// commit applies the writes atomically and retires the token, and rollback
// retires it. An unknown or retired token is INVALID_ARGUMENT, as Firestore
// answers.

type fsTxn struct {
	ID       string `json:"id"`
	ReadTime string `json:"readTime"`
	ReadOnly bool   `json:"readOnly"`
}

var fsTransactions sim.Store[fsTxn]

func handleFSBeginTransaction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options struct {
			ReadOnly *struct {
				ReadTime string `json:"readTime"`
			} `json:"readOnly"`
			ReadWrite *struct {
				RetryTransaction string `json:"retryTransaction"`
			} `json:"readWrite"`
		} `json:"options"`
	}
	// The options object is optional (an absent body defaults to a read-write
	// transaction); a present-but-malformed body is rejected.
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid beginTransaction body: %v", err)
		return
	}
	readTime := fsNow()
	readOnly := req.Options.ReadOnly != nil
	if readOnly && req.Options.ReadOnly.ReadTime != "" {
		readTime = req.Options.ReadOnly.ReadTime
	}
	token := base64.StdEncoding.EncodeToString([]byte(sim.NewUUID()))
	fsTransactions.Put(token, fsTxn{ID: token, ReadTime: readTime, ReadOnly: readOnly})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"transaction": token})
}

func handleFSRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Transaction string `json:"transaction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid rollback body: %v", err)
		return
	}
	if req.Transaction == "" {
		GCPError(w, http.StatusBadRequest, "Transaction is required.", "INVALID_ARGUMENT")
		return
	}
	if _, ok := fsTransactions.Get(req.Transaction); !ok {
		GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	fsTransactions.Delete(req.Transaction)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// fsReadTimeForTxn resolves the readTime a read reports: now without a
// transaction, the pinned snapshot with one, and ok=false for an unknown or
// retired token.
func fsReadTimeForTxn(token string) (string, bool) {
	if token == "" {
		return fsNow(), true
	}
	txn, ok := fsTransactions.Get(token)
	if !ok {
		return "", false
	}
	return txn.ReadTime, true
}
