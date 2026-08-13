package azure_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// web_staticsites_test.go drives the Static Web Apps family through the real
// `az staticwebapp` command group — create/show/list, appsettings,
// environments (builds), secrets + API-key reset, users, custom hostnames
// backed by real Azure DNS records, linked backends and user-provided
// function apps — plus `az rest` for the database-connection surface this az
// version does not expose. Native az service commands authenticate through
// `az cloud update` + `az login` against the TLS simulator, exactly like an
// operator repointing the CLI's endpoints at a private deployment (see
// az_login_test.go); only the coordinates differ from a run against Azure.
// The staticwebapp command group carries a client-side validator that
// requires the selected cloud to BE AzureCloud, so this flow updates the
// built-in cloud's endpoints in place (az fetches /metadata/endpoints from
// the new Azure Resource Manager coordinate — the ARM cloud-metadata
// bootstrap the simulator serves) instead of registering a separate cloud.
//
// The az CLI flattens each SDK model's `properties` block into the top level
// of its JSON output (x-ms-client-flatten), so the parse structs here read
// members like defaultHostname and status at the top level; only
// StringDictionary keeps a nested "properties" map.

func TestAzStaticWebApp_CLIFlow(t *testing.T) {
	env := startAzLoginSimulator(t)

	// az logger warnings ("Waiting for validation to finish...", the
	// appsettings redaction notice) print to stderr, which runCLI merges into
	// the captured output; --only-show-errors keeps command output pure JSON.
	swa := func(args ...string) *exec.Cmd {
		return env.command(append(args, "--only-show-errors")...)
	}

	runCLI(t, swa("cloud", "update",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, swa("login", "--service-principal",
		"-u", "test-client-id",
		"-p", "test-client-secret",
		"--tenant", azLoginTenantID,
		"--allow-no-subscriptions"))

	rg := "swa-cli-rg"
	app := "cli-swa"
	runCLI(t, swa("group", "create", "-n", rg, "-l", "eastus2", "-o", "json"))

	// Create + show + list.
	var created struct {
		Name            string `json:"name"`
		DefaultHostname string `json:"defaultHostname"`
		SKU             struct {
			Name string `json:"name"`
		} `json:"sku"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "create",
		"-n", app, "-g", rg, "-l", "eastus2", "--sku", "Standard", "-o", "json")), &created)
	if created.DefaultHostname != app+".azurestaticapps.net" {
		t.Errorf("defaultHostname = %q", created.DefaultHostname)
	}
	if created.SKU.Name != "Standard" {
		t.Errorf("sku = %q", created.SKU.Name)
	}
	defaultHostname := created.DefaultHostname

	var shown struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "show", "-n", app, "-g", rg, "-o", "json")), &shown)
	if shown.Name != app {
		t.Errorf("show name = %q", shown.Name)
	}
	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "list", "-g", rg, "-o", "json")), &listed)
	if len(listed) != 1 || listed[0].Name != app {
		t.Errorf("list = %+v", listed)
	}

	// Application settings.
	runCLI(t, swa("staticwebapp", "appsettings", "set",
		"-n", app, "-g", rg, "--setting-names", "PING=pong", "WORKERS=3", "-o", "json"))
	var settings struct {
		Properties map[string]string `json:"properties"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "appsettings", "list", "-n", app, "-g", rg, "-o", "json")), &settings)
	if settings.Properties["PING"] != "pong" || settings.Properties["WORKERS"] != "3" {
		t.Errorf("appsettings = %+v", settings.Properties)
	}
	runCLI(t, swa("staticwebapp", "appsettings", "delete",
		"-n", app, "-g", rg, "--setting-names", "WORKERS", "-o", "json"))
	var afterDelete struct {
		Properties map[string]string `json:"properties"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "appsettings", "list", "-n", app, "-g", rg, "-o", "json")), &afterDelete)
	if _, still := afterDelete.Properties["WORKERS"]; still {
		t.Errorf("WORKERS survived delete: %+v", afterDelete.Properties)
	}
	if afterDelete.Properties["PING"] != "pong" {
		t.Errorf("PING lost on delete: %+v", afterDelete.Properties)
	}

	// Environments: the production build exists from creation.
	var envs []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "environment", "list", "-n", app, "-g", rg, "-o", "json")), &envs)
	if len(envs) != 1 || envs[0].Name != "default" || envs[0].Status != "Ready" {
		t.Errorf("environments = %+v", envs)
	}
	var defaultEnv struct {
		Hostname string `json:"hostname"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "environment", "show",
		"-n", app, "-g", rg, "--environment-name", "default", "-o", "json")), &defaultEnv)
	if defaultEnv.Hostname != defaultHostname {
		t.Errorf("default environment hostname = %q", defaultEnv.Hostname)
	}

	// Secrets: deployment token list + rotation.
	var secrets struct {
		Properties map[string]string `json:"properties"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "secrets", "list", "-n", app, "-g", rg, "-o", "json")), &secrets)
	firstKey := secrets.Properties["apiKey"]
	if firstKey == "" {
		t.Fatal("secrets list returned no apiKey")
	}
	runCLI(t, swa("staticwebapp", "secrets", "reset-api-key", "-n", app, "-g", rg))
	var rotated struct {
		Properties map[string]string `json:"properties"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "secrets", "list", "-n", app, "-g", rg, "-o", "json")), &rotated)
	if rotated.Properties["apiKey"] == firstKey {
		t.Error("apiKey did not rotate on reset-api-key")
	}

	// Users: invite, list, update roles.
	var invitation struct {
		InvitationURL string `json:"invitationUrl"`
		ExpiresOn     string `json:"expiresOn"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "users", "invite",
		"-n", app, "-g", rg,
		"--domain", defaultHostname,
		"--roles", "admin",
		"--user-details", "octocat",
		"--authentication-provider", "GitHub",
		"--invitation-expiration-in-hours", "1", "-o", "json")), &invitation)
	if !strings.Contains(invitation.InvitationURL, defaultHostname) {
		t.Errorf("invitationUrl = %q", invitation.InvitationURL)
	}
	if invitation.ExpiresOn == "" {
		t.Error("expiresOn missing on invitation")
	}
	var users []struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
		Roles       string `json:"roles"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "users", "list", "-n", app, "-g", rg, "-o", "json")), &users)
	if len(users) != 1 || users[0].DisplayName != "octocat" {
		t.Fatalf("users = %+v", users)
	}
	runCLI(t, swa("staticwebapp", "users", "update",
		"-n", app, "-g", rg,
		"--user-id", users[0].UserID,
		"--authentication-provider", "GitHub",
		"--roles", "reader", "-o", "json"))
	var usersAfter []struct {
		Roles string `json:"roles"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "users", "list", "-n", app, "-g", rg, "-o", "json")), &usersAfter)
	if len(usersAfter) != 1 || usersAfter[0].Roles != "reader" {
		t.Errorf("users after update = %+v", usersAfter)
	}

	// Custom hostname, validated off a real Azure DNS CNAME record in the
	// simulator: the same two commands an operator runs against Azure DNS.
	// `az staticwebapp hostname set` validates first, then creates and waits.
	zone := "cli-swa-example.com"
	hostname := "www." + zone
	runCLI(t, swa("network", "dns", "zone", "create", "-g", rg, "-n", zone, "-o", "json"))
	runCLI(t, swa("network", "dns", "record-set", "cname", "set-record",
		"-g", rg, "-z", zone, "-n", "www", "-c", defaultHostname, "-o", "json"))
	var domain struct {
		Status string `json:"status"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "hostname", "set",
		"-n", app, "-g", rg, "--hostname", hostname, "-o", "json")), &domain)
	if domain.Status != "Ready" {
		t.Errorf("hostname status = %q, want Ready (CNAME is in place)", domain.Status)
	}
	var hostnames []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "hostname", "list", "-n", app, "-g", rg, "-o", "json")), &hostnames)
	if len(hostnames) != 1 || hostnames[0].Name != hostname {
		t.Errorf("hostnames = %+v", hostnames)
	}
	runCLI(t, swa("staticwebapp", "hostname", "delete",
		"-n", app, "-g", rg, "--hostname", hostname, "--yes"))

	// Backends + user-provided function apps link EXISTING Microsoft.Web
	// sites; create the backing plan + function app through the ARM plane the
	// logged-in CLI already talks to. The az backends commands operate on the
	// default build environment.
	subID := subscriptionID
	planURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/swa-cli-plan?api-version=2023-12-01", env.baseURL, subID, rg)
	runCLI(t, swa("rest", "--method", "PUT", "--url", planURL,
		"--body", `{"location":"eastus2","sku":{"name":"Y1","tier":"Dynamic"}}`, "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/swa-cli-fn?api-version=2023-12-01", env.baseURL, subID, rg)
	fnAppID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/swa-cli-fn", subID, rg)
	runCLI(t, swa("rest", "--method", "PUT", "--url", siteURL,
		"--body", fmt.Sprintf(`{"location":"eastus2","kind":"functionapp,linux","properties":{"serverFarmId":"%s"}}`,
			fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/swa-cli-plan", subID, rg)), "-o", "json"))

	runCLI(t, swa("staticwebapp", "backends", "validate",
		"-n", app, "-g", rg, "--backend-resource-id", fnAppID, "--backend-region", "eastus2"))
	runCLI(t, swa("staticwebapp", "backends", "link",
		"-n", app, "-g", rg, "--backend-resource-id", fnAppID, "--backend-region", "eastus2", "-o", "json"))
	var backends []struct {
		BackendResourceID string `json:"backendResourceId"`
		ProvisioningState string `json:"provisioningState"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "backends", "show", "-n", app, "-g", rg, "-o", "json")), &backends)
	if len(backends) != 1 || backends[0].BackendResourceID != fnAppID || backends[0].ProvisioningState != "Succeeded" {
		t.Errorf("backends = %+v", backends)
	}
	runCLI(t, swa("staticwebapp", "backends", "unlink", "-n", app, "-g", rg))

	// User-provided function app link (site scope).
	runCLI(t, swa("staticwebapp", "functions", "link",
		"-n", app, "-g", rg, "--function-resource-id", fnAppID, "-o", "json"))
	var fnApps []struct {
		FunctionAppResourceID string `json:"functionAppResourceId"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "functions", "show", "-n", app, "-g", rg, "-o", "json")), &fnApps)
	if len(fnApps) != 1 || fnApps[0].FunctionAppResourceID != fnAppID {
		t.Errorf("functions = %+v", fnApps)
	}
	runCLI(t, swa("staticwebapp", "functions", "unlink", "-n", app, "-g", rg))

	// Database connections: this az version has no `az staticwebapp
	// dbconnection` group, so the surface is exercised with `az rest` — the
	// same requests the newer CLI issues.
	dbResourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/swa-cli-cosmos", subID, rg)
	dbConnBase := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/staticSites/%s/databaseConnections", env.baseURL, subID, rg, app)
	runCLI(t, swa("rest", "--method", "PUT",
		"--url", dbConnBase+"/default?api-version=2025-03-01",
		"--body", fmt.Sprintf(`{"properties":{"resourceId":"%s","region":"eastus2","connectionString":"AccountEndpoint=https://swa-cli-cosmos.documents.azure.com;"}}`, dbResourceID),
		"-o", "json"))
	var dbConn struct {
		Properties struct {
			ResourceID       string `json:"resourceId"`
			ConnectionString string `json:"connectionString"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, swa("rest", "--method", "GET",
		"--url", dbConnBase+"/default?api-version=2025-03-01", "-o", "json")), &dbConn)
	if dbConn.Properties.ResourceID != dbResourceID || dbConn.Properties.ConnectionString != "" {
		t.Errorf("plain dbconnection GET = %+v", dbConn.Properties)
	}
	var dbConnDetails struct {
		Properties struct {
			ConnectionString string `json:"connectionString"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, swa("rest", "--method", "POST",
		"--url", dbConnBase+"/default/show?api-version=2025-03-01", "-o", "json")), &dbConnDetails)
	if !strings.Contains(dbConnDetails.Properties.ConnectionString, "AccountEndpoint") {
		t.Errorf("show dbconnection = %+v", dbConnDetails.Properties)
	}
	runCLI(t, swa("rest", "--method", "DELETE",
		"--url", dbConnBase+"/default?api-version=2025-03-01"))

	// Delete the app; a fresh list is empty.
	runCLI(t, swa("staticwebapp", "delete", "-n", app, "-g", rg, "--yes"))
	var afterDeleteList []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, swa("staticwebapp", "list", "-g", rg, "-o", "json")), &afterDeleteList)
	if len(afterDeleteList) != 0 {
		t.Errorf("list after delete = %+v", afterDeleteList)
	}
}
