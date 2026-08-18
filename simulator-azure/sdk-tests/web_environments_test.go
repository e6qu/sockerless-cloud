package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for App Service Environments
// (Microsoft.Web/hostingEnvironments) and Kubernetes Environments
// (Microsoft.Web/kubeEnvironments):
//
//	PUT|GET|PATCH|DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/hostingEnvironments/{name}
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/hostingEnvironments
//	GET  /subscriptions/{subscriptionId}/providers/Microsoft.Web/hostingEnvironments
//	GET  .../hostingEnvironments/{name}/capacities/compute
//	GET  .../hostingEnvironments/{name}/capacities/virtualip
//	GET|PUT|DELETE .../hostingEnvironments/{name}/configurations/customdnssuffix
//	GET|PUT .../hostingEnvironments/{name}/configurations/networking
//	GET  .../hostingEnvironments/{name}/inboundNetworkDependenciesEndpoints
//	GET  .../hostingEnvironments/{name}/outboundNetworkDependenciesEndpoints (declared gap)
//	GET|PUT|PATCH .../hostingEnvironments/{name}/multiRolePools/default
//	GET  .../hostingEnvironments/{name}/multiRolePools
//	GET  .../hostingEnvironments/{name}/multiRolePools/default/skus
//	GET  .../hostingEnvironments/{name}/multiRolePools/default/usages
//	GET  .../hostingEnvironments/{name}/multiRolePools/default/metricdefinitions (declared gap)
//	GET  .../hostingEnvironments/{name}/multiRolePools/default/instances/{instance}/metricdefinitions (declared gap)
//	GET|PUT|PATCH .../hostingEnvironments/{name}/workerPools/{workerPoolName}
//	GET  .../hostingEnvironments/{name}/workerPools
//	GET  .../hostingEnvironments/{name}/workerPools/{workerPoolName}/skus
//	GET  .../hostingEnvironments/{name}/workerPools/{workerPoolName}/usages
//	GET  .../hostingEnvironments/{name}/workerPools/{workerPoolName}/metricdefinitions (declared gap)
//	GET  .../hostingEnvironments/{name}/workerPools/{workerPoolName}/instances/{instance}/metricdefinitions (declared gap)
//	GET  .../hostingEnvironments/{name}/operations
//	GET  .../hostingEnvironments/{name}/usages
//	GET  .../hostingEnvironments/{name}/serverfarms
//	GET  .../hostingEnvironments/{name}/sites
//	POST .../hostingEnvironments/{name}/changeVirtualNetwork
//	POST .../hostingEnvironments/{name}/reboot
//	POST .../hostingEnvironments/{name}/resume
//	POST .../hostingEnvironments/{name}/suspend
//	POST .../hostingEnvironments/{name}/upgrade
//	POST .../hostingEnvironments/{name}/testUpgradeAvailableNotification
//	GET  .../hostingEnvironments/{name}/privateEndpointConnections
//	GET|PUT|DELETE .../hostingEnvironments/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//	GET  .../hostingEnvironments/{name}/privateLinkResources
//	PUT|GET|PATCH|DELETE .../kubeEnvironments/{name}
//	GET  .../kubeEnvironments  and  /subscriptions/{subscriptionId}/providers/Microsoft.Web/kubeEnvironments

const (
	aseRG     = "sdk-ase-rg"
	aseName   = "sdk-ase"
	aseSubnet = "10.60.1.0/24"
)

func environmentsClient(t *testing.T) *armappservice.EnvironmentsClient {
	t.Helper()
	client, err := armappservice.NewEnvironmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	return client
}

// asePagedSiteNames drains the pager an App Service Environment lifecycle
// operation answers with and returns the app names in it. Draining it is the
// assertion that matters: the pager is what the generated client hands back
// for these operations, and until the simulator answered the documented 202
// the SDK handed back one whose handler was nil, so any read of it panicked.
func asePagedSiteNames[T any](t *testing.T, pager *runtime.Pager[T]) []string {
	t.Helper()
	require.NotNil(t, pager)
	var names []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, site := range sitesOfPage(any(page)) {
			names = append(names, *site.Name)
		}
	}
	sort.Strings(names)
	return names
}

