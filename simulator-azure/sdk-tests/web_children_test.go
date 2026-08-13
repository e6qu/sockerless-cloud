package azure_sdk_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// web_children_test.go exercises the Microsoft.Web site child-resource
// families in simulator-azure/web_children.go, web_configrefs.go and the
// sitecontainers deployment-slot twins: public certificates, domain ownership
// identifiers, premier add-ons, config/pushsettings, the Key Vault config
// references derived from app settings / connection strings, and slot-scoped
// sitecontainers. Every resource is read back through GET and its list
// operation so the spec-shape validator sees the single and the collection
// response of each family.

// webChildrenSelfSignedDER returns a freshly generated self-signed
// certificate in DER form — the blob shape App Service public certificates
// carry — plus the SHA-1 thumbprint App Service derives from it.
func webChildrenSelfSignedDER(t *testing.T) ([]byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sdk-webchildren-cert"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	sum := sha1.Sum(der)
	return der, strings.ToUpper(hex.EncodeToString(sum[:]))
}

func webChildrenCreateSlot(t *testing.T, client *armappservice.WebAppsClient, rg, name, slot, planID string) {
	t.Helper()
	poller, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, slot, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig:   &armappservice.SiteConfig{},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestSDK_WebChildren_PublicCertificates(t *testing.T) {
	rg := "webchild-cert-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wc-cert-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "wc-cert-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)

	der, thumb := webChildrenSelfSignedDER(t)
	created, err := client.CreateOrUpdatePublicCertificate(ctx, rg, name, "cert1", armappservice.PublicCertificate{
		Properties: &armappservice.PublicCertificateProperties{
			Blob:                      der,
			PublicCertificateLocation: to.Ptr(armappservice.PublicCertificateLocationCurrentUserMy),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.Thumbprint)
	assert.Equal(t, thumb, *created.Properties.Thumbprint, "thumbprint must be the SHA-1 of the DER blob")

	got, err := client.GetPublicCertificate(ctx, rg, name, "cert1", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, der, got.Properties.Blob)

	found := false
	pager := client.NewListPublicCertificatesPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if c.Name != nil && *c.Name == "cert1" {
				found = true
			}
		}
	}
	assert.True(t, found, "certificate must appear in the list")

	// The slot keeps its own certificates: the production certificate is not
	// visible on the slot, and a slot-created one round-trips at the slot.
	slotDER, slotThumb := webChildrenSelfSignedDER(t)
	slotCreated, err := client.CreateOrUpdatePublicCertificateSlot(ctx, rg, name, "slotcert", slot, armappservice.PublicCertificate{
		Properties: &armappservice.PublicCertificateProperties{
			Blob:                      slotDER,
			PublicCertificateLocation: to.Ptr(armappservice.PublicCertificateLocationLocalMachineMy),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, slotCreated.Properties.Thumbprint)
	assert.Equal(t, slotThumb, *slotCreated.Properties.Thumbprint)

	slotGot, err := client.GetPublicCertificateSlot(ctx, rg, name, slot, "slotcert", nil)
	require.NoError(t, err)
	assert.Equal(t, slotDER, slotGot.Properties.Blob)

	slotNames := map[string]bool{}
	slotPager := client.NewListPublicCertificatesSlotPager(rg, name, slot, nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if c.Name != nil {
				slotNames[*c.Name] = true
			}
		}
	}
	assert.True(t, slotNames["slotcert"], "slot certificate must appear in the slot list")
	assert.False(t, slotNames["cert1"], "production certificate must not leak into the slot list")

	_, err = client.DeletePublicCertificateSlot(ctx, rg, name, slot, "slotcert", nil)
	require.NoError(t, err)
	_, err = client.DeletePublicCertificate(ctx, rg, name, "cert1", nil)
	require.NoError(t, err)
	_, err = client.GetPublicCertificate(ctx, rg, name, "cert1", nil)
	require.Error(t, err, "deleted certificate must not read back")
}

func TestSDK_WebChildren_DomainOwnershipIdentifiers(t *testing.T) {
	rg := "webchild-doi-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wc-doi-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "wc-doi-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)

	_, err = client.CreateOrUpdateDomainOwnershipIdentifier(ctx, rg, name, "doi1", armappservice.Identifier{
		Properties: &armappservice.IdentifierProperties{Value: to.Ptr("verify-token-1")},
	}, nil)
	require.NoError(t, err)

	got, err := client.GetDomainOwnershipIdentifier(ctx, rg, name, "doi1", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, "verify-token-1", *got.Properties.Value)

	// PATCH merges the identity value.
	upd, err := client.UpdateDomainOwnershipIdentifier(ctx, rg, name, "doi1", armappservice.Identifier{
		Properties: &armappservice.IdentifierProperties{Value: to.Ptr("verify-token-2")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "verify-token-2", *upd.Properties.Value)

	found := false
	pager := client.NewListDomainOwnershipIdentifiersPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			if d.Name != nil && *d.Name == "doi1" {
				found = true
			}
		}
	}
	assert.True(t, found, "identifier must appear in the list")

	// Slot twin round-trip.
	_, err = client.CreateOrUpdateDomainOwnershipIdentifierSlot(ctx, rg, name, "slotdoi", slot, armappservice.Identifier{
		Properties: &armappservice.IdentifierProperties{Value: to.Ptr("slot-token")},
	}, nil)
	require.NoError(t, err)
	slotGot, err := client.GetDomainOwnershipIdentifierSlot(ctx, rg, name, "slotdoi", slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "slot-token", *slotGot.Properties.Value)
	slotUpd, err := client.UpdateDomainOwnershipIdentifierSlot(ctx, rg, name, "slotdoi", slot, armappservice.Identifier{
		Properties: &armappservice.IdentifierProperties{Value: to.Ptr("slot-token-2")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "slot-token-2", *slotUpd.Properties.Value)
	slotSeen := false
	slotPager := client.NewListDomainOwnershipIdentifiersSlotPager(rg, name, slot, nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			if d.Name != nil && *d.Name == "slotdoi" {
				slotSeen = true
			}
		}
	}
	assert.True(t, slotSeen, "slot identifier must appear in the slot list")
	_, err = client.DeleteDomainOwnershipIdentifierSlot(ctx, rg, name, "slotdoi", slot, nil)
	require.NoError(t, err)
	_, err = client.DeleteDomainOwnershipIdentifier(ctx, rg, name, "doi1", nil)
	require.NoError(t, err)
}

func TestSDK_WebChildren_PremierAddOns(t *testing.T) {
	rg := "webchild-addon-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wc-addon-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "wc-addon-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)

	_, err = client.AddPremierAddOn(ctx, rg, name, "newrelic", armappservice.PremierAddOn{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.PremierAddOnProperties{
			SKU:     to.Ptr("standard"),
			Product: to.Ptr("NewRelicAPM"),
			Vendor:  to.Ptr("NewRelic"),
		},
	}, nil)
	require.NoError(t, err)

	got, err := client.GetPremierAddOn(ctx, rg, name, "newrelic", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, "NewRelicAPM", *got.Properties.Product)

	upd, err := client.UpdatePremierAddOn(ctx, rg, name, "newrelic", armappservice.PremierAddOnPatchResource{
		Properties: &armappservice.PremierAddOnPatchResourceProperties{SKU: to.Ptr("professional")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "professional", *upd.Properties.SKU)
	assert.Equal(t, "NewRelicAPM", *upd.Properties.Product, "PATCH must merge, not replace")

	// The list operation's declared shape is the single PremierAddOn resource.
	listed, err := client.ListPremierAddOns(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, listed.ID)
	assert.True(t, strings.HasSuffix(*listed.ID, "/premieraddons/newrelic"))

	// Slot twin round-trip.
	_, err = client.AddPremierAddOnSlot(ctx, rg, name, "slotaddon", slot, armappservice.PremierAddOn{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.PremierAddOnProperties{
			SKU: to.Ptr("free"), Product: to.Ptr("SlotProduct"), Vendor: to.Ptr("SlotVendor"),
		},
	}, nil)
	require.NoError(t, err)
	slotGot, err := client.GetPremierAddOnSlot(ctx, rg, name, "slotaddon", slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "SlotProduct", *slotGot.Properties.Product)
	slotUpd, err := client.UpdatePremierAddOnSlot(ctx, rg, name, "slotaddon", slot, armappservice.PremierAddOnPatchResource{
		Properties: &armappservice.PremierAddOnPatchResourceProperties{Vendor: to.Ptr("SlotVendor2")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "SlotVendor2", *slotUpd.Properties.Vendor)
	slotListed, err := client.ListPremierAddOnsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	require.NotNil(t, slotListed.ID)
	assert.Contains(t, *slotListed.ID, "/slots/"+slot+"/premieraddons/slotaddon")
	_, err = client.DeletePremierAddOnSlot(ctx, rg, name, "slotaddon", slot, nil)
	require.NoError(t, err)
	_, err = client.DeletePremierAddOn(ctx, rg, name, "newrelic", nil)
	require.NoError(t, err)
	_, err = client.GetPremierAddOn(ctx, rg, name, "newrelic", nil)
	require.Error(t, err, "deleted premier add-on must not read back")
}

func TestSDK_WebChildren_PushSettings(t *testing.T) {
	rg := "webchild-push-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wc-push-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "wc-push-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)

	// Never-configured push reports the disabled default.
	def, err := client.ListSitePushSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, def.Properties)
	assert.False(t, *def.Properties.IsPushEnabled)

	upd, err := client.UpdateSitePushSettings(ctx, rg, name, armappservice.PushSettings{
		Properties: &armappservice.PushSettingsProperties{
			IsPushEnabled:    to.Ptr(true),
			TagWhitelistJSON: to.Ptr(`["tag1"]`),
		},
	}, nil)
	require.NoError(t, err)
	assert.True(t, *upd.Properties.IsPushEnabled)

	listed, err := client.ListSitePushSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.True(t, *listed.Properties.IsPushEnabled)
	assert.Equal(t, `["tag1"]`, *listed.Properties.TagWhitelistJSON)

	// The slot's push settings are independent of the production site's.
	slotDef, err := client.ListSitePushSettingsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.False(t, *slotDef.Properties.IsPushEnabled, "slot must not inherit the production push settings")
	_, err = client.UpdateSitePushSettingsSlot(ctx, rg, name, slot, armappservice.PushSettings{
		Properties: &armappservice.PushSettingsProperties{IsPushEnabled: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	slotListed, err := client.ListSitePushSettingsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.True(t, *slotListed.Properties.IsPushEnabled)
}

func TestSDK_WebChildren_ConfigKeyVaultReferences(t *testing.T) {
	rg := "webchild-kvref-rg"
	vault := "wc-kvref-vault"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wc-kvref-plan")
	createKVViaARM(t, rg, vault)

	// A real secret with one version, written through the Key Vault data
	// plane, is what the references must resolve against.
	secrets, err := azsecrets.NewClient(kvVaultURL(vault), &fakeCredential{},
		&azsecrets.ClientOptions{
			ClientOptions:                        kvClientOptions(),
			DisableChallengeResourceVerification: true,
		})
	require.NoError(t, err)
	setResp, err := secrets.SetSecret(ctx, "db-password", azsecrets.SetSecretParameters{Value: to.Ptr("hunter2")}, nil)
	require.NoError(t, err)
	secretVersion := setResp.ID.Version()

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "wc-kvref-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)

	resolvedRef := "@Microsoft.KeyVault(SecretUri=https://" + vault + ".vault.azure.net/secrets/db-password)"
	_, err = client.UpdateApplicationSettings(ctx, rg, name, armappservice.StringDictionary{
		Properties: map[string]*string{
			"DB_PASSWORD":  to.Ptr(resolvedRef),
			"GONE_VAULT":   to.Ptr("@Microsoft.KeyVault(VaultName=no-such-vault;SecretName=whatever)"),
			"GONE_SECRET":  to.Ptr("@Microsoft.KeyVault(VaultName=" + vault + ";SecretName=missing)"),
			"PLAIN_STRING": to.Ptr("not-a-reference"),
		},
	}, nil)
	require.NoError(t, err)

	single, err := client.GetAppSettingKeyVaultReference(ctx, rg, name, "DB_PASSWORD", nil)
	require.NoError(t, err)
	require.NotNil(t, single.Properties)
	assert.Equal(t, armappservice.ResolveStatusResolved, *single.Properties.Status)
	assert.Equal(t, vault, *single.Properties.VaultName)
	assert.Equal(t, "db-password", *single.Properties.SecretName)
	require.NotNil(t, single.Properties.ActiveVersion)
	assert.Equal(t, secretVersion, *single.Properties.ActiveVersion,
		"activeVersion must be the secret's current version in Key Vault")

	byKey := map[string]armappservice.ResolveStatus{}
	pager := client.NewGetAppSettingsKeyVaultReferencesPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, ref := range page.Value {
			require.NotNil(t, ref.Properties)
			byKey[*ref.Name] = *ref.Properties.Status
		}
	}
	assert.Equal(t, armappservice.ResolveStatusResolved, byKey["DB_PASSWORD"])
	assert.Equal(t, armappservice.ResolveStatusVaultNotFound, byKey["GONE_VAULT"])
	assert.Equal(t, armappservice.ResolveStatusSecretNotFound, byKey["GONE_SECRET"])
	_, plainListed := byKey["PLAIN_STRING"]
	assert.False(t, plainListed, "a non-reference app setting is not a config reference")

	// A key that is not a Key Vault reference 404s on the single read.
	_, err = client.GetAppSettingKeyVaultReference(ctx, rg, name, "PLAIN_STRING", nil)
	require.Error(t, err)

	// Connection-string references derive from the connection-string store.
	_, err = client.UpdateConnectionStrings(ctx, rg, name, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{
			"MainDb": {Value: to.Ptr(resolvedRef), Type: to.Ptr(armappservice.ConnectionStringTypeSQLAzure)},
			"Plain":  {Value: to.Ptr("Server=tcp:example"), Type: to.Ptr(armappservice.ConnectionStringTypeSQLAzure)},
		},
	}, nil)
	require.NoError(t, err)

	connSingle, err := client.GetSiteConnectionStringKeyVaultReference(ctx, rg, name, "MainDb", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ResolveStatusResolved, *connSingle.Properties.Status)

	connKeys := map[string]bool{}
	connPager := client.NewGetSiteConnectionStringKeyVaultReferencesPager(rg, name, nil)
	for connPager.More() {
		page, err := connPager.NextPage(ctx)
		require.NoError(t, err)
		for _, ref := range page.Value {
			connKeys[*ref.Name] = true
		}
	}
	assert.True(t, connKeys["MainDb"])
	assert.False(t, connKeys["Plain"])

	// Slot references read the slot's own app settings.
	_, err = client.UpdateApplicationSettingsSlot(ctx, rg, name, slot, armappservice.StringDictionary{
		Properties: map[string]*string{"SLOT_SECRET": to.Ptr(resolvedRef)},
	}, nil)
	require.NoError(t, err)
	slotSingle, err := client.GetAppSettingKeyVaultReferenceSlot(ctx, rg, name, "SLOT_SECRET", slot, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ResolveStatusResolved, *slotSingle.Properties.Status)
	slotSeen := map[string]bool{}
	slotPager := client.NewGetAppSettingsKeyVaultReferencesSlotPager(rg, name, slot, nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		for _, ref := range page.Value {
			slotSeen[*ref.Name] = true
		}
	}
	assert.True(t, slotSeen["SLOT_SECRET"])
	assert.False(t, slotSeen["DB_PASSWORD"], "production references must not leak into the slot")
}

func TestSDK_WebChildren_SiteContainersSlot(t *testing.T) {
	rg := "webchild-sc-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wc-sc-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "wc-sc-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)

	created, err := client.CreateOrUpdateSiteContainerSlot(ctx, rg, name, slot, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:  to.Ptr("registry.example/app:staging"),
			IsMain: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "registry.example/app:staging", *created.Properties.Image)
	require.NotNil(t, created.Type)
	assert.Equal(t, "Microsoft.Web/sites/slots/sitecontainers", *created.Type)

	got, err := client.GetSiteContainerSlot(ctx, rg, name, slot, "main", nil)
	require.NoError(t, err)
	assert.True(t, *got.Properties.IsMain)

	slotSeen := false
	pager := client.NewListSiteContainersSlotPager(rg, name, slot, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if c.Name != nil && *c.Name == "main" {
				slotSeen = true
			}
		}
	}
	assert.True(t, slotSeen, "slot sitecontainer must appear in the slot list")

	// The production site's sitecontainer list must not include the slot's.
	prodPager := client.NewListSiteContainersPager(rg, name, nil)
	for prodPager.More() {
		page, err := prodPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value, "slot sitecontainers must not leak into the production list")
	}

	_, err = client.DeleteSiteContainerSlot(ctx, rg, name, slot, "main", nil)
	require.NoError(t, err)
	_, err = client.GetSiteContainerSlot(ctx, rg, name, slot, "main", nil)
	require.Error(t, err, "deleted slot sitecontainer must not read back")
}
