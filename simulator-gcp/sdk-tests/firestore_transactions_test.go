package gcp_sdk_test

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestFirestore_RunTransaction drives beginTransaction, a transactional read,
// commit and rollback through the high-level client's RunTransaction.
func TestFirestore_RunTransaction(t *testing.T) {
	t.Parallel()
	c := newFSClient(t, "fs-txn")
	doc := c.Collection("accounts").Doc("a1")

	if _, err := doc.Set(ctx, map[string]any{"balance": int64(100)}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Read the balance and write balance-30: beginTransaction, a transactional
	// Get, then commit.
	err := c.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(doc)
		if err != nil {
			return err
		}
		bal, err := snap.DataAt("balance")
		if err != nil {
			return err
		}
		return tx.Set(doc, map[string]any{"balance": bal.(int64) - 30})
	})
	if err != nil {
		t.Fatalf("RunTransaction: %v", err)
	}

	got, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("read-back: %v", err)
	}
	if v, _ := got.DataAt("balance"); v != int64(70) {
		t.Fatalf("balance after txn = %v, want 70", v)
	}
}

// TestFirestore_TransactionRollback asserts that returning an error from the
// transaction function rolls the transaction back: no write is applied.
func TestFirestore_TransactionRollback(t *testing.T) {
	t.Parallel()
	c := newFSClient(t, "fs-txn-rollback")
	doc := c.Collection("accounts").Doc("a2")
	if _, err := doc.Set(ctx, map[string]any{"balance": int64(50)}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("abort")
	err := c.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		if err := tx.Set(doc, map[string]any{"balance": int64(999)}); err != nil {
			return err
		}
		return sentinel // abort → rollback
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunTransaction error = %v, want the sentinel", err)
	}

	got, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("read-back: %v", err)
	}
	if v, _ := got.DataAt("balance"); v != int64(50) {
		t.Fatalf("balance after rollback = %v, want 50 (write must not apply)", v)
	}
}

// TestFirestore_WritesDurableAcrossTransactions: a committed Create makes a
// second Create of the same document fail.
func TestFirestore_WritesDurableAcrossTransactions(t *testing.T) {
	t.Parallel()
	c := newFSClient(t, "fs-txn-durable")
	doc := c.Collection("accounts").Doc("a3")
	if _, err := doc.Create(ctx, map[string]any{"n": int64(1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The REST transport maps HTTP 409 ALREADY_EXISTS to codes.Aborted (see
	// TestFirestore_CreatePrecondition).
	_, err := doc.Create(ctx, map[string]any{"n": int64(2)})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("second Create code = %v, want Aborted (REST 409 mapping)", status.Code(err))
	}
}
