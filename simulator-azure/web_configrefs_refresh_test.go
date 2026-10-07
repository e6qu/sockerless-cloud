package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A running app keeps the Key Vault reference values it started with until
// App Service's cache of them expires; the re-fetch then restarts the app
// when a versionless reference resolves to a rotated secret, and caches the
// values for another period when nothing changed.
func TestWebKeyVaultReferenceRefreshRestartsTheAppOnARotatedSecret(t *testing.T) {
	priorSites, priorConfigs, priorVaults, priorSecrets := azfSites, siteConfigStore, keyVaults, keyVaultData
	azfSites = sim.MakeStore[Site](nil, "test_kvref_sites")
	siteConfigStore = sim.MakeStore[siteConfigPayload](nil, "test_kvref_site_configs")
	keyVaults = sim.MakeStore[KeyVault](nil, "test_kvref_vaults")
	keyVaultData = sim.MakeStore[kvSecretStored](nil, "test_kvref_secrets")
	t.Cleanup(func() {
		azfSites, siteConfigStore, keyVaults, keyVaultData = priorSites, priorConfigs, priorVaults, priorSecrets
	})

	const principal = "5b0b6c1e-6a43-4f8e-9d1b-2f4f1c3e7a10"
	vault := KeyVault{
		ID:   "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/refreshvault",
		Name: "refreshvault",
		Properties: KeyVaultProperties{AccessPolicies: []KeyVaultAccessPolicy{{
			ObjectID:    principal,
			Permissions: KeyVaultPermissions{Secrets: []string{"get"}},
		}}},
	}
	keyVaults.Put(vault.ID, vault)
	setSecret := func(version, value string) {
		stored, _ := keyVaultData.Get(keyVaultSecretKey(vault.Name, "db-password"))
		stored.Vault, stored.Name = vault.Name, "db-password"
		stored.Versions = append(stored.Versions, kvSecretVersion{
			Version: version, Value: value, Attributes: KeyVaultAttrs{Enabled: true},
		})
		keyVaultData.Put(keyVaultSecretKey(vault.Name, "db-password"), stored)
	}
	setSecret("v1", "first")
	site := Site{
		ID:       "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/sites/refresh-app",
		Name:     "refresh-app",
		Identity: &SiteIdentity{Type: "SystemAssigned", PrincipalID: principal},
		Properties: SiteProperties{SiteConfig: &SiteConfig{AppSettings: []NameValuePair{
			{Name: "DB", Value: "@Microsoft.KeyVault(VaultName=refreshvault;SecretName=db-password)"},
			{Name: "PLAIN", Value: "unchanged"},
		}}},
	}
	azfSites.Put(site.ID, site)
	t.Cleanup(func() { stopAzureFunctionInstance(site.Name) })

	const containerID = "refresh-app-main"
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	inst.containerID = containerID
	inst.cacheReferencesLocked(site.ID, containerID, webSiteReferenceValues(&site))
	cached := inst.references.appSettings["DB"]
	firstRefresh := inst.referenceRefresh
	inst.mu.Unlock()
	if cached != "first" {
		t.Fatalf("the app started with DB=%q, want the secret's value", cached)
	}
	if firstRefresh == nil {
		t.Fatal("no re-fetch is scheduled for the running app")
	}

	inst.refreshReferences(site.ID, containerID)
	inst.mu.Lock()
	running, rescheduled := inst.containerID, inst.referenceRefresh
	inst.mu.Unlock()
	if running != containerID {
		t.Fatalf("a re-fetch that found the same values restarted the app")
	}
	if rescheduled == nil || rescheduled == firstRefresh {
		t.Fatal("a re-fetch that found the same values did not schedule the next one")
	}

	setSecret("v2", "rotated")
	inst.refreshReferences(site.ID, containerID)
	inst.mu.Lock()
	running = inst.containerID
	inst.mu.Unlock()
	if running != "" {
		t.Fatalf("a re-fetch that found a rotated secret left the app running on the old value")
	}
	if got := webSiteReferenceValues(&site).appSettings["DB"]; got != "rotated" {
		t.Fatalf("the next start resolves DB=%q, want the rotated secret", got)
	}

	inst.refreshReferences(site.ID, containerID)
	inst.mu.Lock()
	running = inst.containerID
	inst.mu.Unlock()
	if running != "" {
		t.Fatalf("a re-fetch scheduled for containers that are gone touched the app")
	}
}