// sitesOfPage reads the WebAppCollection out of whichever per-operation
// response type the page carries. The three operations each generate their
// own response struct wrapping the same collection.
func sitesOfPage(page any) []*armappservice.Site {
	switch typed := page.(type) {
	case armappservice.EnvironmentsClientSuspendResponse:
		return typed.Value
	case armappservice.EnvironmentsClientResumeResponse:
		return typed.Value
	case armappservice.EnvironmentsClientChangeVnetResponse:
		return typed.Value
	}
	return nil
}

// aseActionApps invokes one of the three App Service Environment operations
// that answer with the collection of apps they moved — suspend, resume and
// changeVirtualNetwork — and returns that collection, decoded through the
// SDK's own WebAppCollection model.
//
// All three are long-running with their final state via Location, so the
// collection is read from the Location poll rather than from the response to
// the POST. That is also what makes them readable through the generated
// client: on a synchronous 200 azcore selects its NopPoller, whose result
// field is a zero value of the poller's type parameter — here
// *runtime.Pager[…] — so encoding/json allocates a fresh pager with a zero
// PagingHandler and NopPoller.Result assigns it over the pager the client
// built, leaving every read to call through a nil handler. The 202 + Location
// branch unmarshals the terminal body into the client's own pager through
// Pager.UnmarshalJSON, which sets the current page and leaves the handler
// intact.
func aseActionApps(t *testing.T, action, body string) []*armappservice.Site {
	t.Helper()
	url := fmt.Sprintf(
		"%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/hostingEnvironments/%s/%s?api-version=2025-03-01",
		baseURL, subscriptionID, aseRG, aseName, action)
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, payload)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(raw))
	location := resp.Header.Get("Location")
	require.NotEmpty(t, location,
		"a long-running operation whose final state comes via Location must send one")

	// Poll the Location the way a client does: 202 while the operation runs,
	// then the operation's own result.
	deadline := time.Now().Add(30 * time.Second)
	for {
		pollReq, pollErr := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		require.NoError(t, pollErr)
		pollReq.Header.Set("Authorization", simARMBearer)
		pollResp, pollErr := http.DefaultClient.Do(pollReq)
		require.NoError(t, pollErr)
		body, pollErr := io.ReadAll(pollResp.Body)
		pollResp.Body.Close()
		require.NoError(t, pollErr)
		if pollResp.StatusCode == http.StatusOK {
			var collection armappservice.WebAppCollection
			require.NoError(t, json.Unmarshal(body, &collection), string(body))
			return collection.Value
		}
		require.Equal(t, http.StatusAccepted, pollResp.StatusCode, string(body))
		require.True(t, time.Now().Before(deadline),
			"the %s operation never completed at its Location", action)
		time.Sleep(100 * time.Millisecond)
	}
}

// aseAppStates reduces an app collection to name→state so an assertion names
// both the apps the operation reported and the state it left them in.
func aseAppStates(apps []*armappservice.Site) map[string]string {
	states := map[string]string{}
	for _, app := range apps {
		states[*app.Name] = *app.Properties.State
	}
	return states
}

