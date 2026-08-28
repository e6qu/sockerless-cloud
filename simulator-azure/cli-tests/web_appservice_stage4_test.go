package azure_cli_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// CLI coverage for the App Service certificate, hostname-truth, config
// snapshot, container-log and provider-singleton surfaces. The native az
// commands (az webapp config ssl upload/list/show, az webapp config hostname
// add/list/delete, az resource move) run against a TLS simulator through the
// CLI's own login flow; the operations no az command wraps go through az
// rest. Routes exercised:
//
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/certificates
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/certificates/{name}
//	GET+PUT+PATCH+DELETE /sites/{siteName}/certificates/{certificateName} (+ slot)
//	GET /sites/{siteName}/certificates (+ slot)
//	GET /sites/{siteName}/analyzeCustomHostname (+ slot)
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/customhostnameSites
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/listSitesAssignedToHostName
//	GET /sites/{siteName}/config/web/snapshots (+ slot)
//	GET /sites/{siteName}/config/web/snapshots/{snapshotId} (+ slot)
//	POST /sites/{siteName}/config/web/snapshots/{snapshotId}/recover (+ slot)
//	POST /sites/{siteName}/containerlogs (+ slot)
//	POST /sites/{siteName}/containerlogs/zip/download (+ slot)
//	POST /sites/{siteName}/updatemachinekey
//	GET /providers/Microsoft.Web/operations
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/validate
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/verifyHostingEnvironmentVnet
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/usages
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/billingMeters
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/aseRegions
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/premieraddonoffers
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/operations/{operationId}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources

const stage4CLIAPIVersion = "2025-03-01"

