package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestKMSKeyPolicyPersistsAcrossStoreReopen(t *testing.T) {
	stateDir := t.TempDir()
	db, err := sim.OpenDB(stateDir)
	if err != nil {
		t.Fatalf("open simulator state: %v", err)
	}
	store, err := sim.NewSQLiteStore[KMSKey](db, "kms_keys")
	if err != nil {
		t.Fatalf("open KMS key store: %v", err)
	}

	const policy = `{"Version":"2012-10-17","Statement":[{"Sid":"AllowLogs","Effect":"Allow","Principal":{"Service":"logs.us-east-1.amazonaws.com"},"Action":"kms:Encrypt","Resource":"*"}]}`
	store.Put("key-1", KMSKey{
		KeyId:      "key-1",
		KeyState:   "Enabled",
		PolicyJSON: policy,
	})
	if err := db.Close(); err != nil {
		t.Fatalf("close simulator state: %v", err)
	}

	db, err = sim.OpenDB(stateDir)
	if err != nil {
		t.Fatalf("reopen simulator state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err = sim.NewSQLiteStore[KMSKey](db, "kms_keys")
	if err != nil {
		t.Fatalf("reopen KMS key store: %v", err)
	}
	got, ok := store.Get("key-1")
	if !ok {
		t.Fatal("persisted KMS key disappeared after reopening the state store")
	}
	if got.PolicyJSON != policy {
		t.Fatalf("persisted KMS key policy = %q, want %q", got.PolicyJSON, policy)
	}
}
