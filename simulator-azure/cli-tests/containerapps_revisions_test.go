package azure_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliRevision struct {
	Name       string `json:"name"`
	Properties struct {
		Active         bool   `json:"active"`
		Replicas       int32  `json:"replicas"`
		TrafficWeight  int32  `json:"trafficWeight"`
		LastActiveTime string `json:"lastActiveTime"`
	} `json:"properties"`
}

// TestContainerAppsCLI_RevisionsFollowTheTemplate drives `az containerapp
// revision` and `az containerapp ingress traffic` against a multiple-revision
// app: a template change creates a revision, the traffic split moves between
// them, and a revision is deactivated, activated and restarted on its own.
func TestContainerAppsCLI_RevisionsFollowTheTemplate(t *testing.T) {
	env := startAzTLSSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-aca-revisions",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-aca-revisions"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })

	rg, app := "aca-revisions-cli-rg", "aca-revisions-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	rest := func(method, url, body string, extra ...string) *exec.Cmd {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return env.command(append(args, extra...)...)
	}
	appURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s?api-version=%s",
		env.baseURL, subscriptionID, rg, app, acaAPIVersion)
	template := func(expression string) string {
		return fmt.Sprintf(`{"containers": [{"name": "main", "image": %q, "args": [%q]}],
			"scale": {"minReplicas": 1, "maxReplicas": 1}}`, evalImageName, expression)
	}
	azLongRunning(t, rest, "PUT", appURL, fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"configuration": {
				"activeRevisionsMode": "Multiple",
				"ingress": {"external": false, "targetPort": 80}
			},
			"template": %s
		}
	}`, template("1 + 1")))
	t.Cleanup(func() { azLongRunning(t, rest, "DELETE", appURL, "") })
	runCLI(t, rest("PATCH", appURL, fmt.Sprintf(`{"properties": {"template": %s}}`, template("2 + 2"))))

	list := func() map[string]cliRevision {
		var revisions []cliRevision
		parseJSON(t, runCLI(t, env.command("containerapp", "revision", "list", "-g", rg, "-n", app, "--all", "-o", "json")), &revisions)
		out := map[string]cliRevision{}
		for _, rev := range revisions {
			out[rev.Name] = rev
		}
		return out
	}
	var shown struct {
		Properties struct {
			LatestRevisionName string `json:"latestRevisionName"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("containerapp", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	latest := shown.Properties.LatestRevisionName
	assert.Equal(t, app+"--0000001", latest, "a template change creates the next numbered revision")
	revisions := list()
	require.Len(t, revisions, 2)
	var first string
	for name := range revisions {
		if name != latest {
			first = name
		}
	}
	assert.True(t, revisions[first].Properties.Active, "a multiple-revision app keeps its earlier revision active")
	assert.True(t, revisions[latest].Properties.Active)

	runCLI(t, env.command("containerapp", "ingress", "traffic", "set", "-g", rg, "-n", app,
		"--revision-weight", first+"=30", "latest=70", "-o", "json"))
	revisions = list()
	assert.EqualValues(t, 30, revisions[first].Properties.TrafficWeight)
	assert.EqualValues(t, 70, revisions[latest].Properties.TrafficWeight)

	runCLI(t, env.command("containerapp", "ingress", "traffic", "set", "-g", rg, "-n", app,
		"--revision-weight", "latest=100", "-o", "json"))
	deactivated := runCLI(t, env.command("containerapp", "revision", "deactivate", "-g", rg, "-n", app, "--revision", first))
	assert.Equal(t, `"Deactivate succeeded"`, strings.TrimSpace(deactivated))
	revisions = list()
	assert.False(t, revisions[first].Properties.Active)
	assert.EqualValues(t, 0, revisions[first].Properties.Replicas)
	assert.NotEmpty(t, revisions[first].Properties.LastActiveTime)

	activated := runCLI(t, env.command("containerapp", "revision", "activate", "-g", rg, "-n", app, "--revision", first))
	assert.Equal(t, `"Activate succeeded"`, strings.TrimSpace(activated))
	revisions = list()
	assert.True(t, revisions[first].Properties.Active)
	assert.EqualValues(t, 1, revisions[first].Properties.Replicas)

	restarted := runCLI(t, env.command("containerapp", "revision", "restart", "-g", rg, "-n", app, "--revision", latest))
	assert.Equal(t, `"Restart succeeded"`, strings.TrimSpace(restarted))

	runCLI(t, env.command("containerapp", "revision", "set-mode", "-g", rg, "-n", app, "--mode", "single", "-o", "json"))
	revisions = list()
	assert.False(t, revisions[first].Properties.Active, "a single-revision app keeps only its latest revision active")
	assert.True(t, revisions[latest].Properties.Active)
}
