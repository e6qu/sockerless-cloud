package azure_sdk_test

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// SDK coverage for the App Service certificate, hostname-truth, config
// snapshot, container-log and provider-singleton surfaces:
//
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/certificates
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/certificates/{name}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/certificates
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/analyzeCustomHostname (+ slot)
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/customhostnameSites
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/listSitesAssignedToHostName
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/web/snapshots (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/web/snapshots/{snapshotId} (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/web/snapshots/{snapshotId}/recover (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/containerlogs (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/containerlogs/zip/download (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/updatemachinekey
//	GET /providers/Microsoft.Web/operations
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/validate
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/verifyHostingEnvironmentVnet
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/usages
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/billingMeters
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/aseRegions
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/premieraddonoffers
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}/snapshots
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/deletedSites/{deletedSiteId}
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/operations/{operationId}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources

// stage4SelfSignedCert issues a fresh self-signed certificate for the given
// hostnames and returns the parsed certificate together with its PKCS#12
// archive protected by password — the exact payload `az webapp config ssl
// upload` and terraform-provider-azurerm send.
func stage4SelfSignedCert(t *testing.T, commonName string, dnsNames []string, password string) (*x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              dnsNames,
		NotBefore:             time.Now().Add(-time.Hour).Truncate(time.Second),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour).Truncate(time.Second),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pfx, err := pkcs12.Modern.Encode(key, leaf, nil, password)
	require.NoError(t, err)
	return leaf, pfx
}

func stage4Thumbprint(leaf *x509.Certificate) string {
	sum := sha1.Sum(leaf.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestSDK_WebCertificates_PfxLifecycle(t *testing.T) {
	rg := "stage4-cert-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "s4-cert-plan")

	client, err := armappservice.NewCertificatesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	leaf, pfx := stage4SelfSignedCert(t, "stage4.example.com",
		[]string{"stage4.example.com", "www.stage4.example.com"}, "s4-pass")

	created, err := client.CreateOrUpdate(ctx, rg, "s4-cert", armappservice.AppCertificate{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.AppCertificateProperties{
			PfxBlob:      pfx,
			Password:     to.Ptr("s4-pass"),
			ServerFarmID: to.Ptr(planID),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)

	// Every derived member comes from the real certificate.
	require.NotNil(t, created.Properties.Thumbprint)
	assert.Equal(t, stage4Thumbprint(leaf), *created.Properties.Thumbprint)
	require.NotNil(t, created.Properties.SubjectName)
	assert.Equal(t, "stage4.example.com", *created.Properties.SubjectName)
	require.NotNil(t, created.Properties.Issuer)
	assert.Equal(t, "stage4.example.com", *created.Properties.Issuer, "self-signed: issuer == subject")
	require.NotNil(t, created.Properties.IssueDate)
	assert.WithinDuration(t, leaf.NotBefore, *created.Properties.IssueDate, time.Second)
	require.NotNil(t, created.Properties.ExpirationDate)
	assert.WithinDuration(t, leaf.NotAfter, *created.Properties.ExpirationDate, time.Second)
	require.NotNil(t, created.Properties.Valid)
	assert.True(t, *created.Properties.Valid)
	hostNames := make([]string, 0, len(created.Properties.HostNames))
	for _, h := range created.Properties.HostNames {
		hostNames = append(hostNames, *h)
	}
	assert.ElementsMatch(t, []string{"stage4.example.com", "www.stage4.example.com"}, hostNames)
	// pfxBlob and password are x-ms-secret: write-only, never returned.
	assert.Nil(t, created.Properties.PfxBlob, "pfxBlob is write-only")
	assert.Nil(t, created.Properties.Password, "password is write-only")

	certID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/certificates/s4-cert"

	got, err := client.Get(ctx, rg, "s4-cert", nil)
	require.NoError(t, err)
	require.NotNil(t, got.ID)
	assert.Equal(t, certID, *got.ID)
	assert.Equal(t, stage4Thumbprint(leaf), *got.Properties.Thumbprint)

	// List by resource group and by subscription.
	foundRG, foundSub := false, false
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if c.ID != nil && *c.ID == certID {
				foundRG = true
			}
		}
	}
	subPager := client.NewListPager(nil)
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if c.ID != nil && *c.ID == certID {
				foundSub = true
			}
		}
	}
	assert.True(t, foundRG, "certificate must appear in list-by-resource-group")
	assert.True(t, foundSub, "certificate must appear in list-by-subscription")

	// PATCH with a fresh payload re-derives the members.
	leaf2, pfx2 := stage4SelfSignedCert(t, "patched.example.com", []string{"patched.example.com"}, "p2")
	patched, err := client.Update(ctx, rg, "s4-cert", armappservice.AppCertificatePatchResource{
		Properties: &armappservice.AppCertificatePatchResourceProperties{
			PfxBlob:  pfx2,
			Password: to.Ptr("p2"),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, patched.Properties.Thumbprint)
	assert.Equal(t, stage4Thumbprint(leaf2), *patched.Properties.Thumbprint)

	// A wrong PFX password is a parse failure, not a stored certificate.
	_, err = client.CreateOrUpdate(ctx, rg, "s4-badpass", armappservice.AppCertificate{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.AppCertificateProperties{
			PfxBlob:  pfx,
			Password: to.Ptr("wrong"),
		},
	}, nil)
	require.Error(t, err, "wrong PFX password must be rejected")

	// Delete: 200 for an existing certificate, 204 for an absent one — the
	// SDK treats both as success; the follow-up Get proves it is gone.
	_, err = client.Delete(ctx, rg, "s4-cert", nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "s4-cert", nil)
	require.Error(t, err, "deleted certificate must 404")
	_, err = client.Delete(ctx, rg, "s4-cert", nil)
	require.NoError(t, err, "deleting an absent certificate answers 204")
}

// stage4CreateVault creates a Key Vault through the ARM control plane,
// optionally granting an access policy with the secret verbs App Service
// needs to pull certificate material.
func stage4CreateVault(t *testing.T, rg, vault string, secretPerms []string) string {
	t.Helper()
	props := map[string]any{
		"tenantId": "00000000-0000-0000-0000-000000000000",
	}
	if len(secretPerms) > 0 {
		props["accessPolicies"] = []map[string]any{{
			"tenantId":    "00000000-0000-0000-0000-000000000000",
			"objectId":    "11111111-2222-3333-4444-555555555555",
			"permissions": map[string]any{"secrets": secretPerms},
		}}
	}
	body, _ := json.Marshal(map[string]any{"location": "eastus", "properties": props})
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+
			"/providers/Microsoft.KeyVault/vaults/"+vault+"?api-version=2024-04-01-preview",
		bytes.NewReader(body))
	req.Header.Set("Authorization", simARMBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "ARM vault create must succeed")
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.KeyVault/vaults/" + vault
}

// stage4SetVaultSecret writes a secret through the Key Vault data plane —
// the coordinate real certificate imports land on.
func stage4SetVaultSecret(t *testing.T, vault, name, value string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"value": value, "contentType": "application/x-pkcs12"})
	u := baseURL + "/secrets/" + name + "?api-version=7.4"
	req, _ := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	req.Host = vault + ".vault." + strings.TrimPrefix(baseURL, "http://")
	req.Header.Set("Authorization", simARMBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "Key Vault set-secret must succeed")
}

func TestSDK_WebCertificates_KeyVaultSourced(t *testing.T) {
	rg := "stage4-kvcert-rg"
	ensureRG(t, rg)

	client, err := armappservice.NewCertificatesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	leaf, pfx := stage4SelfSignedCert(t, "kv.stage4.example.com", []string{"kv.stage4.example.com"}, "")
	vaultID := stage4CreateVault(t, rg, "s4kvcertok", []string{"Get", "List"})
	stage4SetVaultSecret(t, "s4kvcertok", "s4-cert-secret", base64.StdEncoding.EncodeToString(pfx))

	status := func(c armappservice.CertificatesClientCreateOrUpdateResponse) armappservice.KeyVaultSecretStatus {
		require.NotNil(t, c.Properties)
		require.NotNil(t, c.Properties.KeyVaultSecretStatus)
		return *c.Properties.KeyVaultSecretStatus
	}

	// Secret resolves and parses → Succeeded, with the real thumbprint.
	ok, err := client.CreateOrUpdate(ctx, rg, "s4-kv-ok", armappservice.AppCertificate{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.AppCertificateProperties{
			KeyVaultID:         to.Ptr(vaultID),
			KeyVaultSecretName: to.Ptr("s4-cert-secret"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.KeyVaultSecretStatusSucceeded, status(ok))
	require.NotNil(t, ok.Properties.Thumbprint)
	assert.Equal(t, stage4Thumbprint(leaf), *ok.Properties.Thumbprint)

	// Vault exists, secret does not → KeyVaultSecretDoesNotExist.
	noSecret, err := client.CreateOrUpdate(ctx, rg, "s4-kv-nosecret", armappservice.AppCertificate{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.AppCertificateProperties{
			KeyVaultID:         to.Ptr(vaultID),
			KeyVaultSecretName: to.Ptr("absent-secret"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.KeyVaultSecretStatusKeyVaultSecretDoesNotExist, status(noSecret))

	// Vault grants no secret "get" permission → OperationNotPermittedOnKeyVault.
	lockedID := stage4CreateVault(t, rg, "s4kvcertlocked", nil)
	locked, err := client.CreateOrUpdate(ctx, rg, "s4-kv-locked", armappservice.AppCertificate{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.AppCertificateProperties{
			KeyVaultID:         to.Ptr(lockedID),
			KeyVaultSecretName: to.Ptr("s4-cert-secret"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.KeyVaultSecretStatusOperationNotPermittedOnKeyVault, status(locked))

	// No vault at that resource ID → KeyVaultDoesNotExist.
	missing, err := client.CreateOrUpdate(ctx, rg, "s4-kv-missing", armappservice.AppCertificate{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.AppCertificateProperties{
			KeyVaultID:         to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.KeyVault/vaults/never-created"),
			KeyVaultSecretName: to.Ptr("s4-cert-secret"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.KeyVaultSecretStatusKeyVaultDoesNotExist, status(missing))
}

func TestSDK_WebApps_HostnameTruth(t *testing.T) {
	rg := "stage4-host-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "s4-host-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webMoreCreateSite(t, client, rg, "s4-host-site", planID)
	webMoreCreateSite(t, client, rg, "s4-host-other", planID)

	// Publish the ownership proof in the simulator's Azure DNS: a zone with
	// a CNAME pointing the custom hostname at the site's default hostname.
	zonesClient, err := armdns.NewZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = zonesClient.CreateOrUpdate(ctx, rg, "stage4-zone.test", armdns.Zone{Location: to.Ptr("global")}, nil)
	require.NoError(t, err)
	recordsClient, err := armdns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = recordsClient.CreateOrUpdate(ctx, rg, "stage4-zone.test", "app", armdns.RecordTypeCNAME, armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL:         to.Ptr[int64](300),
			CnameRecord: &armdns.CnameRecord{Cname: to.Ptr("s4-host-site.azurewebsites.net")},
		},
	}, nil)
	require.NoError(t, err)

	host := "app.stage4-zone.test"

	// The CNAME analysis reads the record set for real: target listed,
	// verification test passed.
	analysis, err := client.AnalyzeCustomHostname(ctx, rg, "s4-host-site",
		&armappservice.WebAppsClientAnalyzeCustomHostnameOptions{HostName: to.Ptr(host)})
	require.NoError(t, err)
	require.NotNil(t, analysis.Properties)
	require.Len(t, analysis.Properties.CNameRecords, 1)
	assert.Equal(t, "s4-host-site.azurewebsites.net", *analysis.Properties.CNameRecords[0])
	require.NotNil(t, analysis.Properties.CustomDomainVerificationTest)
	assert.Equal(t, armappservice.DNSVerificationTestResultPassed, *analysis.Properties.CustomDomainVerificationTest)
	require.NotNil(t, analysis.Properties.IsHostnameAlreadyVerified)
	assert.False(t, *analysis.Properties.IsHostnameAlreadyVerified)

	// A hostname without any DNS record fails the verification test.
	unproven, err := client.AnalyzeCustomHostname(ctx, rg, "s4-host-site",
		&armappservice.WebAppsClientAnalyzeCustomHostnameOptions{HostName: to.Ptr("nowhere.stage4-zone.test")})
	require.NoError(t, err)
	require.NotNil(t, unproven.Properties.CustomDomainVerificationTest)
	assert.Equal(t, armappservice.DNSVerificationTestResultFailed, *unproven.Properties.CustomDomainVerificationTest)
	require.NotNil(t, unproven.Properties.CustomDomainVerificationFailureInfo)

	// Bind the hostname to the site, then re-analyze: the binding owner sees
	// isHostnameAlreadyVerified, the other site sees the conflict.
	_, err = client.CreateOrUpdateHostNameBinding(ctx, rg, "s4-host-site", host, armappservice.HostNameBinding{
		Properties: &armappservice.HostNameBindingProperties{
			CustomHostNameDNSRecordType: to.Ptr(armappservice.CustomHostNameDNSRecordTypeCName),
			HostNameType:                to.Ptr(armappservice.HostNameTypeVerified),
		},
	}, nil)
	require.NoError(t, err)

	bound, err := client.AnalyzeCustomHostname(ctx, rg, "s4-host-site",
		&armappservice.WebAppsClientAnalyzeCustomHostnameOptions{HostName: to.Ptr(host)})
	require.NoError(t, err)
	require.NotNil(t, bound.Properties.IsHostnameAlreadyVerified)
	assert.True(t, *bound.Properties.IsHostnameAlreadyVerified)

	conflicted, err := client.AnalyzeCustomHostname(ctx, rg, "s4-host-other",
		&armappservice.WebAppsClientAnalyzeCustomHostnameOptions{HostName: to.Ptr(host)})
	require.NoError(t, err)
	require.NotNil(t, conflicted.Properties.HasConflictOnScaleUnit)
	assert.True(t, *conflicted.Properties.HasConflictOnScaleUnit)
	require.NotNil(t, conflicted.Properties.ConflictingAppResourceID)
	assert.Contains(t, *conflicted.Properties.ConflictingAppResourceID, "s4-host-site")

	// Slot spelling.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, "s4-host-site", "staging", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotAnalysis, err := client.AnalyzeCustomHostnameSlot(ctx, rg, "s4-host-site", "staging",
		&armappservice.WebAppsClientAnalyzeCustomHostnameSlotOptions{HostName: to.Ptr(host)})
	require.NoError(t, err)
	require.NotNil(t, slotAnalysis.Properties)

	// ListCustomHostNameSites assembles the binding truth per hostname.
	mgmt, err := armappservice.NewWebSiteManagementClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	foundHost := false
	chPager := mgmt.NewListCustomHostNameSitesPager(nil)
	for chPager.More() {
		page, err := chPager.NextPage(ctx)
		require.NoError(t, err)
		for _, row := range page.Value {
			if row.Properties == nil || row.Properties.CustomHostname == nil {
				continue
			}
			if strings.EqualFold(*row.Properties.CustomHostname, host) {
				foundHost = true
				require.NotEmpty(t, row.Properties.SiteResourceIDs)
				require.NotNil(t, row.Properties.SiteResourceIDs[0].ID)
				assert.Contains(t, *row.Properties.SiteResourceIDs[0].ID, "s4-host-site")
			}
		}
	}
	assert.True(t, foundHost, "bound hostname must appear in customhostnameSites")

	// ListSiteIdentifiersAssignedToHostName names the assigned site.
	idPager := mgmt.NewListSiteIdentifiersAssignedToHostNamePager(armappservice.NameIdentifier{Name: to.Ptr(host)}, nil)
	foundSite := false
	for idPager.More() {
		page, err := idPager.NextPage(ctx)
		require.NoError(t, err)
		for _, ident := range page.Value {
			if ident.Name != nil && *ident.Name == "s4-host-site" {
				foundSite = true
			}
		}
	}
	assert.True(t, foundSite, "site must be listed as assigned to the hostname")
}

func TestSDK_WebApps_ConfigSnapshotsAndRecover(t *testing.T) {
	rg := "stage4-snap-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "s4-snap-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webMoreCreateSite(t, client, rg, "s4-snap-site", planID)

	putConfig := func(fx string) {
		_, err := client.CreateOrUpdateConfiguration(ctx, rg, "s4-snap-site", armappservice.SiteConfigResource{
			Properties: &armappservice.SiteConfig{LinuxFxVersion: to.Ptr(fx)},
		}, nil)
		require.NoError(t, err)
	}
	putConfig("DOCKER|registry.example/app:v1")
	putConfig("DOCKER|registry.example/app:v2")

	// Every config/web update wrote a snapshot.
	var snapshots []*armappservice.SiteConfigurationSnapshotInfo
	pager := client.NewListConfigurationSnapshotInfoPager(rg, "s4-snap-site", nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		snapshots = append(snapshots, page.Value...)
	}
	require.GreaterOrEqual(t, len(snapshots), 2, "each config update writes a snapshot")
	first := snapshots[len(snapshots)-2]
	require.NotNil(t, first.Properties)
	require.NotNil(t, first.Properties.SnapshotID)
	require.NotNil(t, first.Properties.Time)
	firstID := *first.Name

	// The snapshot body is the exact stored config of that update.
	snap, err := client.GetConfigurationSnapshot(ctx, rg, "s4-snap-site", firstID, nil)
	require.NoError(t, err)
	require.NotNil(t, snap.Properties)
	require.NotNil(t, snap.Properties.LinuxFxVersion)
	assert.Equal(t, "DOCKER|registry.example/app:v1", *snap.Properties.LinuxFxVersion)

	// Recover restores that exact config into the live site.
	_, err = client.RecoverSiteConfigurationSnapshot(ctx, rg, "s4-snap-site", firstID, nil)
	require.NoError(t, err)
	live, err := client.GetConfiguration(ctx, rg, "s4-snap-site", nil)
	require.NoError(t, err)
	require.NotNil(t, live.Properties.LinuxFxVersion)
	assert.Equal(t, "DOCKER|registry.example/app:v1", *live.Properties.LinuxFxVersion)

	// Slot spellings walk the same history on the slot resource.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, "s4-snap-site", "staging", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.CreateOrUpdateConfigurationSlot(ctx, rg, "s4-snap-site", "staging", armappservice.SiteConfigResource{
		Properties: &armappservice.SiteConfig{LinuxFxVersion: to.Ptr("DOCKER|registry.example/app:slot1")},
	}, nil)
	require.NoError(t, err)
	var slotSnaps []*armappservice.SiteConfigurationSnapshotInfo
	slotPager := client.NewListConfigurationSnapshotInfoSlotPager(rg, "s4-snap-site", "staging", nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		slotSnaps = append(slotSnaps, page.Value...)
	}
	require.NotEmpty(t, slotSnaps, "slot config update writes a slot snapshot")
	slotSnapID := *slotSnaps[len(slotSnaps)-1].Name
	slotSnap, err := client.GetConfigurationSnapshotSlot(ctx, rg, "s4-snap-site", slotSnapID, "staging", nil)
	require.NoError(t, err)
	assert.Equal(t, "DOCKER|registry.example/app:slot1", *slotSnap.Properties.LinuxFxVersion)
	_, err = client.RecoverSiteConfigurationSnapshotSlot(ctx, rg, "s4-snap-site", slotSnapID, "staging", nil)
	require.NoError(t, err)
}

func TestSDK_WebApps_ContainerLogs(t *testing.T) {
	rg := "stage4-logs-rg"
	name := "s4-logs-site"
	azureCreateSiteWithImage(t, rg, name, []string{"echo", "hello-from-stage4"}, "public.ecr.aws/docker/library/alpine:latest")
	t.Cleanup(func() { azureDeleteSite(rg, name) })

	// Run the site container for real; its output flows through the site's
	// log sink into the retained container log.
	azureInvokeFunction(t, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	logsResp, err := client.GetWebSiteContainerLogs(ctx, rg, name, nil)
	require.NoError(t, err)
	logText, err := io.ReadAll(logsResp.Body)
	require.NoError(t, err)
	logsResp.Body.Close()
	require.NotEmpty(t, logText, "an invoked site has retained container log lines")
	assert.Contains(t, string(logText), "hello-from-stage4",
		"the log text carries the container's real stdout")

	// The zip spelling carries the same log content in one docker log file.
	zipResp, err := client.GetContainerLogsZip(ctx, rg, name, nil)
	require.NoError(t, err)
	zipBytes, err := io.ReadAll(zipResp.Body)
	require.NoError(t, err)
	zipResp.Body.Close()
	require.NotEmpty(t, zipBytes)
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)
	require.Len(t, zr.File, 1)
	assert.Contains(t, zr.File[0].Name, "_docker.log")
	f, err := zr.File[0].Open()
	require.NoError(t, err)
	zipped, err := io.ReadAll(f)
	require.NoError(t, err)
	f.Close()
	assert.Equal(t, logText, zipped, "zip content is the same retained log text")

	// A site whose container never ran has no log content: 204.
	quietRG := "stage4-logs-quiet-rg"
	azureCreateSiteWithImage(t, quietRG, "s4-logs-quiet", nil, evalImageName)
	t.Cleanup(func() { azureDeleteSite(quietRG, "s4-logs-quiet") })
	quiet, err := client.GetWebSiteContainerLogs(ctx, quietRG, "s4-logs-quiet", nil)
	require.NoError(t, err)
	quietBody, err := io.ReadAll(quiet.Body)
	require.NoError(t, err)
	quiet.Body.Close()
	assert.Empty(t, quietBody, "a never-run container has no logs (204)")
}

func TestSDK_Web_ProviderAndGlobalSingletons(t *testing.T) {
	rg := "stage4-glob-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "s4-glob-plan")

	// Provider_ListOperations — the Microsoft.Web operation catalog.
	provider, err := armappservice.NewProviderClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	var ops []*armappservice.CsmOperationDescription
	opsPager := provider.NewListOperationsPager(nil)
	for opsPager.More() {
		page, err := opsPager.NextPage(ctx)
		require.NoError(t, err)
		ops = append(ops, page.Value...)
	}
	require.NotEmpty(t, ops)
	names := make([]string, 0, len(ops))
	for _, op := range ops {
		require.NotNil(t, op.Name)
		names = append(names, *op.Name)
	}
	assert.Contains(t, names, "Microsoft.Web/sites/Read")
	assert.Contains(t, names, "Microsoft.Web/certificates/Write")

	mgmt, err := armappservice.NewWebSiteManagementClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// The catalog surfaces the simulator genuinely hosts nothing of are
	// truthfully empty collections.
	bmPager := mgmt.NewListBillingMetersPager(nil)
	for bmPager.More() {
		page, err := bmPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value, "the simulator meters no billing")
	}
	asePager := mgmt.NewListAseRegionsPager(nil)
	for asePager.More() {
		page, err := asePager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value, "the simulator hosts no App Service Environments")
	}
	offerPager := mgmt.NewListPremierAddOnOffersPager(nil)
	for offerPager.More() {
		page, err := offerPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value, "no marketplace premier add-on offers exist in the simulator")
	}
	usages, err := armappservice.NewGetUsagesInLocationClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	usagePager := usages.NewListPager("eastus", nil)
	for usagePager.More() {
		page, err := usagePager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value, "the simulator enforces no location quotas")
	}

	// Validate — against the real stores: an existing plan validates a Site,
	// a missing plan fails it.
	valid, err := mgmt.Validate(ctx, rg, armappservice.ValidateRequest{
		Name:       to.Ptr("s4-validate-site"),
		Type:       to.Ptr(armappservice.ValidateResourceTypesSite),
		Location:   to.Ptr("eastus"),
		Properties: &armappservice.ValidateProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, valid.Status)
	assert.Equal(t, "Success", *valid.Status)

	invalid, err := mgmt.Validate(ctx, rg, armappservice.ValidateRequest{
		Name:     to.Ptr("s4-validate-site"),
		Type:     to.Ptr(armappservice.ValidateResourceTypesSite),
		Location: to.Ptr("eastus"),
		Properties: &armappservice.ValidateProperties{
			ServerFarmID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverfarms/never-created"),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, invalid.Status)
	assert.Equal(t, "Failure", *invalid.Status)
	require.NotNil(t, invalid.Error)
	require.NotNil(t, invalid.Error.Code)
	assert.Equal(t, "ServerFarmNotFound", *invalid.Error.Code)

	// VerifyHostingEnvironmentVnet — a subnet the Microsoft.Network store
	// does not hold fails the check truthfully.
	vnetCheck, err := mgmt.VerifyHostingEnvironmentVnet(ctx, armappservice.VnetParameters{
		Properties: &armappservice.VnetParametersProperties{
			VnetResourceGroup: to.Ptr(rg),
			VnetName:          to.Ptr("s4-missing-vnet"),
			VnetSubnetName:    to.Ptr("s4-missing-subnet"),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, vnetCheck.Properties)
	require.NotNil(t, vnetCheck.Properties.Failed)
	assert.True(t, *vnetCheck.Properties.Failed)
	assert.NotEmpty(t, vnetCheck.Properties.FailedTests)

	// UpdateMachineKey — resets the verified site's machine key.
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webMoreCreateSite(t, client, rg, "s4-machinekey-site", planID)
	_, err = client.UpdateMachineKey(ctx, rg, "s4-machinekey-site", nil)
	require.NoError(t, err)

	// Deleted-web-app reads: the simulator hard-deletes sites, so every
	// deletedSiteId is truthfully absent.
	global, err := armappservice.NewGlobalClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = global.GetDeletedWebApp(ctx, "12345", nil)
	require.Error(t, err, "no deleted web app exists in the simulator")
	_, err = global.GetDeletedWebAppSnapshots(ctx, "12345", nil)
	require.Error(t, err)
	deleted, err := armappservice.NewDeletedWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = deleted.GetDeletedWebAppByLocation(ctx, "eastus", "12345", nil)
	require.Error(t, err)
}

func TestSDK_Web_MoveResources(t *testing.T) {
	srcRG, dstRG := "stage4-move-src-rg", "stage4-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)
	planID := webMoreEnsurePlan(t, srcRG, "s4-move-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webMoreCreateSite(t, client, srcRG, "s4-move-site", planID)
	siteID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG + "/providers/Microsoft.Web/sites/s4-move-site"

	mgmt, err := armappservice.NewWebSiteManagementClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	envelope := armappservice.CsmMoveResourceEnvelope{
		TargetResourceGroup: to.Ptr(dstRG),
		Resources:           []*string{to.Ptr(siteID), to.Ptr(planID)},
	}

	// ValidateMove runs the move's checks without moving anything.
	_, err = mgmt.ValidateMove(ctx, srcRG, envelope, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, srcRG, "s4-move-site", nil)
	require.NoError(t, err, "validate-move must not move the site")

	// Move re-homes the site (and its plan) into the target resource group.
	_, err = mgmt.Move(ctx, srcRG, envelope, nil)
	require.NoError(t, err)

	moved, err := client.Get(ctx, dstRG, "s4-move-site", nil)
	require.NoError(t, err, "moved site must appear under the target resource group")
	_, err = client.Get(ctx, srcRG, "s4-move-site", nil)
	require.Error(t, err, "moved site must no longer resolve under the source resource group")
	require.NotNil(t, moved.Properties.ServerFarmID)
	assert.Contains(t, *moved.Properties.ServerFarmID, "/resourceGroups/"+dstRG+"/",
		"the moved plan reference follows the move")

	// Moving a resource the store does not hold refuses with the ARM error.
	_, err = mgmt.ValidateMove(ctx, srcRG, armappservice.CsmMoveResourceEnvelope{
		TargetResourceGroup: to.Ptr(dstRG),
		Resources:           []*string{to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG + "/providers/Microsoft.Web/sites/never-created")},
	}, nil)
	require.Error(t, err, "validating a move of an absent resource must fail")
}

func TestSDK_Web_GetSubscriptionOperation(t *testing.T) {
	rg := "stage4-op-rg"
	ensureRG(t, rg)

	// Run a real long-running operation and capture the operation record it
	// issued: an Event Hubs namespace create answers 202 with an
	// Azure-AsyncOperation URL whose last segment is the operation ID.
	nsClient, err := armeventhub.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	var createRaw *http.Response
	createCtx := policy.WithCaptureResponse(ctx, &createRaw)
	poller, err := nsClient.BeginCreateOrUpdate(createCtx, rg, "s4-op-ns", armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armeventhub.SKU{
			Name: to.Ptr(armeventhub.SKUNameStandard),
			Tier: to.Ptr(armeventhub.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createRaw)
	asyncURL := createRaw.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, asyncURL, "the namespace create answers with an Azure-AsyncOperation header")
	parsed, err := url.Parse(asyncURL)
	require.NoError(t, err)
	segs := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	opID := segs[len(segs)-1]

	// Global_GetSubscriptionOperationWithAsyncResponse: 204 for an operation
	// the service issued, 404 for one it never did.
	global, err := armappservice.NewGlobalClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = global.GetSubscriptionOperationWithAsyncResponse(ctx, "eastus", opID, nil)
	require.NoError(t, err, "an operation the service issued answers 204")
	_, err = global.GetSubscriptionOperationWithAsyncResponse(ctx, "eastus", "00000000-dead-beef-0000-000000000000", nil)
	require.Error(t, err, "an operation the service never issued answers 404")
}