// stage4CLISelfSignedPFX issues a self-signed certificate and returns the
// leaf plus its PKCS#12 archive — the same payload a real `az webapp config
// ssl upload` reads from disk.
func stage4CLISelfSignedPFX(t *testing.T, commonName, password string) (*x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              []string{commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
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

func stage4CLIThumbprint(leaf *x509.Certificate) string {
	sum := sha1.Sum(leaf.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// TestWebAppStage4_NativeSSLHostnameAndMove drives the certificate and
// hostname surfaces through the native az commands and moves the site
// between resource groups with `az resource move`.
func TestWebAppStage4_NativeSSLHostnameAndMove(t *testing.T) {
	env := startAzLoginSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-stage4",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-stage4"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	rg, app := "stage4-cli-rg", "stage4-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	planURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/stage4-cli-plan?api-version=%s",
		env.baseURL, subscriptionID, rg, stage4CLIAPIVersion)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", planURL,
		"--body", `{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`, "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=%s",
		env.baseURL, subscriptionID, rg, app, stage4CLIAPIVersion)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(
		`{"location":"eastus","kind":"app,linux","properties":{"serverFarmId":"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/stage4-cli-plan"}}`,
		subscriptionID, rg), "-o", "json"))

	leaf, pfx := stage4CLISelfSignedPFX(t, "cli.stage4.example.com", "s4-cli-pass")
	pfxPath := filepath.Join(t.TempDir(), "stage4.pfx")
	require.NoError(t, os.WriteFile(pfxPath, pfx, 0o600))

	var uploaded struct {
		Name        string `json:"name"`
		Thumbprint  string `json:"thumbprint"`
		SubjectName string `json:"subjectName"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "config", "ssl", "upload",
		"-g", rg, "-n", app,
		"--certificate-file", pfxPath, "--certificate-password", "s4-cli-pass",
		"-o", "json")), &uploaded)
	assert.Equal(t, stage4CLIThumbprint(leaf), uploaded.Thumbprint,
		"the simulator derives the same SHA-1 thumbprint az computed locally")
	require.NotEmpty(t, uploaded.Name)

	var listed []struct {
		Thumbprint string `json:"thumbprint"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "config", "ssl", "list", "-g", rg, "-o", "json")), &listed)
	foundCert := false
	for _, c := range listed {
		if c.Thumbprint == stage4CLIThumbprint(leaf) {
			foundCert = true
		}
	}
	assert.True(t, foundCert, "uploaded certificate must appear in ssl list")

	var shown struct {
		SubjectName string   `json:"subjectName"`
		HostNames   []string `json:"hostNames"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "config", "ssl", "show",
		"-g", rg, "--certificate-name", uploaded.Name, "-o", "json")), &shown)
	assert.Equal(t, "cli.stage4.example.com", shown.SubjectName)
	assert.Contains(t, shown.HostNames, "cli.stage4.example.com")

	host := "cli.stage4.example.com"
	runCLI(t, env.command("webapp", "config", "hostname", "add",
		"-g", rg, "--webapp-name", app, "--hostname", host, "-o", "json"))
	var hostnames []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "config", "hostname", "list",
		"-g", rg, "--webapp-name", app, "-o", "json")), &hostnames)
	foundHost := false
	for _, h := range hostnames {
		if strings.Contains(h.Name, host) {
			foundHost = true
		}
	}
	assert.True(t, foundHost, "added hostname must appear in hostname list")
	runCLI(t, env.command("webapp", "config", "hostname", "delete",
		"-g", rg, "--webapp-name", app, "--hostname", host))

	dstRG := "stage4-cli-moved-rg"
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	siteID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", subscriptionID, rg, app)
	planID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/stage4-cli-plan", subscriptionID, rg)
	runCLI(t, env.command("resource", "move", "--ids", siteID, planID, "--destination-group", dstRG))

	movedURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=%s",
		env.baseURL, subscriptionID, dstRG, app, stage4CLIAPIVersion)
	var moved struct {
		ID         string `json:"id"`
		Properties struct {
			ServerFarmID string `json:"serverFarmId"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("rest", "--method", "GET", "--url", movedURL, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/", "the site moved into the destination group")
	assert.Contains(t, moved.Properties.ServerFarmID, "/resourceGroups/"+dstRG+"/",
		"the moved plan reference follows the move")
}

// TestWebAppStage4_RestSurfaces drives the site-scoped certificates, the
// hostname analyzers, the configuration snapshots and the provider
// singletons through az rest.
func TestWebAppStage4_RestSurfaces(t *testing.T) {
	planBody := `{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`
	runCLI(t, azRest("PUT", webMoreURL("serverfarms/cli-s4-plan"), planBody))
	siteBody := fmt.Sprintf(
		`{"location":"eastus","kind":"functionapp","properties":{"serverFarmId":"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/cli-s4-plan"}}`,
		subscriptionID, resourceGroup)
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site"), siteBody))
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/slots/staging"), `{"location":"eastus","kind":"functionapp"}`))

	leaf, pfx := stage4CLISelfSignedPFX(t, "rest.stage4.example.com", "s4-rest-pass")
	certBody := fmt.Sprintf(`{"location":"eastus","properties":{"pfxBlob":"%s","password":"s4-rest-pass"}}`,
		base64.StdEncoding.EncodeToString(pfx))

	var siteCert struct {
		Properties struct {
			Thumbprint  string `json:"thumbprint"`
			SubjectName string `json:"subjectName"`
			PfxBlob     string `json:"pfxBlob"`
			Password    string `json:"password"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/certificates/s4-site-cert"), certBody)), &siteCert)
	assert.Equal(t, stage4CLIThumbprint(leaf), siteCert.Properties.Thumbprint)
	assert.Equal(t, "rest.stage4.example.com", siteCert.Properties.SubjectName)
	assert.Empty(t, siteCert.Properties.PfxBlob, "pfxBlob is write-only")
	assert.Empty(t, siteCert.Properties.Password, "password is write-only")

	runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/certificates/s4-site-cert"), ""))
	var siteCerts struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/certificates"), "")), &siteCerts)
	require.Len(t, siteCerts.Value, 1)
	assert.Equal(t, "s4-site-cert", siteCerts.Value[0].Name)
	runCLI(t, azRest("PATCH", webMoreURL("sites/cli-s4-site/certificates/s4-site-cert"),
		`{"properties":{"hostNames":["rest.stage4.example.com","alt.stage4.example.com"]}}`))

	// Slot spelling.
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/slots/staging/certificates/s4-slot-cert"), certBody))
	runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/slots/staging/certificates"), ""))
	runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/slots/staging/certificates/s4-slot-cert"), ""))
	runCLI(t, azRest("PATCH", webMoreURL("sites/cli-s4-site/slots/staging/certificates/s4-slot-cert"),
		`{"properties":{"hostNames":["slot.stage4.example.com"]}}`))
	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-s4-site/slots/staging/certificates/s4-slot-cert"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-s4-site/certificates/s4-site-cert"), ""))

	var rgCert struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", webMoreURL("certificates/cli-s4-cert"), certBody)), &rgCert)
	require.NotEmpty(t, rgCert.ID)
	runCLI(t, azRest("GET", webMoreURL("certificates/cli-s4-cert"), ""))
	runCLI(t, azRest("GET", webMoreURL("certificates"), ""))
	subCerts := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/certificates?api-version=%s",
		baseURL, subscriptionID, stage4CLIAPIVersion)
	runCLI(t, azRest("GET", subCerts, ""))
	runCLI(t, azRest("PATCH", webMoreURL("certificates/cli-s4-cert"),
		`{"properties":{"hostNames":["patched.stage4.example.com"]}}`))
	runCLI(t, azRest("DELETE", webMoreURL("certificates/cli-s4-cert"), ""))

	// The ownership proof lives in the simulator's Azure DNS: a CNAME pointing
	// the custom hostname at the site's default hostname.
	dnsZoneURL := armURL("Microsoft.Network", "dnsZones/cli-stage4.test", "2018-05-01")
	runCLI(t, azRest("PUT", dnsZoneURL, `{"location":"global"}`))
	dnsRecordURL := armURL("Microsoft.Network", "dnsZones/cli-stage4.test/CNAME/app", "2018-05-01")
	runCLI(t, azRest("PUT", dnsRecordURL,
		`{"properties":{"TTL":300,"CNAMERecord":{"cname":"cli-s4-site.azurewebsites.net"}}}`))

	var analysis struct {
		Properties struct {
			CNameRecords                 []string `json:"cNameRecords"`
			CustomDomainVerificationTest string   `json:"customDomainVerificationTest"`
		} `json:"properties"`
	}
	analyzeURL := webMoreURL("sites/cli-s4-site/analyzeCustomHostname") + "&hostName=app.cli-stage4.test"
	parseJSON(t, runCLI(t, azRest("GET", analyzeURL, "")), &analysis)
	require.Len(t, analysis.Properties.CNameRecords, 1)
	assert.Equal(t, "cli-s4-site.azurewebsites.net", analysis.Properties.CNameRecords[0])
	assert.Equal(t, "Passed", analysis.Properties.CustomDomainVerificationTest)

	slotAnalyzeURL := webMoreURL("sites/cli-s4-site/slots/staging/analyzeCustomHostname") + "&hostName=app.cli-stage4.test"
	runCLI(t, azRest("GET", slotAnalyzeURL, ""))

	// Bind the hostname, then read the two global hostname surfaces.
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/hostNameBindings/app.cli-stage4.test"),
		`{"properties":{"hostNameType":"Verified"}}`))
	customHostsURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/customhostnameSites?api-version=%s",
		baseURL, subscriptionID, stage4CLIAPIVersion)
	var customHosts struct {
		Value []struct {
			Properties struct {
				CustomHostname  string `json:"customHostname"`
				SiteResourceIDs []struct {
					ID string `json:"id"`
				} `json:"siteResourceIds"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", customHostsURL, "")), &customHosts)
	foundHost := false
	for _, row := range customHosts.Value {
		if row.Properties.CustomHostname == "app.cli-stage4.test" {
			foundHost = true
			require.NotEmpty(t, row.Properties.SiteResourceIDs)
		}
	}
	assert.True(t, foundHost, "bound hostname must appear in customhostnameSites")

	assignedURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/listSitesAssignedToHostName?api-version=%s",
		baseURL, subscriptionID, stage4CLIAPIVersion)
	var assigned struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("POST", assignedURL, `{"name":"app.cli-stage4.test"}`)), &assigned)
	require.Len(t, assigned.Value, 1)
	assert.Equal(t, "cli-s4-site", assigned.Value[0].Name)

	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/config/web"),
		`{"properties":{"linuxFxVersion":"DOCKER|registry.example/app:v1"}}`))
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/config/web"),
		`{"properties":{"linuxFxVersion":"DOCKER|registry.example/app:v2"}}`))
	var snapshots struct {
		Value []struct {
			Name       string `json:"name"`
			Properties struct {
				SnapshotID int    `json:"snapshotId"`
				Time       string `json:"time"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/config/web/snapshots"), "")), &snapshots)
	require.GreaterOrEqual(t, len(snapshots.Value), 2)
	firstSnap := snapshots.Value[len(snapshots.Value)-2].Name

	var snapCfg struct {
		Properties struct {
			LinuxFxVersion string `json:"linuxFxVersion"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/config/web/snapshots/"+firstSnap), "")), &snapCfg)
	assert.Equal(t, "DOCKER|registry.example/app:v1", snapCfg.Properties.LinuxFxVersion)

	runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/config/web/snapshots/"+firstSnap+"/recover"), ""))
	var liveCfg struct {
		Properties struct {
			LinuxFxVersion string `json:"linuxFxVersion"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/config/web"), "")), &liveCfg)
	assert.Equal(t, "DOCKER|registry.example/app:v1", liveCfg.Properties.LinuxFxVersion)

	// Slot spellings.
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-s4-site/slots/staging/config/web"),
		`{"properties":{"linuxFxVersion":"DOCKER|registry.example/app:slot1"}}`))
	var slotSnaps struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/slots/staging/config/web/snapshots"), "")), &slotSnaps)
	require.NotEmpty(t, slotSnaps.Value)
	slotSnap := slotSnaps.Value[len(slotSnaps.Value)-1].Name
	runCLI(t, azRest("GET", webMoreURL("sites/cli-s4-site/slots/staging/config/web/snapshots/"+slotSnap), ""))
	runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/slots/staging/config/web/snapshots/"+slotSnap+"/recover"), ""))

	assert.Empty(t, strings.TrimSpace(runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/containerlogs"), ""))))
	assert.Empty(t, strings.TrimSpace(runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/containerlogs/zip/download"), ""))))
	assert.Empty(t, strings.TrimSpace(runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/slots/staging/containerlogs"), ""))))
	assert.Empty(t, strings.TrimSpace(runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/slots/staging/containerlogs/zip/download"), ""))))

	runCLI(t, azRest("POST", webMoreURL("sites/cli-s4-site/updatemachinekey"), ""))

	opsURL := fmt.Sprintf("%s/providers/Microsoft.Web/operations?api-version=%s", baseURL, stage4CLIAPIVersion)
	var ops struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", opsURL, "")), &ops)
	require.NotEmpty(t, ops.Value)

	var validate struct {
		Status string `json:"status"`
	}
	validateURL := webMoreURL("validate")
	parseJSON(t, runCLI(t, azRest("POST", validateURL, fmt.Sprintf(
		`{"name":"cli-s4-validate","type":"Site","location":"eastus","properties":{"serverFarmId":"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/cli-s4-plan"}}`,
		subscriptionID, resourceGroup))), &validate)
	assert.Equal(t, "Success", validate.Status)

	verifyVnetURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/verifyHostingEnvironmentVnet?api-version=%s",
		baseURL, subscriptionID, stage4CLIAPIVersion)
	var vnetCheck struct {
		Properties struct {
			Failed bool `json:"failed"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", verifyVnetURL,
		`{"properties":{"vnetResourceGroup":"missing-rg","vnetName":"missing-vnet","vnetSubnetName":"missing-subnet"}}`)), &vnetCheck)
	assert.True(t, vnetCheck.Properties.Failed, "a subnet the simulator does not hold fails the check")

	for _, path := range []string{"billingMeters", "aseRegions", "premieraddonoffers", "locations/eastus/usages"} {
		u := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/%s?api-version=%s",
			baseURL, subscriptionID, path, stage4CLIAPIVersion)
		var col struct {
			Value []any `json:"value"`
		}
		parseJSON(t, runCLI(t, azRest("GET", u, "")), &col)
		assert.Empty(t, col.Value, path+" is truthfully empty in the simulator")
	}

	// Cleanup.
	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-s4-site/hostNameBindings/app.cli-stage4.test"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-s4-site/slots/staging"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-s4-site"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("serverfarms/cli-s4-plan"), ""))
	runCLI(t, azRest("DELETE", dnsRecordURL, ""))
	runCLI(t, azRest("DELETE", dnsZoneURL, ""))
}
