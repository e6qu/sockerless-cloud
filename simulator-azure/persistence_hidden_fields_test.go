package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// storeReopenRoundTrip closes and reopens the SQLite store, as a SIM_PERSIST
// restart does. Hidden (json:"-") fields ride the persistence sidecar, so a JSON
// round-trip cannot stand in for this.
func storeReopenRoundTrip[T any](t *testing.T, table string, item T) T {
	t.Helper()
	dir := t.TempDir()
	db, err := sim.OpenDB(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store, err := sim.NewSQLiteStore[T](db, table)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	store.Put("item", item)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	db, err = sim.OpenDB(dir)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err = sim.NewSQLiteStore[T](db, table)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got, ok := store.Get("item")
	if !ok {
		t.Fatal("persisted item missing after reopen")
	}
	return got
}

// cosmosDocsFor filters on Account, DB and Coll, so losing them would hide
// every persisted document from query and list while point reads still worked.
func TestCosmosDocumentPersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "cosmos_documents_test", CosmosDocument{
		ID:      "doc1",
		Account: "acct1",
		DB:      "db1",
		Coll:    "coll1",
		Body:    map[string]any{"id": "doc1", "team": "platform"},
		ETag:    `"5f-2a"`,
		RID:     "acct1-db1-coll1-doc1",
		TS:      1700000000,
	})
	if got.Account != "acct1" || got.DB != "db1" || got.Coll != "coll1" {
		t.Fatalf("document addressing dropped on reopen: %+v", got)
	}
	if got.Body["team"] != "platform" || got.ETag != `"5f-2a"` {
		t.Fatalf("document payload drifted on reopen: %+v", got)
	}

	// The wire shape must not leak the storage-only addressing fields.
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	for _, leaked := range []string{`"Account"`, `"DB"`, `"Coll"`} {
		if strings.Contains(string(wire), leaked) {
			t.Fatalf("wire shape leaks storage field %s: %s", leaked, wire)
		}
	}
}

// The script list and execute handlers filter on all four hidden fields.
func TestCosmosScriptPersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "cosmos_scripts_test", CosmosScript{
		Account: "acct1",
		DB:      "db1",
		Coll:    "coll1",
		Kind:    "sproc",
		ID:      "sp1",
		Body:    "function sp1() { }",
		ETag:    `"5f-2b"`,
	})
	if got.Account != "acct1" || got.DB != "db1" || got.Coll != "coll1" || got.Kind != "sproc" {
		t.Fatalf("script addressing dropped on reopen: %+v", got)
	}
	if got.Body != "function sp1() { }" {
		t.Fatalf("script body drifted on reopen: %+v", got)
	}
}

// The mounts stay off the site wire shape; their own config sub-resource
// serves them.
func TestSiteAzureStorageAccountsPersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "azf_sites_test", Site{
		ID:       "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app1",
		Name:     "app1",
		Type:     "Microsoft.Web/sites",
		Location: "eastus",
		Properties: SiteProperties{
			State:   "Running",
			Enabled: true,
		},
		AzureStorageAccounts: map[string]*AzureStorageInfoValue{
			"workspace": {
				Type:        "AzureFiles",
				AccountName: "stacct",
				ShareName:   "share1",
				AccessKey:   "key",
				MountPath:   "/mnt/workspace",
			},
		},
	})
	mount := got.AzureStorageAccounts["workspace"]
	if mount == nil || mount.ShareName != "share1" || mount.MountPath != "/mnt/workspace" {
		t.Fatalf("AzureStorageAccounts dropped on reopen: %+v", got.AzureStorageAccounts)
	}
	if got.Properties.State != "Running" {
		t.Fatalf("site properties drifted on reopen: %+v", got.Properties)
	}

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	if strings.Contains(string(wire), "AzureStorageAccounts") || strings.Contains(string(wire), "share1") {
		t.Fatalf("site wire shape leaks the storage mount dictionary: %s", wire)
	}
}

func TestFunctionEnvelopePersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "azf_function_configs_test", FunctionEnvelope{
		ID:           "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app1/functions/fn1",
		Name:         "app1/fn1",
		Type:         "Microsoft.Web/sites/functions",
		FunctionName: "fn1",
	})
	if got.FunctionName != "fn1" {
		t.Fatalf("FunctionName dropped on reopen: %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	if strings.Contains(string(wire), "FunctionName") {
		t.Fatalf("function wire shape leaks FunctionName: %s", wire)
	}
}

