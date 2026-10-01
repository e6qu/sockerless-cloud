package azure_cli_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWebApp_CLI_NodeStackRunsTheDeployedZip drives a Linux web app on a
// built-in runtime stack with the native az commands: `az webapp list-runtimes`
// reads the stack catalogue, `az webapp create --runtime` creates the app on a
// stack from it, the app serves the platform's default page until content is
// deployed, and `az webapp deploy --src-url --type zip` deploys a package the
// platform image then runs.
func TestWebApp_CLI_NodeStackRunsTheDeployedZip(t *testing.T) {
	env := startAzTLSSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-stacks",
		// The public cloud's resource manager endpoint ends in a slash, and the
		// CLI appends ARM paths to it verbatim.
		"--endpoint-resource-manager", env.baseURL+"/",
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-stacks"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	var listed []struct {
		Config string `json:"config"`
		OS     string `json:"os"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "list-runtimes", "--os", "linux", "-o", "json")), &listed)
	var runtimes []string
	for _, r := range listed {
		assert.Equal(t, "Linux", r.OS)
		runtimes = append(runtimes, r.Config)
	}
	assert.ElementsMatch(t, []string{"NODE|20-lts", "NODE|22-lts", "PYTHON|3.12"}, runtimes)

	rg, plan, app := "stacks-cli-rg", "stacks-cli-plan", "stacks-cli-node"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "B1", "-o", "json"))
	runCLI(t, env.command("webapp", "create", "-g", rg, "-p", plan, "-n", app,
		"--runtime", "NODE:20-lts", "-o", "json"))

	var shown struct {
		LinuxFxVersion string `json:"linuxFxVersion"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "config", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	assert.Equal(t, "NODE|20-lts", shown.LinuxFxVersion)

	siteGET := func(path string) string {
		return runCLI(t, env.command("rest", "--method", "GET", "--url", env.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header"))
	}
	assert.Contains(t, siteGET("/"), "waiting for your content",
		"an app with no content serves the platform's default page")

	pkgURL := stage3ServeZip(t, stage3Zip(t, map[string]string{
		"server.js": `require("http").createServer((req, res) => res.end("node " + process.version + " " + req.url)).listen(process.env.PORT)`,
	}))
	runCLI(t, env.command("webapp", "deploy", "-g", rg, "-n", app,
		"--src-url", pkgURL, "--type", "zip", "-o", "json"))

	out := strings.TrimSpace(siteGET("/from-the-cli"))
	assert.Regexp(t, regexp.MustCompile(`^node v20\.\d+\.\d+ /from-the-cli$`), out)

	runCLI(t, env.command("webapp", "delete", "-g", rg, "-n", app))
}
