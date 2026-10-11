package azure_sdk_test

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The App Service front end terminates the client's connection and tells the
// app what it saw: X-Forwarded-For gains the client's address with its port,
// and X-Forwarded-Proto and X-AppService-Proto name the client's protocol.
func TestSDK_WebApps_ForwardedRequestHeaders(t *testing.T) {
	rg, name := uniqueName("sdk-web-xff-rg"), uniqueName("sdk-web-xff")
	ensureRG(t, rg)
	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	planPoller, err := plans.BeginCreateOrUpdate(ctx, rg, name+"-plan", armappservice.Plan{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("linux"),
		SKU:        &armappservice.SKUDescription{Name: to.Ptr("B1")},
		Properties: &armappservice.PlanProperties{Reserved: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	plan, err := planPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: plan.ID,
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + httpProbeImageName),
				AppCommandLine: to.Ptr("echo-headers"),
				AppSettings:    []*armappservice.NameValuePair{{Name: to.Ptr("WEBSITES_PORT"), Value: to.Ptr("8080")}},
			},
		},
	}, nil)
	require.NoError(t, err)
	site, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })

	got := echoedFrontEndHeaders(t, ptrVal(site.Properties.DefaultHostName), http.Header{
		"X-Forwarded-For":   {"203.0.113.7"},
		"X-Forwarded-Proto": {"spoofed"},
	})
	forwardedFor := strings.Split(got.Get("X-Forwarded-For"), ", ")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	clientIP, clientPort, err := net.SplitHostPort(forwardedFor[1])
	require.NoError(t, err, "the client's address carries its port: %q", forwardedFor[1])
	assertLoopbackIP(t, clientIP)
	assert.NotEmpty(t, clientPort)
	assert.Equal(t, baseURLScheme(t), got.Get("X-Forwarded-Proto"))
	assert.Equal(t, baseURLScheme(t), got.Get("X-AppService-Proto"))
}
