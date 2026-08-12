package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webChildrenAPIVersion addresses the Microsoft.Web child-resource families
// (publicCertificates, domainOwnershipIdentifiers, premieraddons,
// config/pushsettings, config/configreferences, workflows).
const webChildrenAPIVersion = "2024-11-01"

func webChildURL(resourcePath string) string {
	return armURL("Microsoft.Web", resourcePath, webChildrenAPIVersion)
}

// webChildrenCertDER is a self-signed X.509 certificate in DER form, base64
// encoded — the blob an App Service public certificate carries. Its SHA-1
// thumbprint is webChildrenCertThumb.
const webChildrenCertDER = "MIIBODCB36ADAgECAgEBMAoGCCqGSM49BAMCMCUxIzAhBgNVBAMTGnNvY2tlcmxlc3Mtc2ltLXB1YmxpYy1jZXJ0MCAXDTI2MDEwMTAwMDAwMFoYDzIxMjYwMTAxMDAwMDAwWjAlMSMwIQYDVQQDExpzb2NrZXJsZXNzLXNpbS1wdWJsaWMtY2VydDBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABIYJeLik0TOyqjYwfwDiqAkGzAbpepEjv4QKPOTTI7OH598l1LPkSwMNslhaMITB4V3UHtod+Y5SmGM1nJeCdZEwCgYIKoZIzj0EAwIDSAAwRQIhALwq3le+aDLA2FJZr4hYiJvJ1PNpq5PUlNdrn4t6XY9CAiBQKGsAY1MrTtkXxK0tuItu4cbF85xw7RQajq5/ubpXGg=="

const webChildrenCertThumb = "67A203A735B08E10D349C6E91F178233EEA9F8AB"

