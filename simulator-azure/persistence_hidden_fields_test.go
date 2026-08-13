package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// storeReopenRoundTrip persists one record into a fresh SQLite-backed store,
// closes the database, reopens the same directory, and returns the record read
// back — the exact lifecycle a SIM_PERSIST=true simulator restart puts every
// stored resource through. Hidden (json:"-") fields ride the persistence
// sidecar, so a plain JSON round-trip cannot stand in for this check.
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

// TestCosmosDocumentPersistenceRoundTrip asserts a stored Cosmos document
// keeps its Account/DB/Coll addressing across a store reopen. cosmosDocsFor
// filters on those fields, so dropping them makes every persisted document
// unreachable by QUERY (and list) after a restart while point reads still
// work — the worst kind of silent data loss.
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

// TestCosmosScriptPersistenceRoundTrip asserts stored procedures / UDFs /
// triggers keep their container addressing and kind across a store reopen —
// the script list and execute handlers filter on all four hidden fields.
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

// TestCosmosOfferPersistenceRoundTrip asserts a throughput offer keeps its
// Account across a store reopen — cosmosOfferFor keys offers by account+rid.
func TestCosmosOfferPersistenceRoundTrip(t *testing.T) {
	got := storeReopenRoundTrip(t, "cosmos_offers_test", cosmosOffer{
		Account:         "acct1",
		OfferID:         "offer1",
		RID:             "offer1rid",
		OfferResourceID: "collrid",
		OfferVersion:    "V2",
		Content:         map[string]any{"offerThroughput": float64(400)},
	})
	if got.Account != "acct1" {
		t.Fatalf("offer Account dropped on reopen: %+v", got)
	}
	if got.Content["offerThroughput"] != float64(400) {
		t.Fatalf("offer content drifted on reopen: %+v", got)
	}
}

// TestSiteAzureStorageAccountsPersistenceRoundTrip asserts a site's Azure
// Files mount dictionary survives a store reopen while staying off the
// Microsoft.Web site wire shape (its own config sub-resource serves it).
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

// TestFunctionEnvelopePersistenceRoundTrip asserts the internal short function
// name survives a store reopen without appearing on the wire.
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

// TestEHEventHubPersistenceRoundTrip asserts an Event Hub's CreatedAt survives
// a store reopen — the hub's runtime metadata reports it and it is not part of
// the ARM wire shape.
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

// TestStaticSiteCustomDomainPersistenceRoundTrip asserts a custom domain
// keeps its write-only validation method across a store reopen — the
// re-validation on later reads checks the DNS record kind the caller asked
// for — while the wire shape never carries it.
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

// TestStaticSiteBasicAuthPersistenceRoundTrip asserts the basic-auth password
// survives a store reopen through the persistence sidecar — real Azure keeps
// the configured password while only reporting secretState — and never
// appears on the wire.
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

// TestWebCertificatePersistenceRoundTrip asserts an App Service certificate's
// uploaded PFX archive and password survive a store reopen through the
// persistence sidecar — real App Service retains the uploaded material while
// pfxBlob and password stay x-ms-secret — and never appear on the wire.
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
