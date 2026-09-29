package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/simjwt"
)

// A data directory an earlier release wrote keeps its access-token key: the
// same key, published under the same key id.
func TestAccessTokenKeyStoreReadsThePersistedRecord(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	store := sim.NewStateStore[accessTokenSigningKeyRecord]()
	store.Put(accessTokenSigningKeyID, accessTokenSigningKeyRecord{PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))})

	signer, err := simjwt.LoadOrCreate(accessTokenKeyStore{store}, accessTokenSigningKeyID, simjwt.RS256)
	if err != nil {
		t.Fatalf("load persisted key: %v", err)
	}
	if !key.PublicKey.Equal(signer.Key().Public()) {
		t.Fatal("the persisted key was replaced")
	}
	want, err := simjwt.Thumbprint(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if signer.KeyID() != want {
		t.Fatalf("kid = %q, want %q", signer.KeyID(), want)
	}
}

func TestServiceAccountSignJwtVerifiesUnderItsKeyID(t *testing.T) {
	previous := iamSASystemKeys
	iamSASystemKeys = sim.NewStateStore[serviceAccountSystemKey]()
	t.Cleanup(func() { iamSASystemKeys = previous })

	name := "projects/p/serviceAccounts/sa@p.iam.gserviceaccount.com"
	material, err := serviceAccountSigningKey(name, "sa@p.iam.gserviceaccount.com")
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"iss":"sa@p.iam.gserviceaccount.com","exp":9999999999}`
	signed, err := material.jwt.SignPayload([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := simjwt.Verify(signed, &claims, simjwt.Options{}, material.jwt); err != nil {
		t.Fatalf("signJwt output does not verify: %v", err)
	}
	if material.jwt.KeyID() != material.keyID {
		t.Fatalf("JWT kid %q, published key id %q", material.jwt.KeyID(), material.keyID)
	}
	again, err := serviceAccountSigningKey(name, "sa@p.iam.gserviceaccount.com")
	if err != nil || again.keyID != material.keyID {
		t.Fatalf("second resolution changed the key: %v", err)
	}
}
