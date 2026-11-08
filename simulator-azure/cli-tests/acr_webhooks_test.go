package azure_cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `az acr webhook ping` POSTs a ping event to the webhook's URI and
// `az acr webhook list-events` reads it back; `az acr show-usage` reports the
// SKU's limits; `az acr import` refuses an image the source does not hold.
func TestACRWebhooksUsageAndImportCLI(t *testing.T) {
	t.Parallel()
	env := startAzTLSSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-acr-webhooks",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-acr-webhooks"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	actions := make(chan string, 4)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var event struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(data, &event)
		actions <- event.Action
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	const rg, registry, source = "cli-acr-webhooks-rg", "cliacrwebhooks", "cliacrwebhookssrc"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("acr", "create", "-n", registry, "-g", rg, "--sku", "Basic", "-o", "json"))
	runCLI(t, env.command("acr", "create", "-n", source, "-g", rg, "--sku", "Basic", "-o", "json"))
	runCLI(t, env.command("acr", "webhook", "create", "-n", "clihook", "-r", registry, "-g", rg,
		"--uri", listener.URL, "--actions", "push", "delete", "-o", "json"))

	runCLI(t, env.command("acr", "webhook", "ping", "-n", "clihook", "-r", registry, "-g", rg, "-o", "json"))
	select {
	case action := <-actions:
		assert.Equal(t, "ping", action)
	case <-time.After(30 * time.Second):
		t.Fatal("the ping never reached the webhook's URI")
	}
	var events []struct {
		EventRequestMessage struct {
			Content struct {
				Action string `json:"action"`
			} `json:"content"`
		} `json:"eventRequestMessage"`
		EventResponseMessage struct {
			StatusCode string `json:"statusCode"`
		} `json:"eventResponseMessage"`
	}
	parseJSON(t, runCLI(t, env.command("acr", "webhook", "list-events", "-n", "clihook", "-r", registry, "-g", rg, "-o", "json")), &events)
	require.Len(t, events, 1)
	assert.Equal(t, "ping", events[0].EventRequestMessage.Content.Action)
	assert.Equal(t, "200", events[0].EventResponseMessage.StatusCode)

	// A Basic registry allows two webhooks.
	runCLI(t, env.command("acr", "webhook", "create", "-n", "clihook2", "-r", registry, "-g", rg,
		"--uri", listener.URL, "--actions", "push", "-o", "json"))
	refused := runCLIExpectFailure(t, env.command("acr", "webhook", "create", "-n", "clihook3", "-r", registry, "-g", rg,
		"--uri", listener.URL, "--actions", "push", "-o", "json"))
	assert.Contains(t, refused, "webhooks its SKU allows")

	var usage struct {
		Value []struct {
			Name         string `json:"name"`
			CurrentValue int64  `json:"currentValue"`
			Limit        int64  `json:"limit"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("acr", "show-usage", "-n", registry, "-g", rg, "-o", "json")), &usage)
	limits := map[string][2]int64{}
	for _, u := range usage.Value {
		limits[u.Name] = [2]int64{u.CurrentValue, u.Limit}
	}
	assert.Equal(t, [2]int64{2, 2}, limits["Webhooks"])
	assert.Equal(t, int64(10<<30), limits["Size"][1], "a Basic registry includes 10 GiB")

	failure := runCLIExpectFailure(t, env.command("acr", "import", "-n", registry, "-g", rg,
		"--source", "missing:v1", "--registry", source, "--image", "copied:v1"))
	assert.Contains(t, failure, "could not read missing:v1")
}
