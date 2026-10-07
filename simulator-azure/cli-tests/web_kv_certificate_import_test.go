package azure_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for importing a Key Vault certificate into App Service with
// `az webapp config ssl import`. App Service reads the certificate's secret as
// its first-party service principal, "Microsoft Azure App Service", which an
// operator grants with `az keyvault set-policy --spn <its application ID>`.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/certificates/{name}
//	GET /v1.0/servicePrincipals?$filter=servicePrincipalNames/any(c:c eq '{appId}')
func TestWebAppSSLImport_CLI_AuthorizesAppServicePrincipal(t *testing.T) {
	const appServiceAppID = "abfa0a7c-a6b6-4736-8310-5855508787cd"
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-ssl-import")

	rg, app, vault := "ssl-import-cli-rg", "ssl-import-cli-app", "sslimportclivault"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	planURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/ssl-import-cli-plan?api-version=%s",
		env.baseURL, subscriptionID, rg, stage4CLIAPIVersion)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", planURL,
		"--body", `{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`, "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=%s",
		env.baseURL, subscriptionID, rg, app, stage4CLIAPIVersion)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(
		`{"location":"eastus","kind":"app,linux","properties":{"serverFarmId":"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/ssl-import-cli-plan"}}`,
		subscriptionID, rg), "-o", "json"))

	runCLI(t, env.command("keyvault", "create", "-n", vault, "-g", rg, "-l", "eastus",
		"--sku", "standard", "--enable-rbac-authorization", "false", "-o", "json"))
	leaf, pfx := stage4CLISelfSignedPFX(t, "import.cli.example.com", "")
	body, err := json.Marshal(map[string]string{"value": base64.StdEncoding.EncodeToString(pfx), "contentType": "application/x-pkcs12"})
	require.NoError(t, err)
	runCLI(t, kvMoveDataPlane(env, vault, "PUT", "/secrets/site-cert", string(body)))

	type imported struct {
		KeyVaultSecretStatus string `json:"keyVaultSecretStatus"`
		Thumbprint           string `json:"thumbprint"`
	}
	importCert := func(name string) imported {
		t.Helper()
		var out imported
		parseJSON(t, runCLI(t, env.command("webapp", "config", "ssl", "import", "-g", rg, "-n", app,
			"--key-vault", vault, "--key-vault-certificate-name", "site-cert", "--certificate-name", name, "-o", "json")), &out)
		return out
	}

	// The vault's creator can read the secret; App Service cannot until the
	// vault grants its service principal.
	locked := importCert("ssl-import-locked")
	assert.Equal(t, "OperationNotPermittedOnKeyVault", locked.KeyVaultSecretStatus)

	runCLI(t, env.command("keyvault", "set-policy", "-n", vault, "-g", rg,
		"--spn", appServiceAppID, "--secret-permissions", "get", "-o", "json"))
	granted := importCert("ssl-import-granted")
	assert.Equal(t, "Succeeded", granted.KeyVaultSecretStatus)
	assert.Equal(t, stage4CLIThumbprint(leaf), granted.Thumbprint)
}
