package main

import (
	"encoding/json"
	"testing"
)

// TestKVStoragePersistenceRoundTrip asserts that the KV persistence
// wrapper types (kvSecretStored, kvKeyStored, kvCertStored) round-trip
// through json.Marshal + json.Unmarshal with both the Vault+Name
// fields and the embedded wire fields intact. sim.Store uses
// json.Marshal internally; if Vault+Name aren't preserved, List
// handlers filter on zero values and return empty results.
//
// Regression check: every KV wrapper must keep Vault+Name reachable
// after a store round-trip so List queries continue to work after a
// SIM_PERSIST=true restart.
func TestKVStoragePersistenceRoundTrip(t *testing.T) {
	t.Run("secret", func(t *testing.T) {
		rec := kvSecretStored{
			Vault: "myvault",
			Name:  "mysecret",
			Versions: []kvSecretVersion{{
				Version:     "v1",
				Value:       "super-secret-value",
				ContentType: "text/plain",
				Tags:        map[string]string{"env": "prod"},
				Attributes:  KeyVaultAttrs{Enabled: true, Created: 1700000000, Updated: 1700000000},
			}},
		}
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got kvSecretStored
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Vault != "myvault" {
			t.Errorf("Vault dropped on round-trip: got %q, want %q", got.Vault, "myvault")
		}
		if got.Name != "mysecret" {
			t.Errorf("Name dropped on round-trip: got %q, want %q", got.Name, "mysecret")
		}
		if len(got.Versions) != 1 {
			t.Fatalf("Versions chain dropped: got %d, want 1", len(got.Versions))
		}
		gotV := got.latest()
		if gotV.Value != "super-secret-value" {
			t.Errorf("Value dropped on round-trip: got %q", gotV.Value)
		}
		if gotV.Version != "v1" {
			t.Errorf("Version drifted: got %q, want %q", gotV.Version, "v1")
		}
		if gotV.Tags["env"] != "prod" {
			t.Errorf("Tags dropped: got %v", gotV.Tags)
		}
	})

	t.Run("key", func(t *testing.T) {
		rec := kvKeyStored{
			Vault: "myvault",
			Name:  "mykey",
			KeyVaultKey: KeyVaultKey{
				ID: "https://myvault.vault.azure.net/keys/mykey/v1",
				JsonWebKey: map[string]any{
					"kty": "RSA",
					"n":   "real-base64url-modulus-bytes",
					"e":   "AQAB",
				},
				Attributes: KeyVaultAttrs{Enabled: true},
				Tags:       map[string]string{"role": "signing"},
			},
		}
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got kvKeyStored
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Vault != "myvault" || got.Name != "mykey" {
			t.Errorf("Vault/Name dropped: %+v", got)
		}
		if got.JsonWebKey["kty"] != "RSA" {
			t.Errorf("JsonWebKey dropped: got %v", got.JsonWebKey)
		}
	})

	t.Run("certificate", func(t *testing.T) {
		rec := kvCertStored{
			Vault: "myvault",
			Name:  "mycert",
			KeyVaultCertificate: KeyVaultCertificate{
				ID:             "https://myvault.vault.azure.net/certificates/mycert/v1",
				X509Thumbprint: "ABCDEF12",
				Policy:         &kvCertPolicy{Issuer: &kvCertIssuer{Name: "Self"}},
				Tags:           map[string]string{"team": "infra"},
			},
		}
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got kvCertStored
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Vault != "myvault" || got.Name != "mycert" {
			t.Errorf("Vault/Name dropped: %+v", got)
		}
		if got.X509Thumbprint != "ABCDEF12" {
			t.Errorf("X509Thumbprint dropped: got %q", got.X509Thumbprint)
		}
	})
}

// TestKVWireShapeOmitsStorageFields asserts the wire structs
// (KeyVaultSecret / Key / Certificate) do not carry Vault+Name —
// those fields belong to the persistence wrappers, not the wire.
// Real Azure KV's response shape doesn't include either; emitting
// them would be a wire-fidelity drift.
func TestKVWireShapeOmitsStorageFields(t *testing.T) {
	secret := KeyVaultSecret{
		ID:    "https://myvault.vault.azure.net/secrets/mysecret/v1",
		Value: "x",
	}
	data, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); contains(got, `"vault"`) || contains(got, `"name"`) {
		t.Errorf("wire shape leaks storage fields: %s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
