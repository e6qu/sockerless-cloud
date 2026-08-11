package gcp_sdk_test

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestFirestore_RunTransaction exercises the Firestore transaction surface
// (beginTransaction / transactional reads / commit / rollback, BUG-2158) via the
// high-level client's RunTransaction — the read-modify-write pattern that
// previously 404'd because the transaction endpoints were unimplemented.
func TestFirestore_RunTransaction(t *testing.T) {
	c := newFSClient(t, "fs-txn")
	doc := c.Collection("accounts").Doc("a1")

	if _, err := doc.Set(ctx, map[string]any{"balance": int64(100)}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Read-modify-write inside a transaction: read the balance, then write
	// balance-30. This drives beginTransaction → transactional Get → commit.
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

// TestFirestore_WritesDurableAcrossTransactions confirms the transaction
// plumbing doesn't lose committed writes: a committed Create makes a second
// Create of the same document fail with AlreadyExists.
func TestFirestore_WritesDurableAcrossTransactions(t *testing.T) {
	c := newFSClient(t, "fs-txn-durable")
	doc := c.Collection("accounts").Doc("a3")
	if _, err := doc.Create(ctx, map[string]any{"n": int64(1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The REST transport maps the sim's HTTP 409 ALREADY_EXISTS to codes.Aborted
	// (the gax REST status-mapping quirk also relied on by TestFirestore_CreatePrecondition).
	_, err := doc.Create(ctx, map[string]any{"n": int64(2)})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("second Create code = %v, want Aborted (REST 409 mapping)", status.Code(err))
	}
}
