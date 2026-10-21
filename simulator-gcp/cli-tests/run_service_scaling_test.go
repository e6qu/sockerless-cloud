package gcp_cli_test

import (
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A service deployed with `gcloud run deploy --concurrency=1
// --max-instances=2` runs a second instance for a request the first has no
// room for, and makes a third request wait for a place rather than start a
// third instance.
func TestCloudRun_CLI_ServiceScalesOnConcurrency(t *testing.T) {
	t.Parallel()
	const service = "cli-svc-scale"
	runCLI(t, gcloudRegionalRunCLI("run", "deploy", service,
		"--region="+location,
		"--no-allow-unauthenticated",
		"--quiet",
		"--image="+commandImageName,
		"--args=instance-http,8080",
		"--port=8080",
		"--concurrency=1",
		"--max-instances=2",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "services", "delete", service, "--region="+location, "--quiet"))
	})
	var stored struct {
		URI      string `json:"uri"`
		Template struct {
			MaxInstanceRequestConcurrency int `json:"maxInstanceRequestConcurrency"`
			Scaling                       struct {
				MaxInstanceCount int `json:"maxInstanceCount"`
			} `json:"scaling"`
		} `json:"template"`
	}
	parseJSON(t, httpDoJSON(t, "GET", runServiceURL(service), ""), &stored)
	require.NotEmpty(t, stored.URI)
	assert.Equal(t, 1, stored.Template.MaxInstanceRequestConcurrency)
	assert.Equal(t, 2, stored.Template.Scaling.MaxInstanceCount)
	invoker := cliInvokerEmail(t)
	addServiceInvoker(t, service, "serviceAccount:"+invoker)
	token := cliIDToken(t, invoker, stored.URI)
	u, err := url.Parse(stored.URI)
	require.NoError(t, err)

	type answer struct {
		status int
		body   string
		err    error
	}
	invoke := func(n int) []answer {
		answers := make([]answer, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req, err := http.NewRequest(http.MethodGet, baseURL+"/?hold=1500", nil)
				if err != nil {
					answers[i].err = err
					return
				}
				req.Host = u.Host
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					answers[i].err = err
					return
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				answers[i] = answer{status: resp.StatusCode, body: string(body), err: err}
			}()
		}
		wg.Wait()
		return answers
	}

	hosts := map[string]bool{}
	for _, a := range invoke(2) {
		require.NoError(t, a.err)
		require.Equal(t, http.StatusOK, a.status, "body=%q", a.body)
		hosts[a.body] = true
	}
	assert.Len(t, hosts, 2, "two requests held at once run on two instances")
	for _, a := range invoke(3) {
		require.NoError(t, a.err)
		require.Equal(t, http.StatusOK, a.status, "body=%q", a.body)
		assert.True(t, hosts[a.body], "no instance beyond --max-instances: %q", a.body)
	}
}
