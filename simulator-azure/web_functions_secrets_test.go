package main

import "testing"

// The SDK suite's TestSDK_FunctionApps_NodeHostRunsTheDeployedFunctions proves
// these formats against the running Functions host.

func TestFunctionsKeysAreIdentifiableSecrets(t *testing.T) {
	for _, seed := range []uint64{functionsMasterKeySeed, functionsSystemKeySeed, functionsFunctionKeySeed} {
		if key := newFunctionsKey(seed); !validFunctionsKey(key, seed) {
			t.Fatalf("generated key %q does not validate under its own seed", key)
		}
	}
	if validFunctionsKey(newFunctionsKey(functionsFunctionKeySeed), functionsMasterKeySeed) {
		t.Fatal("a function key validated under the master key seed")
	}
}

func TestFunctionSecretProtectionRoundTrips(t *testing.T) {
	key := newSiteEncryptionKey()
	protected, err := protectFunctionSecret(key, "a key value")
	if err != nil {
		t.Fatal(err)
	}
	if protected[:5] != "CfDJ8" {
		t.Fatalf("payload %q does not open with the Data Protection header", protected)
	}
	back, err := unprotectFunctionSecret(key, protected)
	if err != nil || back != "a key value" {
		t.Fatalf("round trip = %q, %v", back, err)
	}
	if _, err := unprotectFunctionSecret(newSiteEncryptionKey(), protected); err == nil {
		t.Fatal("a payload unprotected under another site's key")
	}
}