// TestWebChildren_CLI drives the Microsoft.Web site child-resource families
// through `az rest`: public certificates, domain ownership identifiers,
// premier add-ons, push settings, and the Key Vault config references derived
// from app settings — each on the production site, plus a slot twin check.
func TestWebChildren_CLI(t *testing.T) {
	runCLI(t, azRest("PUT", webChildURL("serverfarms/cli-wc-plan"),
		`{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`))
	runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site"),
		`{"location":"eastus","kind":"functionapp","properties":{}}`))
	runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site/slots/staging"),
		`{"location":"eastus","kind":"functionapp"}`))
	defer func() {
		runCLI(t, azRest("DELETE", webChildURL("sites/cli-wc-site/slots/staging"), ""))
		runCLI(t, azRest("DELETE", webChildURL("sites/cli-wc-site"), ""))
		runCLI(t, azRest("DELETE", webChildURL("serverfarms/cli-wc-plan"), ""))
	}()

	// Public certificate: PUT derives the SHA-1 thumbprint from the DER blob.
	var cert struct {
		Properties struct {
			Thumbprint string `json:"thumbprint"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site/publicCertificates/cli-cert"),
		`{"properties":{"blob":"`+webChildrenCertDER+`","publicCertificateLocation":"CurrentUserMy"}}`)), &cert)
	assert.Equal(t, webChildrenCertThumb, cert.Properties.Thumbprint)
	runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/publicCertificates/cli-cert"), ""))
	var certList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/publicCertificates"), "")), &certList)
	require.Len(t, certList.Value, 1)
	assert.Equal(t, "cli-cert", certList.Value[0].Name)
	runCLI(t, azRest("DELETE", webChildURL("sites/cli-wc-site/publicCertificates/cli-cert"), ""))

	// Domain ownership identifier round-trip, PATCH merge included.
	runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site/domainOwnershipIdentifiers/cli-doi"),
		`{"properties":{"id":"token-1"}}`))
	var doi struct {
		Properties struct {
			ID string `json:"id"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PATCH", webChildURL("sites/cli-wc-site/domainOwnershipIdentifiers/cli-doi"),
		`{"properties":{"id":"token-2"}}`)), &doi)
	assert.Equal(t, "token-2", doi.Properties.ID)
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/domainOwnershipIdentifiers/cli-doi"), "")), &doi)
	assert.Equal(t, "token-2", doi.Properties.ID)
	runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/domainOwnershipIdentifiers"), ""))
	runCLI(t, azRest("DELETE", webChildURL("sites/cli-wc-site/domainOwnershipIdentifiers/cli-doi"), ""))

	// Premier add-on round-trip; the collection GET is the single resource.
	runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site/premieraddons/cli-addon"),
		`{"location":"eastus","properties":{"sku":"standard","product":"NewRelicAPM","vendor":"NewRelic"}}`))
	var addon struct {
		Name       string `json:"name"`
		Properties struct {
			Sku     string `json:"sku"`
			Product string `json:"product"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PATCH", webChildURL("sites/cli-wc-site/premieraddons/cli-addon"),
		`{"properties":{"sku":"professional"}}`)), &addon)
	assert.Equal(t, "professional", addon.Properties.Sku)
	assert.Equal(t, "NewRelicAPM", addon.Properties.Product, "PATCH must merge, not replace")
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/premieraddons"), "")), &addon)
	assert.Equal(t, "cli-addon", addon.Name)
	runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/premieraddons/cli-addon"), ""))
	runCLI(t, azRest("DELETE", webChildURL("sites/cli-wc-site/premieraddons/cli-addon"), ""))

	// Push settings: PUT then POST /list; the slot keeps its own record.
	var push struct {
		Properties struct {
			IsPushEnabled bool `json:"isPushEnabled"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site/config/pushsettings"),
		`{"properties":{"isPushEnabled":true,"tagWhitelistJson":"[\"t1\"]"}}`)), &push)
	assert.True(t, push.Properties.IsPushEnabled)
	parseJSON(t, runCLI(t, azRest("POST", webChildURL("sites/cli-wc-site/config/pushsettings/list"), "")), &push)
	assert.True(t, push.Properties.IsPushEnabled)
	parseJSON(t, runCLI(t, azRest("POST", webChildURL("sites/cli-wc-site/slots/staging/config/pushsettings/list"), "")), &push)
	assert.False(t, push.Properties.IsPushEnabled, "slot must not inherit the production push settings")

	// Key Vault config references, derived from the app settings: a
	// reference against a real (empty) vault resolves to SecretNotFound, a
	// reference naming no existing vault to VaultNotFound.
	vaultURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/cli-wc-vault?api-version=2024-04-01-preview",
		baseURL, subscriptionID, resourceGroup)
	runCLI(t, azRest("PUT", vaultURL, `{"location":"eastus","properties":{"tenantId":"00000000-0000-0000-0000-000000000000"}}`))
	defer runCLI(t, azRest("DELETE", vaultURL, ""))
	runCLI(t, azRest("PUT", webChildURL("sites/cli-wc-site/config/appsettings"),
		`{"properties":{"NO_SECRET":"@Microsoft.KeyVault(VaultName=cli-wc-vault;SecretName=absent)","NO_VAULT":"@Microsoft.KeyVault(SecretUri=https://no-such-vault.vault.azure.net/secrets/x)","PLAIN":"value"}}`))
	var ref struct {
		Properties struct {
			Status    string `json:"status"`
			VaultName string `json:"vaultName"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/config/configreferences/appsettings/NO_SECRET"), "")), &ref)
	assert.Equal(t, "SecretNotFound", ref.Properties.Status)
	assert.Equal(t, "cli-wc-vault", ref.Properties.VaultName)
	var refs struct {
		Value []struct {
			Name       string `json:"name"`
			Properties struct {
				Status string `json:"status"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/config/configreferences/appsettings"), "")), &refs)
	statuses := map[string]string{}
	for _, v := range refs.Value {
		statuses[v.Name] = v.Properties.Status
	}
	assert.Equal(t, "SecretNotFound", statuses["NO_SECRET"])
	assert.Equal(t, "VaultNotFound", statuses["NO_VAULT"])
	_, plainListed := statuses["PLAIN"]
	assert.False(t, plainListed, "a non-reference app setting is not a config reference")
	runCLI(t, azRest("GET", webChildURL("sites/cli-wc-site/config/configreferences/connectionstrings"), ""))
}

// TestWebWorkflows_CLI drives the App-Service-hosted Logic Apps workflow
// surface through `az rest`: deployWorkflowArtifacts, the workflow envelope
// reads, listWorkflowsConnections, and the hostruntime bridge (trigger run,
// runs, trigger histories, versions).
func TestWebWorkflows_CLI(t *testing.T) {
	runCLI(t, azRest("PUT", webChildURL("serverfarms/cli-wf-plan"),
		`{"location":"eastus","sku":{"name":"WS1","tier":"WorkflowStandard"}}`))
	runCLI(t, azRest("PUT", webChildURL("sites/cli-wf-site"),
		`{"location":"eastus","kind":"functionapp,workflowapp","properties":{}}`))
	defer func() {
		runCLI(t, azRest("DELETE", webChildURL("sites/cli-wf-site"), ""))
		runCLI(t, azRest("DELETE", webChildURL("serverfarms/cli-wf-plan"), ""))
	}()

	runCLI(t, azRest("POST", webChildURL("sites/cli-wf-site/deployWorkflowArtifacts"),
		`{"files":{"cliwf/workflow.json":{"kind":"Stateful","definition":{"triggers":{"manual":{"type":"Request"}},"actions":{"reply":{"type":"Response"}}}},"connections.json":{"managedApiConnections":{}}}}`))

	// Envelope reads.
	var envList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wf-site/workflows"), "")), &envList)
	require.Len(t, envList.Value, 1)
	assert.Equal(t, "cliwf", envList.Value[0].Name)
	var env struct {
		Properties struct {
			FlowState string         `json:"flowState"`
			Files     map[string]any `json:"files"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL("sites/cli-wf-site/workflows/cliwf"), "")), &env)
	assert.Equal(t, "Enabled", env.Properties.FlowState)
	assert.Contains(t, env.Properties.Files, "workflow.json")
	parseJSON(t, runCLI(t, azRest("POST", webChildURL("sites/cli-wf-site/listWorkflowsConnections"), "")), &env)
	assert.Contains(t, env.Properties.Files, "connections.json")

	// Hostruntime bridge: fire the manual trigger, then read the run, its
	// actions, the trigger history, and the version snapshot.
	hostruntime := "sites/cli-wf-site/hostruntime/runtime/webhooks/workflow/api/management/workflows/cliwf"
	runCLI(t, azRest("GET", webChildURL(hostruntime+"/triggers"), ""))
	runCLI(t, azRest("POST", webChildURL(hostruntime+"/triggers/manual/run"), ""))
	var runList struct {
		Value []struct {
			Name       string `json:"name"`
			Properties struct {
				Status string `json:"status"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL(hostruntime+"/runs"), "")), &runList)
	require.Len(t, runList.Value, 1)
	assert.Equal(t, "Succeeded", runList.Value[0].Properties.Status)
	runCLI(t, azRest("GET", webChildURL(hostruntime+"/runs/"+runList.Value[0].Name), ""))
	var actList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL(hostruntime+"/runs/"+runList.Value[0].Name+"/actions"), "")), &actList)
	require.Len(t, actList.Value, 1)
	assert.Equal(t, "reply", actList.Value[0].Name)
	var histList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL(hostruntime+"/triggers/manual/histories"), "")), &histList)
	require.Len(t, histList.Value, 1)
	var verList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webChildURL(hostruntime+"/versions"), "")), &verList)
	require.Len(t, verList.Value, 1)
	runCLI(t, azRest("GET", webChildURL(hostruntime+"/versions/"+verList.Value[0].Name), ""))
	runCLI(t, azRest("POST", webChildURL(hostruntime+"/regenerateAccessKey"), `{"keyType":"Primary"}`))
}
