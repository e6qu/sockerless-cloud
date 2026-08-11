package gcp_sdk_test

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudkms/v1"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

// TestSecretManager_ConcurrentAddVersion_UniqueIDs asserts that concurrent
// AddSecretVersion calls each get a distinct, monotonic version ID — the
// per-secret counter is reserved atomically, so no two versions collide and
// none is silently overwritten.
func TestSecretManager_ConcurrentAddVersion_UniqueIDs(t *testing.T) {
	svc := secretManagerService(t)
	parent := "projects/sdk-sm-concurrency"
	secretID := "concurrent-versions"
	secretName := parent + "/secrets/" + secretID

	_, err := svc.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(secretID).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Secrets.Delete(secretName).Do() })

	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Projects.Secrets.AddVersion(secretName, &secretmanager.AddSecretVersionRequest{
				Payload: &secretmanager.SecretPayload{
					Data: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("payload-%d", i))),
				},
			}).Do()
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		require.NoError(t, e, "AddVersion %d", i)
	}

	versions, err := svc.Projects.Secrets.Versions.List(secretName).Do()
	require.NoError(t, err)
	require.Len(t, versions.Versions, n, "every concurrent AddVersion must persist a distinct version")

	ids := map[int]bool{}
	for _, v := range versions.Versions {
		idStr := v.Name[strings.LastIndex(v.Name, "/")+1:]
		id, perr := strconv.Atoi(idStr)
		require.NoError(t, perr)
		require.False(t, ids[id], "duplicate version ID %d — IDs collided under concurrency", id)
		ids[id] = true
	}
	// IDs must be exactly 1..n (monotonic, no gaps, no collisions).
	got := make([]int, 0, len(ids))
	for id := range ids {
		got = append(got, id)
	}
	sort.Ints(got)
	for i := 0; i < n; i++ {
		require.Equal(t, i+1, got[i], "version IDs must be the contiguous range 1..n")
	}
}

// TestSecretManager_ListFilter asserts ListSecrets honors the `filter` query
// param (label match), instead of returning the full set.
func TestSecretManager_ListFilter(t *testing.T) {
	svc := secretManagerService(t)
	parent := "projects/sdk-sm-filter"

	mk := func(id string, labels map[string]string) {
		_, err := svc.Projects.Secrets.Create(parent, &secretmanager.Secret{
			Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
			Labels:      labels,
		}).SecretId(id).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Projects.Secrets.Delete(parent + "/secrets/" + id).Do() })
	}
	mk("prod-one", map[string]string{"env": "prod"})
	mk("prod-two", map[string]string{"env": "prod"})
	mk("dev-one", map[string]string{"env": "dev"})

	resp, err := svc.Projects.Secrets.List(parent).Filter("labels.env=prod").Do()
	require.NoError(t, err)
	require.Len(t, resp.Secrets, 2, "filter labels.env=prod must return only the 2 prod secrets")
	for _, s := range resp.Secrets {
		require.Equal(t, "prod", s.Labels["env"])
	}
}

// TestCloudKMS_ConcurrentCreateVersion_UniqueIDs asserts concurrent
// CreateCryptoKeyVersion calls each reserve a distinct version ID atomically.
func TestCloudKMS_ConcurrentCreateVersion_UniqueIDs(t *testing.T) {
	svc := kmsService(t)
	parent := "projects/sdk-kms-concurrency/locations/global"
	ringID := "concurrency-ring"
	ringName := parent + "/keyRings/" + ringID
	_, err := svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	require.NoError(t, err)

	keyName := ringName + "/cryptoKeys/concurrency-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "ENCRYPT_DECRYPT",
	}).CryptoKeyId("concurrency-key").Do()
	require.NoError(t, err)

	// The key starts with primary version 1. Spawn concurrent new-version
	// creations.
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Create(
				keyName, &cloudkms.CryptoKeyVersion{}).Do()
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		require.NoError(t, e, "CreateCryptoKeyVersion %d", i)
	}

	versions, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.List(keyName).Do()
	require.NoError(t, err)
	// 1 initial + n concurrently created, all distinct.
	require.Len(t, versions.CryptoKeyVersions, n+1, "every concurrent CreateCryptoKeyVersion must persist a distinct version")
	seen := map[string]bool{}
	for _, v := range versions.CryptoKeyVersions {
		require.False(t, seen[v.Name], "duplicate version %s — IDs collided under concurrency", v.Name)
		seen[v.Name] = true
	}
}
