package gcp_cli_test

import (
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

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

// `gcloud run deploy --scaling=2` puts the service under manual scaling at two
// instances, which the Knative surface carries as the service's scalingMode
// and manualInstanceCount annotations and the v2 surface as its scaling, and
// the service runs exactly two instances before any request reaches it.
// gcloud offers no wait on a service's instances, so `logging read` is
// repeated until both have announced themselves.
func TestCloudRun_CLI_ServiceManualScalingRunsTheInstanceCount(t *testing.T) {
	t.Parallel()
	const service = "cli-svc-manual-scaling"
	runCLI(t, gcloudRegionalRunCLI("run", "deploy", service,
		"--region="+location,
		"--no-allow-unauthenticated",
		"--quiet",
		"--image="+commandImageName,
		"--args=instance-http,8080",
		"--port=8080",
		"--scaling=2",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "services", "delete", service, "--region="+location, "--quiet"))
	})

	var described struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	parseJSON(t, runCLI(t, gcloudRegionalRunCLI("run", "services", "describe", service, "--region="+location, "--format=json")), &described)
	assert.Equal(t, "manual", described.Metadata.Annotations["run.googleapis.com/scalingMode"])
	assert.Equal(t, "2", described.Metadata.Annotations["run.googleapis.com/manualInstanceCount"])

	var stored struct {
		Scaling struct {
			ScalingMode         string `json:"scalingMode"`
			ManualInstanceCount int    `json:"manualInstanceCount"`
		} `json:"scaling"`
	}
	parseJSON(t, httpDoJSON(t, "GET", runServiceURL(service), ""), &stored)
	assert.Equal(t, "MANUAL", stored.Scaling.ScalingMode)
	assert.Equal(t, 2, stored.Scaling.ManualInstanceCount)

	var entries []struct {
		Labels map[string]string `json:"labels"`
	}
	instances := map[string]bool{}
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_revision" AND resource.labels.service_name="`+service+`" AND textPayload:"serving as "`,
			"--format=json",
		)), &entries)
		for _, entry := range entries {
			instances[entry.Labels["instanceId"]] = true
		}
		return len(instances) >= 2
	}, 90*time.Second, time.Second, "the service never announced two instances: %v", instances)
	assert.Len(t, instances, 2, "manual scaling runs exactly the instance count")
	assert.NotContains(t, instances, "")
}
