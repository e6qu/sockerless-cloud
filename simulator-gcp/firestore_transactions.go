package main

import (
	"encoding/base64"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Firestore transactions: beginTransaction / commit (with a transaction) /
// rollback, plus transactional reads (batchGet / runQuery carrying a
// `transaction` token). The model matches the Firestore v1 REST surface the
// high-level `cloud.google.com/go/firestore` client drives via RunTransaction:
//
//   - beginTransaction issues an opaque token and pins a read snapshot time.
//   - A read carrying the token reports that snapshot time as its readTime.
//   - commit carrying the token applies its writes atomically (the same
//     precondition-checked path as a non-transactional commit) and retires the
//     token — a transaction can be committed at most once.
//   - rollback retires the token.
//
// The simulator serves requests serially per transaction (the client issues a
// transaction's reads and its commit in sequence), so a single stored snapshot
// time is sufficient; an unknown or already-retired token is rejected with
// INVALID_ARGUMENT exactly as real Firestore does, never silently accepted.

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
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid beginTransaction body: %v", err)
		return
	}
	readTime := fsNow()
	readOnly := req.Options.ReadOnly != nil
	if readOnly && req.Options.ReadOnly.ReadTime != "" {
		readTime = req.Options.ReadOnly.ReadTime
	}
	token := base64.StdEncoding.EncodeToString([]byte(generateUUID()))
	fsTransactions.Put(token, fsTxn{ID: token, ReadTime: readTime, ReadOnly: readOnly})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"transaction": token})
}

func handleFSRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Transaction string `json:"transaction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid rollback body: %v", err)
		return
	}
	if req.Transaction == "" {
		sim.GCPError(w, http.StatusBadRequest, "Transaction is required.", "INVALID_ARGUMENT")
		return
	}
	if _, ok := fsTransactions.Get(req.Transaction); !ok {
		sim.GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	fsTransactions.Delete(req.Transaction)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// fsReadTimeForTxn resolves the readTime a read should report. With no
// transaction it's the current time; with a transaction it's the pinned
// snapshot time, and an unknown/retired token reports ok=false so the caller can
// reject it with INVALID_ARGUMENT.
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
