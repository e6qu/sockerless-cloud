package simkms

import (
	"bytes"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpensOnlyWithTheSameKeyAndAAD(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(key, []byte("secret"), []byte("context"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(key, sealed, []byte("context"))
	if err != nil || string(got) != "secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := Open(key, sealed, []byte("other context")); err == nil {
		t.Error("Open accepted a different aad")
	}
	other, _ := NewKey()
	if _, err := Open(other, sealed, []byte("context")); err == nil {
		t.Error("Open accepted a different key")
	}
	if _, err := Open(key, sealed[:5], nil); err != ErrTruncated {
		t.Errorf("Open of a truncated value = %v, want ErrTruncated", err)
	}
}

// The two worked examples of RFC 5649 section 6.
func TestWrapWithPaddingMatchesRFC5649(t *testing.T) {
	kek := mustHex(t, "5840df6e29b02af1ab493b705bf16ea1ae8338f4dcc176a8")
	for _, tc := range []struct{ key, wrapped string }{
		{"c37b7e6492584340bed12207808941155068f738", "138bdeaa9b8fa7fc61f97742e72248ee5ae6ae5360d1ae6a5f54f373fa543b6a"},
		{"466f7250617369", "afbeb0f07dfbf5419200f2ccb50bb24f"},
	} {
		wrapped, err := WrapWithPadding(kek, mustHex(t, tc.key))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(wrapped) != tc.wrapped {
			t.Errorf("wrap %s = %x, want %s", tc.key, wrapped, tc.wrapped)
		}
		key, err := UnwrapWithPadding(kek, wrapped)
		if err != nil || hex.EncodeToString(key) != tc.key {
			t.Errorf("unwrap = %x, %v, want %s", key, err, tc.key)
		}
	}
	wrapped, _ := WrapWithPadding(kek, mustHex(t, "466f7250617369"))
	wrapped[3] ^= 1
	if _, err := UnwrapWithPadding(kek, wrapped); err == nil {
		t.Error("unwrap accepted a tampered value")
	}
}

type keyRow struct{ Key []byte }

func TestEnsureKeyGeneratesOnceUnderConcurrentFirstUse(t *testing.T) {
	store := sim.NewStateStore[keyRow]()
	keyOf := func(r keyRow) []byte { return r.Key }
	withKey := func(r *keyRow, k []byte) { r.Key = k }
	keys := make([][]byte, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := EnsureKey(store, "service-key", keyOf, withKey)
			if err != nil {
				t.Error(err)
			}
			keys[i] = key
		}()
	}
	wg.Wait()
	for _, key := range keys {
		if len(key) != KeySize || !bytes.Equal(key, keys[0]) {
			t.Fatalf("callers got different keys: %x vs %x", key, keys[0])
		}
	}
	stored, _ := store.Get("service-key")
	if !bytes.Equal(stored.Key, keys[0]) {
		t.Error("the stored key is not the one callers sealed under")
	}
}