// TestSDK_AppServiceEnvironment_PlacementAndCapacity drives the whole
// App Service Environment surface through the official SDK and proves the
// environment is a real placement scope rather than a stored document: it is
// refused a subnet that does not exist, derives its internal address from the
// subnet it does occupy, counts its front-end instances from its own pool,
// reports Linux workers only once a Linux plan is placed in it, and subtracts
// the capacity those plans take from its stamp capacity.
func TestSDK_AppServiceEnvironment_PlacementAndCapacity(t *testing.T) {
	requireResourceGroup(t, aseRG)
	subnetID := requireSubnet(t, aseRG, "sdk-ase-vnet", "10.60.0.0/16", "ase-subnet", aseSubnet)
	client := environmentsClient(t)

	// Negative control 1: the environment does not exist yet.
	_, err := client.Get(ctx, aseRG, aseName, nil)
	require.Error(t, err, "an App Service Environment that was never created must not be readable")
	require.Contains(t, err.Error(), "ResourceNotFound")

	// Negative control 2: an environment is placed in a subnet that exists.
	// A reference to one that does not is refused, which is what proves the
	// placement is resolved against the real Microsoft.Network store.
	_, err = client.BeginCreateOrUpdate(ctx, aseRG, aseName, armappservice.EnvironmentResource{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.Environment{
			VirtualNetwork: &armappservice.VirtualNetworkProfile{
				ID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + aseRG +
					"/providers/Microsoft.Network/virtualNetworks/sdk-ase-vnet/subnets/absent"),
			},
		},
	}, nil)
	require.Error(t, err, "an App Service Environment must not be created in a subnet that does not exist")
	require.Contains(t, err.Error(), "does not exist")

	// Create the environment, internally load-balanced.
	poller, err := client.BeginCreateOrUpdate(ctx, aseRG, aseName, armappservice.EnvironmentResource{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("ASEV3"),
		Tags:     map[string]*string{"team": to.Ptr("platform")},
		Properties: &armappservice.Environment{
			VirtualNetwork:            &armappservice.VirtualNetworkProfile{ID: to.Ptr(subnetID)},
			InternalLoadBalancingMode: to.Ptr(armappservice.LoadBalancingModeWebPublishing),
			ZoneRedundant:             to.Ptr(true),
			DedicatedHostCount:        to.Ptr[int32](0),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, created.Properties)
	assert.Equal(t, armappservice.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	assert.Equal(t, armappservice.HostingEnvironmentStatusReady, *created.Properties.Status)
	assert.False(t, *created.Properties.Suspended)
	assert.Equal(t, armappservice.UpgradeAvailabilityNone, *created.Properties.UpgradeAvailability)
	assert.Equal(t, "Microsoft.Web/hostingEnvironments", *created.Type)
	// The read-only members of the virtual-network profile are resolved from
	// the subnet resource itself.
	require.NotNil(t, created.Properties.VirtualNetwork)
	assert.Equal(t, "ase-subnet", *created.Properties.VirtualNetwork.Name)
	assert.Equal(t, "Microsoft.Network/virtualNetworks/subnets", *created.Properties.VirtualNetwork.Type)
	// An internally load-balanced environment publishes Microsoft's internal
	// domain suffix for its apps.
	assert.Equal(t, aseName+".appserviceenvironment.net", *created.Properties.DNSSuffix)
	// The address it answers on comes out of the subnet it occupies: Azure
	// reserves the first four addresses of a subnet, so 10.60.1.4 is the first
	// one it can take.
	require.NotNil(t, created.Properties.NetworkingConfiguration)
	netProps := created.Properties.NetworkingConfiguration.Properties
	require.NotNil(t, netProps)
	require.Len(t, netProps.InternalInboundIPAddresses, 1)
	assert.Equal(t, "10.60.1.4", *netProps.InternalInboundIPAddresses[0])
	assert.Empty(t, netProps.ExternalInboundIPAddresses)
	require.Len(t, netProps.WindowsOutboundIPAddresses, 1)
	outbound := *netProps.WindowsOutboundIPAddresses[0]
	assert.NotEqual(t, "10.60.1.4", outbound, "the outbound lease must not be the subnet address")
	// Front-end count and Linux workers are computed, and both are still zero
	// and false: no front-end instance and no plan has been created yet.
	assert.Equal(t, int32(0), *created.Properties.MultiRoleCount)
	assert.False(t, *created.Properties.HasLinuxWorkers)

	// The virtual-IP view reports the same addresses.
	vip, err := client.GetVipInfo(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	require.NotNil(t, vip.Properties)
	assert.Equal(t, "10.60.1.4", *vip.Properties.InternalIPAddress)
	assert.Equal(t, "10.60.1.4", *vip.Properties.ServiceIPAddress)
	require.Len(t, vip.Properties.OutboundIPAddresses, 1)
	assert.Equal(t, outbound, *vip.Properties.OutboundIPAddresses[0])

	// Inbound dependencies before FTP is enabled: the environment's own
	// address on the web ports, and the subnet it occupies.
	inbound := listInboundDependencies(t, client)
	require.Contains(t, inbound, "App Service Environment VIP")
	assert.Equal(t, []string{"80", "443"}, inbound["App Service Environment VIP"].ports)
	assert.Equal(t, []string{"10.60.1.4/32"}, inbound["App Service Environment VIP"].endpoints)
	require.Contains(t, inbound, "App Service Environment subnet")
	assert.Equal(t, []string{aseSubnet}, inbound["App Service Environment subnet"].endpoints)

	// Turning FTP on adds its ports, and nothing else: the dependency list is
	// computed from the environment's own configuration.
	_, err = client.UpdateAseNetworkingConfiguration(ctx, aseRG, aseName, armappservice.AseV3NetworkingConfiguration{
		Properties: &armappservice.AseV3NetworkingConfigurationProperties{FtpEnabled: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	networking, err := client.GetAseV3NetworkingConfiguration(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.True(t, *networking.Properties.FtpEnabled)
	require.Len(t, networking.Properties.InternalInboundIPAddresses, 1,
		"a networking update must not disturb the addresses the platform assigned")
	inbound = listInboundDependencies(t, client)
	assert.Equal(t, []string{"80", "443", "21", "990", "10001-10020"},
		inbound["App Service Environment VIP"].ports)

	// The custom domain suffix round-trips and appears on the environment.
	suffixBefore, err := client.GetAseCustomDNSSuffixConfiguration(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Nil(t, suffixBefore.Properties.DNSSuffix, "no custom domain suffix is configured yet")
	_, err = client.UpdateAseCustomDNSSuffixConfiguration(ctx, aseRG, aseName,
		armappservice.CustomDNSSuffixConfiguration{
			Properties: &armappservice.CustomDNSSuffixConfigurationProperties{
				DNSSuffix:      to.Ptr("contoso.example"),
				CertificateURL: to.Ptr("https://sdk-ase-kv.vault.azure.net/secrets/contosocert"),
			},
		}, nil)
	require.NoError(t, err)
	suffix, err := client.GetAseCustomDNSSuffixConfiguration(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Equal(t, "contoso.example", *suffix.Properties.DNSSuffix)
	assert.Equal(t, armappservice.CustomDNSSuffixProvisioningStateSucceeded, *suffix.Properties.ProvisioningState)
	withSuffix, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	require.NotNil(t, withSuffix.Properties.CustomDNSSuffixConfiguration)
	assert.Equal(t, "contoso.example", *withSuffix.Properties.CustomDNSSuffixConfiguration.Properties.DNSSuffix)
	_, err = client.DeleteAseCustomDNSSuffixConfiguration(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	cleared, err := client.GetAseCustomDNSSuffixConfiguration(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Nil(t, cleared.Properties.DNSSuffix)

	// Front-end pool: scaling it is what moves the environment's front-end
	// instance count, which is a read-only member computed from the pool.
	frontEnd, err := client.BeginCreateOrUpdateMultiRolePool(ctx, aseRG, aseName, armappservice.WorkerPoolResource{
		Properties: &armappservice.WorkerPool{WorkerCount: to.Ptr[int32](2), WorkerSize: to.Ptr("Medium")},
		SKU:        &armappservice.SKUDescription{Name: to.Ptr("I1v2")},
	}, nil)
	require.NoError(t, err)
	pool, err := frontEnd.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), *pool.Properties.WorkerCount)
	require.Len(t, pool.Properties.InstanceNames, 2)
	assert.Equal(t, "default_0", *pool.Properties.InstanceNames[0])
	// The SKU tier is derived from its name, exactly as an App Service plan's is.
	assert.Equal(t, "IsolatedV2", *pool.SKU.Tier)
	scaled, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), *scaled.Properties.MultiRoleCount,
		"multiRoleCount reports the front-end pool's worker count")

	readPool, err := client.GetMultiRolePool(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), *readPool.Properties.WorkerCount)
	patched, err := client.UpdateMultiRolePool(ctx, aseRG, aseName, armappservice.WorkerPoolResource{
		Properties: &armappservice.WorkerPool{WorkerCount: to.Ptr[int32](3)},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(3), *patched.Properties.WorkerCount)
	assert.Equal(t, "Medium", *patched.Properties.WorkerSize,
		"a merge must keep the members it does not carry")

	multiPools := pagerAll(t, client.NewListMultiRolePoolsPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListMultiRolePoolsResponse) []*armappservice.WorkerPoolResource {
			return p.Value
		})
	require.Len(t, multiPools, 1)
	assert.Equal(t, "default", *multiPools[0].Name)

	// Worker pool.
	workerPoller, err := client.BeginCreateOrUpdateWorkerPool(ctx, aseRG, aseName, "1", armappservice.WorkerPoolResource{
		Properties: &armappservice.WorkerPool{
			WorkerCount:  to.Ptr[int32](4),
			WorkerSize:   to.Ptr("Large"),
			WorkerSizeID: to.Ptr[int32](2),
		},
		SKU: &armappservice.SKUDescription{Name: to.Ptr("I2v2")},
	}, nil)
	require.NoError(t, err)
	worker, err := workerPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(4), *worker.Properties.WorkerCount)
	readWorker, err := client.GetWorkerPool(ctx, aseRG, aseName, "1", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), *readWorker.Properties.WorkerSizeID)
	workerPools := pagerAll(t, client.NewListWorkerPoolsPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListWorkerPoolsResponse) []*armappservice.WorkerPoolResource {
			return p.Value
		})
	require.Len(t, workerPools, 1)

	// The SKU discovery answer is the SKU the pool runs, and it carries no
	// invented scale envelope.
	skus := pagerAll(t, client.NewListWorkerPoolSKUsPager(aseRG, aseName, "1", nil),
		func(p armappservice.EnvironmentsClientListWorkerPoolSKUsResponse) []*armappservice.SKUInfo {
			return p.Value
		})
	require.Len(t, skus, 1)
	assert.Equal(t, "I2v2", *skus[0].SKU.Name)
	assert.Equal(t, "IsolatedV2", *skus[0].SKU.Tier)
	assert.Nil(t, skus[0].Capacity, "the simulator enforces no scale envelope, so none is declared")
	multiSKUs := pagerAll(t, client.NewListMultiRolePoolSKUsPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListMultiRolePoolSKUsResponse) []*armappservice.SKUInfo {
			return p.Value
		})
	require.Len(t, multiSKUs, 1)
	assert.Equal(t, "I1v2", *multiSKUs[0].SKU.Name)

	// No pool quota is metered, so the usage collections are empty.
	assert.Empty(t, pagerAll(t, client.NewListMultiRoleUsagesPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListMultiRoleUsagesResponse) []*armappservice.Usage {
			return p.Value
		}))
	assert.Empty(t, pagerAll(t, client.NewListWebWorkerUsagesPager(aseRG, aseName, "1", nil),
		func(p armappservice.EnvironmentsClientListWebWorkerUsagesResponse) []*armappservice.Usage {
			return p.Value
		}))
	assert.Empty(t, pagerAll(t, client.NewListUsagesPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListUsagesResponse) []*armappservice.CsmUsageQuota {
			return p.Value
		}))

	// Nothing is running on the environment, so it reports no operations.
	operations, err := client.ListOperations(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Empty(t, operations.OperationArray)

	// Stamp capacity before any plan: every worker is available.
	capacities := listCapacities(t, client)
	require.Contains(t, capacities, "default")
	assert.Equal(t, int64(3), *capacities["default"].TotalCapacity)
	assert.Equal(t, int64(3), *capacities["default"].AvailableCapacity)
	require.Contains(t, capacities, "1")
	assert.Equal(t, int64(4), *capacities["1"].TotalCapacity)
	assert.Equal(t, int64(4), *capacities["1"].AvailableCapacity)
	assert.Equal(t, armappservice.WorkerSizeOptionsLarge, *capacities["1"].WorkerSize)
	assert.False(t, *capacities["1"].IsLinux)

	// Place a Linux plan on the worker pool. The environment's capacity drops
	// by the plan's instance count and its Linux-worker flag flips — both are
	// computed from the plan, not stored on the environment.
	aseID := *created.ID
	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	planPoller, err := plans.BeginCreateOrUpdate(ctx, aseRG, "sdk-ase-plan", armappservice.Plan{
		Location: to.Ptr("eastus"),
		SKU:      &armappservice.SKUDescription{Name: to.Ptr("I2v2"), Capacity: to.Ptr[int32](3)},
		Properties: &armappservice.PlanProperties{
			Reserved:                  to.Ptr(true),
			TargetWorkerSizeID:        to.Ptr[int32](2),
			HostingEnvironmentProfile: &armappservice.HostingEnvironmentProfile{ID: to.Ptr(aseID)},
		},
	}, nil)
	require.NoError(t, err)
	plan, err := planPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, plan.Properties.HostingEnvironmentProfile)
	assert.Equal(t, aseName, *plan.Properties.HostingEnvironmentProfile.Name,
		"the environment reference is resolved to its name and type")

	capacities = listCapacities(t, client)
	assert.Equal(t, int64(4), *capacities["1"].TotalCapacity)
	assert.Equal(t, int64(1), *capacities["1"].AvailableCapacity,
		"the plan's three instances are taken out of the pool's four workers")
	assert.True(t, *capacities["1"].IsLinux)
	withLinux, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.True(t, *withLinux.Properties.HasLinuxWorkers)

	// The environment's own inventory reads the plan and the app back.
	listedPlans := pagerAll(t, client.NewListAppServicePlansPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListAppServicePlansResponse) []*armappservice.Plan {
			return p.Value
		})
	require.Len(t, listedPlans, 1)
	assert.Equal(t, "sdk-ase-plan", *listedPlans[0].Name)

	webApps, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	sitePoller, err := webApps.BeginCreateOrUpdate(ctx, aseRG, "sdk-ase-app", armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: plan.ID,
		},
	}, nil)
	require.NoError(t, err)
	_, err = sitePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	listedSites := pagerAll(t, client.NewListWebAppsPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientListWebAppsResponse) []*armappservice.Site { return p.Value })
	require.Len(t, listedSites, 1)
	assert.Equal(t, "sdk-ase-app", *listedSites[0].Name)

	// Suspending the environment stops the apps in it; resuming starts them.
	// Each operation answers with the collection of apps it moved, so that
	// collection is asserted on the wire (see aseActionApps for why the
	// generated client cannot return it), the operation is then driven through
	// the generated client as a real caller drives it, and its effect is read
	// back through the environment and the apps.
	assert.Equal(t, map[string]string{"sdk-ase-app": "Stopped"},
		aseAppStates(aseActionApps(t, "suspend", "")),
		"suspend answers with the apps it stopped, in the state it left them")
	suspendPoller, err := client.BeginSuspend(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	suspendPager, err := suspendPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	// The result is a pager, and reading it is the whole point: an operation
	// that answered synchronously would hand back a pager with a nil handler
	// here and panic on the first read.
	assert.Equal(t, []string{"sdk-ase-app"}, asePagedSiteNames(t, suspendPager),
		"the suspend operation's own result names the apps it stopped")
	afterSuspend, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.True(t, *afterSuspend.Properties.Suspended)
	site, err := webApps.Get(ctx, aseRG, "sdk-ase-app", nil)
	require.NoError(t, err)
	assert.Equal(t, "Stopped", *site.Properties.State,
		"the app itself must report the state the environment put it in")

	assert.Equal(t, map[string]string{"sdk-ase-app": "Running"},
		aseAppStates(aseActionApps(t, "resume", "")),
		"resume answers with the apps it started, in the state it left them")
	resumePoller, err := client.BeginResume(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	resumePager, err := resumePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"sdk-ase-app"}, asePagedSiteNames(t, resumePager),
		"the resume operation's own result names the apps it started")
	resumedSite, err := webApps.Get(ctx, aseRG, "sdk-ase-app", nil)
	require.NoError(t, err)
	assert.Equal(t, "Running", *resumedSite.Properties.State)
	afterResume, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.False(t, *afterResume.Properties.Suspended)

	// Reboot is accepted and completes as a long-running operation.
	_, err = client.Reboot(ctx, aseRG, aseName, nil)
	require.NoError(t, err)

	// The upgrade pair. Nothing is available until the notification announces
	// one, so the upgrade is refused first — the negative control for the
	// state machine that follows.
	_, err = client.BeginUpgrade(ctx, aseRG, aseName, nil)
	require.Error(t, err, "an upgrade must be refused while none is available")
	assert.Contains(t, err.Error(), "No upgrade is available")
	_, err = client.TestUpgradeAvailableNotification(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	announced, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.UpgradeAvailabilityReady, *announced.Properties.UpgradeAvailability)
	upgradePoller, err := client.BeginUpgrade(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	_, err = upgradePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	upgraded, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.UpgradeAvailabilityNone, *upgraded.Properties.UpgradeAvailability,
		"the upgrade consumed the availability it was started from")

	// Private endpoint connections: none has been opened on the environment,
	// and it publishes no named private-link group.
	conns := pagerAll(t, client.NewGetPrivateEndpointConnectionListPager(aseRG, aseName, nil),
		func(p armappservice.EnvironmentsClientGetPrivateEndpointConnectionListResponse) []*armappservice.RemotePrivateEndpointConnectionARMResource {
			return p.Value
		})
	assert.Empty(t, conns)
	_, err = client.GetPrivateEndpointConnection(ctx, aseRG, aseName, "absent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
	links, err := client.GetPrivateLinkResources(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Empty(t, links.Value)

	// Declared gaps: the four metric-definition operations and the outbound
	// dependency catalog answer 501 naming what is missing, rather than a bare
	// routing 404 from inside a resource whose other operations all work.
	for name, pager := range map[string]func() error{
		"multi-role metric definitions": func() error {
			_, err := client.NewListMultiRoleMetricDefinitionsPager(aseRG, aseName, nil).NextPage(ctx)
			return err
		},
		"multi-role instance metric definitions": func() error {
			_, err := client.NewListMultiRolePoolInstanceMetricDefinitionsPager(aseRG, aseName, "default_0", nil).NextPage(ctx)
			return err
		},
		"worker metric definitions": func() error {
			_, err := client.NewListWebWorkerMetricDefinitionsPager(aseRG, aseName, "1", nil).NextPage(ctx)
			return err
		},
		"worker instance metric definitions": func() error {
			_, err := client.NewListWorkerPoolInstanceMetricDefinitionsPager(aseRG, aseName, "1", "1_0", nil).NextPage(ctx)
			return err
		},
		"outbound network dependencies": func() error {
			_, err := client.NewGetOutboundNetworkDependenciesEndpointsPager(aseRG, aseName, nil).NextPage(ctx)
			return err
		},
	} {
		err := pager()
		require.Error(t, err, "%s must report the gap rather than answer", name)
		assert.Contains(t, err.Error(), "NotImplemented", name)
		assert.Contains(t, err.Error(), "not implemented by the simulator", name)
	}

	// Moving the environment to another subnet re-derives its address. The
	// apps it hosts come with it, which is the collection the move answers
	// with.
	otherSubnet := requireSubnet(t, aseRG, "sdk-ase-vnet2", "10.61.0.0/16", "ase-subnet-2", "10.61.5.0/24")
	assert.Equal(t, map[string]string{"sdk-ase-app": "Running"},
		aseAppStates(aseActionApps(t, "changeVirtualNetwork", fmt.Sprintf(`{"id":%q}`, otherSubnet))),
		"the move answers with the apps that came with the environment")
	changePoller, err := client.BeginChangeVnet(ctx, aseRG, aseName,
		armappservice.VirtualNetworkProfile{ID: to.Ptr(otherSubnet)}, nil)
	require.NoError(t, err)
	_, err = changePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	afterMove, err := client.Get(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	assert.Equal(t, "10.61.5.4",
		*afterMove.Properties.NetworkingConfiguration.Properties.InternalInboundIPAddresses[0])

	// PATCH merges: it changes the scale factor and leaves the placement alone.
	updated, err := client.Update(ctx, aseRG, aseName, armappservice.EnvironmentPatchResource{
		Properties: &armappservice.Environment{FrontEndScaleFactor: to.Ptr[int32](20)},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(20), *updated.Properties.FrontEndScaleFactor)
	assert.Equal(t, otherSubnet, *updated.Properties.VirtualNetwork.ID)

	// Both list spellings report the environment.
	byGroup := pagerAll(t, client.NewListByResourceGroupPager(aseRG, nil),
		func(p armappservice.EnvironmentsClientListByResourceGroupResponse) []*armappservice.EnvironmentResource {
			return p.Value
		})
	require.Len(t, byGroup, 1)
	assert.Equal(t, aseName, *byGroup[0].Name)
	bySubscription := pagerAll(t, client.NewListPager(nil),
		func(p armappservice.EnvironmentsClientListResponse) []*armappservice.EnvironmentResource {
			return p.Value
		})
	require.NotEmpty(t, bySubscription)

	// Deleting an environment that still hosts plans is refused.
	_, err = client.BeginDelete(ctx, aseRG, aseName, nil)
	require.Error(t, err, "an environment that still hosts App Service plans must not be deleted")
	assert.Contains(t, err.Error(), "still deployed in it")

	_, err = webApps.Delete(ctx, aseRG, "sdk-ase-app", nil)
	require.NoError(t, err)
	_, err = plans.Delete(ctx, aseRG, "sdk-ase-plan", nil)
	require.NoError(t, err)
	deletePoller, err := client.BeginDelete(ctx, aseRG, aseName, nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, aseRG, aseName, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

type inboundEndpoint struct {
	endpoints []string
	ports     []string
}

func listInboundDependencies(t *testing.T, client *armappservice.EnvironmentsClient) map[string]inboundEndpoint {
	t.Helper()
	out := map[string]inboundEndpoint{}
	pager := client.NewGetInboundNetworkDependenciesEndpointsPager(aseRG, aseName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, item := range page.Value {
			endpoint := inboundEndpoint{}
			for _, e := range item.Endpoints {
				endpoint.endpoints = append(endpoint.endpoints, *e)
			}
			for _, p := range item.Ports {
				endpoint.ports = append(endpoint.ports, *p)
			}
			out[*item.Description] = endpoint
		}
	}
	return out
}

func listCapacities(t *testing.T, client *armappservice.EnvironmentsClient) map[string]*armappservice.StampCapacity {
	t.Helper()
	out := map[string]*armappservice.StampCapacity{}
	pager := client.NewListCapacitiesPager(aseRG, aseName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, item := range page.Value {
			out[*item.Name] = item
		}
	}
	return out
}

// pagerAll drains an azcore pager into one slice.
func pagerAll[P any, T any](t *testing.T, pager *runtime.Pager[P], value func(P) []*T) []*T {
	t.Helper()
	var out []*T
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		out = append(out, value(page)...)
	}
	return out
}

// TestSDK_KubeEnvironment_Lifecycle drives Microsoft.Web/kubeEnvironments
// through the official SDK: create, read, list at both scopes, merge-update,
// and delete. The environment's address and default domain are the simulator's
// own, and the Arc cluster's kubeConfig is a declared secret that a read never
// hands back.
func TestSDK_KubeEnvironment_Lifecycle(t *testing.T) {
	rg := "sdk-kube-rg"
	requireResourceGroup(t, rg)
	client, err := armappservice.NewKubeEnvironmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.Get(ctx, rg, "sdk-kube", nil)
	require.Error(t, err, "a Kubernetes environment that was never created must not be readable")
	assert.Contains(t, err.Error(), "ResourceNotFound")

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-kube", armappservice.KubeEnvironment{
		Location: to.Ptr("eastus"),
		ExtendedLocation: &armappservice.ExtendedLocation{
			Name: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.ExtendedLocation/customLocations/sdk-custom-location"),
		},
		Properties: &armappservice.KubeEnvironmentProperties{
			StaticIP:                    to.Ptr("10.70.0.7"),
			InternalLoadBalancerEnabled: to.Ptr(true),
			ArcConfiguration: &armappservice.ArcConfiguration{
				ArtifactStorageClassName: to.Ptr("default"),
				KubeConfig:               to.Ptr("apiVersion: v1\nkind: Config\n"),
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.KubeEnvironmentProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	assert.Equal(t, "10.70.0.7", *created.Properties.StaticIP)
	assert.Equal(t, "sdk-kube.10-70-0-7.k4apps.io", *created.Properties.DefaultDomain)
	assert.Equal(t, "CustomLocation", *created.ExtendedLocation.Type)
	require.NotNil(t, created.Properties.ArcConfiguration)
	assert.Equal(t, "default", *created.Properties.ArcConfiguration.ArtifactStorageClassName)
	assert.Nil(t, created.Properties.ArcConfiguration.KubeConfig,
		"the Arc kubeConfig is a secret the service does not hand back")

	read, err := client.Get(ctx, rg, "sdk-kube", nil)
	require.NoError(t, err)
	assert.Equal(t, "Microsoft.Web/kubeEnvironments", *read.Type)

	patched, err := client.Update(ctx, rg, "sdk-kube", armappservice.KubeEnvironmentPatchResource{
		Properties: &armappservice.KubeEnvironmentPatchResourceProperties{
			AksResourceID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.ContainerService/managedClusters/sdk-aks"),
		},
	}, nil)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(*patched.Properties.AksResourceID, "/sdk-aks"))
	assert.Equal(t, "10.70.0.7", *patched.Properties.StaticIP, "a merge keeps what it does not carry")

	byGroup := pagerAll(t, client.NewListByResourceGroupPager(rg, nil),
		func(p armappservice.KubeEnvironmentsClientListByResourceGroupResponse) []*armappservice.KubeEnvironment {
			return p.Value
		})
	require.Len(t, byGroup, 1)
	bySubscription := pagerAll(t, client.NewListBySubscriptionPager(nil),
		func(p armappservice.KubeEnvironmentsClientListBySubscriptionResponse) []*armappservice.KubeEnvironment {
			return p.Value
		})
	require.NotEmpty(t, bySubscription)

	deletePoller, err := client.BeginDelete(ctx, rg, "sdk-kube", nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "sdk-kube", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
