package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A network trace is recorded against the site that asked for it, so a read
// reports the trace actually requested and a site that started none has none.
func TestSDK_WebApps_NetworkTraces(t *testing.T) {
	rg, name := "sdk-trace-rg", "sdk-trace-app"
	ensureRG(t, rg)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)

	// Stopping a site that has no capture running says so rather than
	// reporting a stop that did not happen.
	_, err = client.StopNetworkTrace(ctx, rg, name, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No network trace is running")

	started, err := client.BeginStartWebSiteNetworkTraceOperation(ctx, rg, name,
		&armappservice.WebAppsClientBeginStartWebSiteNetworkTraceOperationOptions{
			DurationInSeconds: to.Ptr[int32](30),
		})
	require.NoError(t, err)
	result, err := started.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.NetworkTraceArray)

	// The trace reports no capture path, because no capture file was written —
	// naming one would point a client at a file that is not there.
	trace := result.NetworkTraceArray[0]
	require.NotNil(t, trace.Status)
	assert.Nil(t, trace.Path, "a trace with no capture names no file")

	// A duration that is not a duration is refused.
	_, err = client.BeginStartWebSiteNetworkTraceOperation(ctx, rg, name,
		&armappservice.WebAppsClientBeginStartWebSiteNetworkTraceOperationOptions{
			DurationInSeconds: to.Ptr[int32](-5),
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive number of seconds")

	// The site's running capture stops.
	_, err = client.StopNetworkTrace(ctx, rg, name, nil)
	require.NoError(t, err)
}
