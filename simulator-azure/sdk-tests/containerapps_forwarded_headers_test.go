package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Container Apps ingress appends the client's IP to X-Forwarded-For and
// overwrites X-Forwarded-Proto with the protocol the client used.
func TestSDK_ContainerAppsIngress_ForwardedRequestHeaders(t *testing.T) {
	rg, name := uniqueName("sdk-aca-xff-rg"), uniqueName("acaxff")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.App/managedEnvironments/sim-env"),
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{
				External:   to.Ptr(false),
				TargetPort: to.Ptr[int32](8080),
			}},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{
					Name:  to.Ptr("main"),
					Image: to.Ptr(httpProbeImageName),
					Args:  []*string{to.Ptr("echo-headers")},
				}},
				Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	got := echoedFrontEndHeaders(t, ptrVal(created.Properties.Configuration.Ingress.Fqdn), http.Header{
		"X-Forwarded-For":   {"203.0.113.7"},
		"X-Forwarded-Proto": {"spoofed"},
	})
	forwardedFor := strings.Split(got.Get("X-Forwarded-For"), ", ")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	assertLoopbackIP(t, forwardedFor[1])
	assert.Equal(t, baseURLScheme(t), got.Get("X-Forwarded-Proto"))
}

// echoedFrontEndHeaders sends a request carrying host to the simulator, the way
// a client resolving a platform hostname to its front end does, and returns the
// headers the echoing workload received.
func echoedFrontEndHeaders(t *testing.T, host string, header http.Header) http.Header {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Host = host
	for name, values := range header {
		req.Header[name] = values
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "front end for %s: %s", host, body)
	var echoed http.Header
	require.NoError(t, json.Unmarshal(body, &echoed), string(body))
	return echoed
}

func assertLoopbackIP(t *testing.T, value string) {
	t.Helper()
	ip := net.ParseIP(value)
	require.NotNil(t, ip, "%q is not an IP address", value)
	assert.True(t, ip.IsLoopback(), "%q is not the loopback address the test connected from", value)
}

func baseURLScheme(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	return u.Scheme
}