// The hub's runtime metadata reports CreatedAt; the ARM wire shape does not.
func TestEHEventHubPersistenceRoundTrip(t *testing.T) {
	created := time.Date(2026, 7, 1, 12, 30, 0, 0, time.UTC)
	got := storeReopenRoundTrip(t, "eh_eventhubs_test", EHEventHub{
		ID:        "/subscriptions/s/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/ns/eventhubs/hub1",
		Name:      "hub1",
		Type:      "Microsoft.EventHub/namespaces/eventhubs",
		CreatedAt: created,
	})
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt drifted on reopen: got %v, want %v", got.CreatedAt, created)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	if strings.Contains(string(wire), "CreatedAt") {
		t.Fatalf("event hub wire shape leaks CreatedAt: %s", wire)
	}
}

// Re-validation checks the DNS record kind the caller asked for, which the wire
// never carries.
func TestStaticSiteCustomDomainPersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "web_static_site_custom_domains_test", StaticSiteCustomDomainOverviewARMResource{
		ID:   "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/staticSites/x/customDomains/www.example.com",
		Name: "www.example.com",
		Type: "Microsoft.Web/staticSites/customDomains",
		Properties: StaticSiteCustomDomainOverviewARMResourceProperties{
			DomainName:      "www.example.com",
			Status:          "Validating",
			ValidationToken: "tok123",
		},
		ValidationMethod: "dns-txt-token",
	})
	if got.ValidationMethod != "dns-txt-token" {
		t.Fatalf("validation method dropped on reopen: %+v", got)
	}
	if got.Properties.Status != "Validating" || got.Properties.ValidationToken != "tok123" {
		t.Fatalf("domain payload drifted on reopen: %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	if strings.Contains(string(wire), "dns-txt-token") || strings.Contains(string(wire), "ValidationMethod") {
		t.Fatalf("wire shape leaks the validation method: %s", wire)
	}
}

// Azure keeps the password while reporting only secretState.
func TestStaticSiteBasicAuthPersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "web_static_site_basic_auth_test", StaticSiteBasicAuthPropertiesARMResource{
		ID:   "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/staticSites/x/basicAuth/default",
		Name: "default",
		Type: "Microsoft.Web/staticSites/basicAuth",
		Properties: StaticSiteBasicAuthPropertiesARMResourceProperties{
			ApplicableEnvironmentsMode: "AllEnvironments",
			SecretState:                "Password",
		},
		Password: "hunter2hunter2",
	})
	if got.Password != "hunter2hunter2" {
		t.Fatalf("password dropped on reopen: %+v", got)
	}
	if got.Properties.SecretState != "Password" {
		t.Fatalf("basic auth payload drifted on reopen: %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	if strings.Contains(string(wire), "hunter2hunter2") || strings.Contains(string(wire), "Password\":") {
		t.Fatalf("wire shape leaks the password: %s", wire)
	}
}

// App Service retains the uploaded PFX and password, which are x-ms-secret.
func TestWebCertificatePersistenceRoundTrip(t *testing.T) {
	valid := true
	got := storeReopenRoundTrip(t, "web_certificates_test", WebCertificate{
		ID:       "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/certificates/c1",
		Name:     "c1",
		Type:     "Microsoft.Web/certificates",
		Location: "eastus",
		Properties: WebCertificateProperties{
			SubjectName: "persist.example.com",
			Thumbprint:  "AABBCCDDEEFF00112233445566778899AABBCCDD",
			Valid:       &valid,
		},
		PfxBlob:  []byte{0x30, 0x82, 0x01, 0x02},
		Password: "pfx-secret",
	})
	if string(got.PfxBlob) != string([]byte{0x30, 0x82, 0x01, 0x02}) || got.Password != "pfx-secret" {
		t.Fatalf("certificate secret material dropped on reopen: %+v", got)
	}
	if got.Properties.Thumbprint != "AABBCCDDEEFF00112233445566778899AABBCCDD" {
		t.Fatalf("certificate payload drifted on reopen: %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal wire shape: %v", err)
	}
	for _, leaked := range []string{"pfx-secret", "pfxBlob", "password", "PfxBlob", "Password"} {
		if strings.Contains(string(wire), leaked) {
			t.Fatalf("wire shape leaks certificate secret material %q: %s", leaked, wire)
		}
	}
}
