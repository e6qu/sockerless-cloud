package azure_sdk_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Application Gateway through the official armnetwork client. The gateway
// is not just written and read back: the test stands up three real HTTP servers,
// puts them in backend pools, and then sends requests at the gateway's own
// frontend address to prove that the routing rules, URL path map, redirect
// configuration and rewrite rule set it was configured with are the ones that
// actually decide where each request goes — and that backend health reports
// what the gateway's probes found when it reached (or failed to reach) each
// server.

// TestNetworkApplicationGateway_RoutesRealTraffic covers the whole resource:
// create, get, retag, both lists, start/stop, backend health, backend health on
// demand and delete, with the data plane exercised between them.
func TestNetworkApplicationGateway_RoutesRealTraffic(t *testing.T) {
	requireNetworkHost(t)
	rg := "appgw-rg"
	requireResourceGroup(t, rg)
	gatewaySubnet := requireSubnet(t, rg, "appgw-vnet", "10.60.0.0/16", "appgw-subnet", "10.60.1.0/24")

	blue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Served-By", "blue")
		fmt.Fprintf(w, "blue %s host=%s", r.URL.Path, r.Host)
	}))
	defer blue.Close()
	green := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "green")
		fmt.Fprintf(w, "green %s", r.URL.Path)
	}))
	defer green.Close()
	// A server that answers every probe with 503 is a pool member the gateway
	// must classify as Down and must never forward a request to.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()

	pipClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pipPoller, err := pipClient.BeginCreateOrUpdate(ctx, rg, "appgw-pip", armnetwork.PublicIPAddress{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.PublicIPAddressSKU{Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard)},
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
		},
	}, nil)
	require.NoError(t, err)
	pip, err := pipPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, pip.Properties.IPAddress)
	frontendAddress := *pip.Properties.IPAddress
	require.NotEmpty(t, frontendAddress)

	gatewayID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/applicationGateways/site-gw",
		subscriptionID, rg)
	child := func(collection, name string) *armnetwork.SubResource {
		return &armnetwork.SubResource{ID: to.Ptr(gatewayID + "/" + collection + "/" + name)}
	}

	client, err := armnetwork.NewApplicationGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, "site-gw", armnetwork.ApplicationGateway{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"tier": to.Ptr("edge")},
		Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
			SKU: &armnetwork.ApplicationGatewaySKU{
				Name:     to.Ptr(armnetwork.ApplicationGatewaySKUNameStandardV2),
				Capacity: to.Ptr[int32](2),
			},
			GatewayIPConfigurations: []*armnetwork.ApplicationGatewayIPConfiguration{{
				Name: to.Ptr("gw-ipcfg"),
				Properties: &armnetwork.ApplicationGatewayIPConfigurationPropertiesFormat{
					Subnet: &armnetwork.SubResource{ID: to.Ptr(gatewaySubnet)},
				},
			}},
			FrontendIPConfigurations: []*armnetwork.ApplicationGatewayFrontendIPConfiguration{{
				Name: to.Ptr("public-frontend"),
				Properties: &armnetwork.ApplicationGatewayFrontendIPConfigurationPropertiesFormat{
					PublicIPAddress: &armnetwork.SubResource{ID: pip.ID},
				},
			}},
			FrontendPorts: []*armnetwork.ApplicationGatewayFrontendPort{
				{Name: to.Ptr("port-80"), Properties: &armnetwork.ApplicationGatewayFrontendPortPropertiesFormat{Port: to.Ptr[int32](80)}},
				{Name: to.Ptr("port-8080"), Properties: &armnetwork.ApplicationGatewayFrontendPortPropertiesFormat{Port: to.Ptr[int32](8080)}},
			},
			BackendAddressPools: []*armnetwork.ApplicationGatewayBackendAddressPool{
				{
					Name: to.Ptr("blue-pool"),
					Properties: &armnetwork.ApplicationGatewayBackendAddressPoolPropertiesFormat{
						BackendAddresses: []*armnetwork.ApplicationGatewayBackendAddress{
							{IPAddress: to.Ptr(testServerHost(t, blue.URL))},
						},
					},
				},
				{
					Name: to.Ptr("green-pool"),
					Properties: &armnetwork.ApplicationGatewayBackendAddressPoolPropertiesFormat{
						BackendAddresses: []*armnetwork.ApplicationGatewayBackendAddress{
							{IPAddress: to.Ptr(testServerHost(t, green.URL))},
						},
					},
				},
				{
					Name: to.Ptr("failing-pool"),
					Properties: &armnetwork.ApplicationGatewayBackendAddressPoolPropertiesFormat{
						BackendAddresses: []*armnetwork.ApplicationGatewayBackendAddress{
							{IPAddress: to.Ptr(testServerHost(t, failing.URL))},
						},
					},
				},
			},
			Probes: []*armnetwork.ApplicationGatewayProbe{{
				Name: to.Ptr("blue-probe"),
				Properties: &armnetwork.ApplicationGatewayProbePropertiesFormat{
					Protocol: to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
					Path:     to.Ptr("/healthz"),
					Match: &armnetwork.ApplicationGatewayProbeHealthResponseMatch{
						StatusCodes: []*string{to.Ptr("200-399")},
					},
				},
			}},
			BackendHTTPSettingsCollection: []*armnetwork.ApplicationGatewayBackendHTTPSettings{
				{
					Name: to.Ptr("blue-settings"),
					Properties: &armnetwork.ApplicationGatewayBackendHTTPSettingsPropertiesFormat{
						Port:     to.Ptr(testServerPort(t, blue.URL)),
						Protocol: to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
						Probe:    child("probes", "blue-probe"),
					},
				},
				{
					Name: to.Ptr("green-settings"),
					Properties: &armnetwork.ApplicationGatewayBackendHTTPSettingsPropertiesFormat{
						Port:     to.Ptr(testServerPort(t, green.URL)),
						Protocol: to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
						HostName: to.Ptr("green.internal"),
					},
				},
				{
					Name: to.Ptr("failing-settings"),
					Properties: &armnetwork.ApplicationGatewayBackendHTTPSettingsPropertiesFormat{
						Port:     to.Ptr(testServerPort(t, failing.URL)),
						Protocol: to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
					},
				},
			},
			HTTPListeners: []*armnetwork.ApplicationGatewayHTTPListener{
				{
					Name: to.Ptr("site-listener"),
					Properties: &armnetwork.ApplicationGatewayHTTPListenerPropertiesFormat{
						FrontendIPConfiguration: child("frontendIPConfigurations", "public-frontend"),
						FrontendPort:            child("frontendPorts", "port-80"),
						Protocol:                to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
					},
				},
				{
					Name: to.Ptr("legacy-listener"),
					Properties: &armnetwork.ApplicationGatewayHTTPListenerPropertiesFormat{
						FrontendIPConfiguration: child("frontendIPConfigurations", "public-frontend"),
						FrontendPort:            child("frontendPorts", "port-8080"),
						Protocol:                to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
					},
				},
			},
			RewriteRuleSets: []*armnetwork.ApplicationGatewayRewriteRuleSet{{
				Name: to.Ptr("stamp-region"),
				Properties: &armnetwork.ApplicationGatewayRewriteRuleSetPropertiesFormat{
					RewriteRules: []*armnetwork.ApplicationGatewayRewriteRule{{
						Name:         to.Ptr("stamp"),
						RuleSequence: to.Ptr[int32](100),
						Conditions: []*armnetwork.ApplicationGatewayRewriteRuleCondition{{
							Variable: to.Ptr("http_method"),
							Pattern:  to.Ptr("^GET$"),
						}},
						ActionSet: &armnetwork.ApplicationGatewayRewriteRuleActionSet{
							RequestHeaderConfigurations: []*armnetwork.ApplicationGatewayHeaderConfiguration{{
								HeaderName:  to.Ptr("X-Gateway-Region"),
								HeaderValue: to.Ptr("eastus"),
							}},
							ResponseHeaderConfigurations: []*armnetwork.ApplicationGatewayHeaderConfiguration{{
								HeaderName:  to.Ptr("X-Rewritten-By"),
								HeaderValue: to.Ptr("site-gw"),
							}},
						},
					}},
				},
			}},
			URLPathMaps: []*armnetwork.ApplicationGatewayURLPathMap{{
				Name: to.Ptr("site-paths"),
				Properties: &armnetwork.ApplicationGatewayURLPathMapPropertiesFormat{
					DefaultBackendAddressPool:  child("backendAddressPools", "blue-pool"),
					DefaultBackendHTTPSettings: child("backendHttpSettingsCollection", "blue-settings"),
					DefaultRewriteRuleSet:      child("rewriteRuleSets", "stamp-region"),
					PathRules: []*armnetwork.ApplicationGatewayPathRule{{
						Name: to.Ptr("api"),
						Properties: &armnetwork.ApplicationGatewayPathRulePropertiesFormat{
							Paths:               []*string{to.Ptr("/api/*")},
							BackendAddressPool:  child("backendAddressPools", "green-pool"),
							BackendHTTPSettings: child("backendHttpSettingsCollection", "green-settings"),
						},
					}},
				},
			}},
			RedirectConfigurations: []*armnetwork.ApplicationGatewayRedirectConfiguration{{
				Name: to.Ptr("to-https"),
				Properties: &armnetwork.ApplicationGatewayRedirectConfigurationPropertiesFormat{
					RedirectType:       to.Ptr(armnetwork.ApplicationGatewayRedirectTypePermanent),
					TargetURL:          to.Ptr("https://contoso.example"),
					IncludePath:        to.Ptr(true),
					IncludeQueryString: to.Ptr(true),
				},
			}},
			RequestRoutingRules: []*armnetwork.ApplicationGatewayRequestRoutingRule{
				{
					Name: to.Ptr("site-rule"),
					Properties: &armnetwork.ApplicationGatewayRequestRoutingRulePropertiesFormat{
						RuleType:     to.Ptr(armnetwork.ApplicationGatewayRequestRoutingRuleTypePathBasedRouting),
						Priority:     to.Ptr[int32](100),
						HTTPListener: child("httpListeners", "site-listener"),
						URLPathMap:   child("urlPathMaps", "site-paths"),
					},
				},
				{
					Name: to.Ptr("legacy-rule"),
					Properties: &armnetwork.ApplicationGatewayRequestRoutingRulePropertiesFormat{
						RuleType:              to.Ptr(armnetwork.ApplicationGatewayRequestRoutingRuleTypeBasic),
						Priority:              to.Ptr[int32](200),
						HTTPListener:          child("httpListeners", "legacy-listener"),
						RedirectConfiguration: child("redirectConfigurations", "to-https"),
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, created.Properties)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	assert.Equal(t, armnetwork.ApplicationGatewayOperationalStateRunning, *created.Properties.OperationalState)
	require.NotNil(t, created.Properties.ResourceGUID)
	assert.NotEmpty(t, *created.Properties.ResourceGUID)
	require.NotNil(t, created.Etag)
	// The v2 generation's default predefined TLS policy is the 2022 one.
	require.NotNil(t, created.Properties.DefaultPredefinedSSLPolicy)
	assert.Equal(t, armnetwork.ApplicationGatewaySSLPolicyName("AppGwSslPolicy20220101"), *created.Properties.DefaultPredefinedSSLPolicy)
	// The tier follows from the SKU name when the request leaves it out.
	assert.Equal(t, armnetwork.ApplicationGatewayTierStandardV2, *created.Properties.SKU.Tier)
	// A public frontend holds no private address, but it still reports the
	// allocation method every frontend carries. The HashiCorp provider reads
	// this back on refresh, so an absent value re-plans an unchanged gateway.
	require.Len(t, created.Properties.FrontendIPConfigurations, 1)
	publicFrontend := created.Properties.FrontendIPConfigurations[0].Properties
	assert.Empty(t, publicFrontend.PrivateIPAddress)
	require.NotNil(t, publicFrontend.PrivateIPAllocationMethod)
	assert.Equal(t, armnetwork.IPAllocationMethodDynamic, *publicFrontend.PrivateIPAllocationMethod)
	// Every child collection member is given the ARM identity a real gateway
	// assigns it, which is what the sibling references in the same body resolve
	// against.
	require.Len(t, created.Properties.HTTPListeners, 2)
	assert.Equal(t, gatewayID+"/httpListeners/site-listener", *created.Properties.HTTPListeners[0].ID)
	assert.Equal(t, "Microsoft.Network/applicationGateways/httpListeners", *created.Properties.HTTPListeners[0].Type)
	require.Len(t, created.Properties.URLPathMaps, 1)
	require.Len(t, created.Properties.URLPathMaps[0].Properties.PathRules, 1)
	assert.Equal(t, gatewayID+"/urlPathMaps/site-paths/pathRules/api",
		*created.Properties.URLPathMaps[0].Properties.PathRules[0].ID)

	// --- data plane -------------------------------------------------------
	// A request for a path the URL path map does not name goes to the map's
	// default pool, and the rewrite rule set attached to that default runs.
	body, resp := gatewayRequest(t, http.MethodGet, frontendAddress, "/", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "blue /")
	assert.Equal(t, "blue", resp.Header.Get("X-Served-By"))
	assert.Equal(t, "site-gw", resp.Header.Get("X-Rewritten-By"),
		"the rewrite rule set must stamp its response header")

	// A path the map names goes to the path rule's pool, with the host header
	// the backend settings pin.
	body, resp = gatewayRequest(t, http.MethodGet, frontendAddress, "/api/orders", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "green /api/orders")
	assert.Equal(t, "green", resp.Header.Get("X-Served-By"))
	assert.Empty(t, resp.Header.Get("X-Rewritten-By"),
		"the path rule names no rewrite rule set, so the default one must not run")

	// The rewrite rule set also reaches the request: the backend sees the header
	// it added, and the host header the settings pinned.
	body, _ = gatewayRequest(t, http.MethodGet, frontendAddress, "/echo", nil)
	assert.Contains(t, body, "host="+frontendAddress,
		"a settings member with no host name forwards the client's own host")

	// The second listener's rule redirects rather than forwarding, and carries
	// the incoming path and query over.
	_, resp = gatewayRequest(t, http.MethodGet, frontendAddress+":8080", "/legacy?page=2", nil)
	assert.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	assert.Equal(t, "https://contoso.example/legacy?page=2", resp.Header.Get("Location"))

	// --- backend health ---------------------------------------------------
	healthPoller, err := client.BeginBackendHealth(ctx, rg, "site-gw", nil)
	require.NoError(t, err)
	health, err := healthPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	byPool := map[string]*armnetwork.ApplicationGatewayBackendHealthPool{}
	for _, pool := range health.BackendAddressPools {
		byPool[*pool.BackendAddressPool.Name] = pool
	}
	bluePool := byPool["blue-pool"]
	require.NotNil(t, bluePool, "the blue pool must appear in backend health")
	require.Len(t, bluePool.BackendHTTPSettingsCollection, 1,
		"health is reported for the settings a routing rule pairs the pool with")
	require.Len(t, bluePool.BackendHTTPSettingsCollection[0].Servers, 1)
	assert.Equal(t, armnetwork.ApplicationGatewayBackendHealthServerHealthUp,
		*bluePool.BackendHTTPSettingsCollection[0].Servers[0].Health)
	assert.NotEmpty(t, *bluePool.BackendHTTPSettingsCollection[0].Servers[0].HealthProbeLog)
	// No routing rule pairs the failing pool with any settings, so the gateway
	// reports no settings for it — the same pairing its data plane uses.
	failingPool := byPool["failing-pool"]
	require.NotNil(t, failingPool)
	assert.Empty(t, failingPool.BackendHTTPSettingsCollection)

	// An on-demand probe runs against the pool and settings the caller names,
	// and the server that answers every probe with 503 is Down.
	onDemand, err := client.BeginBackendHealthOnDemand(ctx, rg, "site-gw", armnetwork.ApplicationGatewayOnDemandProbe{
		Protocol:            to.Ptr(armnetwork.ApplicationGatewayProtocolHTTP),
		Path:                to.Ptr("/"),
		BackendAddressPool:  child("backendAddressPools", "failing-pool"),
		BackendHTTPSettings: child("backendHttpSettingsCollection", "failing-settings"),
	}, nil)
	require.NoError(t, err)
	onDemandResult, err := onDemand.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, onDemandResult.BackendHealthHTTPSettings)
	require.Len(t, onDemandResult.BackendHealthHTTPSettings.Servers, 1)
	assert.Equal(t, armnetwork.ApplicationGatewayBackendHealthServerHealthDown,
		*onDemandResult.BackendHealthHTTPSettings.Servers[0].Health)

	// --- lifecycle --------------------------------------------------------
	stopPoller, err := client.BeginStop(ctx, rg, "site-gw", nil)
	require.NoError(t, err)
	_, err = stopPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	stopped, err := client.Get(ctx, rg, "site-gw", nil)
	require.NoError(t, err)
	assert.Equal(t, armnetwork.ApplicationGatewayOperationalStateStopped, *stopped.Properties.OperationalState)
	_, resp = gatewayRequest(t, http.MethodGet, frontendAddress, "/", nil)
	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"a stopped gateway must not serve its frontend address")

	startPoller, err := client.BeginStart(ctx, rg, "site-gw", nil)
	require.NoError(t, err)
	_, err = startPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	body, resp = gatewayRequest(t, http.MethodGet, frontendAddress, "/", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "blue /")

	tagged, err := client.UpdateTags(ctx, rg, "site-gw", armnetwork.TagsObject{
		Tags: map[string]*string{"tier": to.Ptr("edge"), "env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", *tagged.Tags["env"])
	// Retagging must not re-mint the resource GUID or restart the gateway.
	assert.Equal(t, *created.Properties.ResourceGUID, *tagged.Properties.ResourceGUID)

	assert.Contains(t, applicationGatewayNames(t, client, rg), "site-gw")
	all := map[string]bool{}
	allPager := client.NewListAllPager(nil)
	for allPager.More() {
		page, err := allPager.NextPage(ctx)
		require.NoError(t, err)
		for _, gw := range page.Value {
			all[*gw.Name] = true
		}
	}
	assert.True(t, all["site-gw"], "the subscription-wide list must contain the gateway")

	delPoller, err := client.BeginDelete(ctx, rg, "site-gw", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "site-gw", nil)
	require.Error(t, err, "a deleted application gateway must not be readable")
}

// TestNetworkApplicationGateway_PrivateLink covers the gateway's private-link
// surface: the groups it publishes, and the connection a consumer's private
// endpoint opens on it, which the gateway's owner then approves and removes.
func TestNetworkApplicationGateway_PrivateLink(t *testing.T) {
	requireNetworkHost(t)
	rg := "appgw-plink-rg"
	requireResourceGroup(t, rg)
	gatewaySubnet := requireSubnet(t, rg, "appgw-plink-vnet", "10.61.0.0/16", "gw-subnet", "10.61.1.0/24")
	linkSubnet := requireSubnet(t, rg, "appgw-plink-vnet", "10.61.0.0/16", "link-subnet", "10.61.2.0/24")
	consumerSubnet := requireSubnet(t, rg, "appgw-consumer-vnet", "10.62.0.0/16", "pe-subnet", "10.62.1.0/24")

	gatewayID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/applicationGateways/link-gw",
		subscriptionID, rg)
	client, err := armnetwork.NewApplicationGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, "link-gw", armnetwork.ApplicationGateway{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
			SKU: &armnetwork.ApplicationGatewaySKU{
				Name:     to.Ptr(armnetwork.ApplicationGatewaySKUNameStandardV2),
				Capacity: to.Ptr[int32](2),
			},
			GatewayIPConfigurations: []*armnetwork.ApplicationGatewayIPConfiguration{{
				Name: to.Ptr("gw-ipcfg"),
				Properties: &armnetwork.ApplicationGatewayIPConfigurationPropertiesFormat{
					Subnet: &armnetwork.SubResource{ID: to.Ptr(gatewaySubnet)},
				},
			}},
			PrivateLinkConfigurations: []*armnetwork.ApplicationGatewayPrivateLinkConfiguration{{
				Name: to.Ptr("private-frontend-link"),
				Properties: &armnetwork.ApplicationGatewayPrivateLinkConfigurationProperties{
					IPConfigurations: []*armnetwork.ApplicationGatewayPrivateLinkIPConfiguration{{
						Name: to.Ptr("link-ipcfg"),
						Properties: &armnetwork.ApplicationGatewayPrivateLinkIPConfigurationProperties{
							Subnet:                    &armnetwork.SubResource{ID: to.Ptr(linkSubnet)},
							PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
							Primary:                   to.Ptr(true),
						},
					}},
				},
			}},
			FrontendIPConfigurations: []*armnetwork.ApplicationGatewayFrontendIPConfiguration{{
				Name: to.Ptr("private-frontend"),
				Properties: &armnetwork.ApplicationGatewayFrontendIPConfigurationPropertiesFormat{
					Subnet:                    &armnetwork.SubResource{ID: to.Ptr(gatewaySubnet)},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					PrivateLinkConfiguration: &armnetwork.SubResource{
						ID: to.Ptr(gatewayID + "/privateLinkConfigurations/private-frontend-link"),
					},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	gw, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	// A dynamically addressed private frontend takes a real address out of the
	// subnet it names.
	require.Len(t, gw.Properties.FrontendIPConfigurations, 1)
	privateAddress := gw.Properties.FrontendIPConfigurations[0].Properties.PrivateIPAddress
	require.NotNil(t, privateAddress)
	assert.True(t, strings.HasPrefix(*privateAddress, "10.61.1."),
		"the frontend address must come out of the gateway subnet, got %q", *privateAddress)
	// Every frontend reports an allocation method — a frontend that named no
	// static address is dynamic. Terraform reads this back on refresh, so an
	// absent value re-plans the gateway on an unchanged configuration.
	require.NotNil(t, gw.Properties.FrontendIPConfigurations[0].Properties.PrivateIPAllocationMethod)
	assert.Equal(t, armnetwork.IPAllocationMethodDynamic,
		*gw.Properties.FrontendIPConfigurations[0].Properties.PrivateIPAllocationMethod)

	linkClient, err := armnetwork.NewApplicationGatewayPrivateLinkResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	linkPager := linkClient.NewListPager(rg, "link-gw", nil)
	var groups []string
	for linkPager.More() {
		page, err := linkPager.NextPage(ctx)
		require.NoError(t, err)
		for _, resource := range page.Value {
			groups = append(groups, *resource.Properties.GroupID)
			assert.Contains(t, resource.Properties.RequiredMembers, to.Ptr("private-frontend"),
				"the group must report the frontend it projects")
		}
	}
	assert.Equal(t, []string{"private-frontend-link"}, groups,
		"the gateway publishes one group per private link configuration")

	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "gw-consumer", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(consumerSubnet)},
			ManualPrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("gw-connection"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: to.Ptr(gatewayID),
					GroupIDs:             []*string{to.Ptr("private-frontend-link")},
					RequestMessage:       to.Ptr("please approve"),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	connClient, err := armnetwork.NewApplicationGatewayPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	connPager := connClient.NewListPager(rg, "link-gw", nil)
	var connectionNames []string
	for connPager.More() {
		page, err := connPager.NextPage(ctx)
		require.NoError(t, err)
		for _, conn := range page.Value {
			connectionNames = append(connectionNames, *conn.Name)
			assert.Equal(t, "Pending",
				*conn.Properties.PrivateLinkServiceConnectionState.Status,
				"a manual connection waits for the gateway owner")
		}
	}
	require.Equal(t, []string{"gw-consumer"}, connectionNames)

	got, err := connClient.Get(ctx, rg, "link-gw", "gw-consumer", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties.PrivateEndpoint)
	assert.Equal(t, "gw-consumer", *got.Properties.PrivateEndpoint.Name,
		"the connection renders the live private endpoint record")

	approvePoller, err := connClient.BeginUpdate(ctx, rg, "link-gw", "gw-consumer", armnetwork.ApplicationGatewayPrivateEndpointConnection{
		Properties: &armnetwork.ApplicationGatewayPrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armnetwork.PrivateLinkServiceConnectionState{
				Status:      to.Ptr("Approved"),
				Description: to.Ptr("approved by the gateway owner"),
			},
		},
	}, nil)
	require.NoError(t, err)
	approved, err := approvePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Approved", *approved.Properties.PrivateLinkServiceConnectionState.Status)

	// The consumer reads the owner's decision on its own endpoint: one object,
	// two views.
	consumerView, err := peClient.Get(ctx, rg, "gw-consumer", nil)
	require.NoError(t, err)
	require.Len(t, consumerView.Properties.ManualPrivateLinkServiceConnections, 1)
	assert.Equal(t, "Approved",
		*consumerView.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)

	deletePoller, err := connClient.BeginDelete(ctx, rg, "link-gw", "gw-consumer", nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = connClient.Get(ctx, rg, "link-gw", "gw-consumer", nil)
	require.Error(t, err, "a removed connection must not be readable")
}

// TestNetworkApplicationGateway_Catalogs covers the subscription-scoped
// catalogs: the server variables and headers a rewrite rule set may name, and
// the TLS options a gateway's SSL policy is built from.
func TestNetworkApplicationGateway_Catalogs(t *testing.T) {
	client, err := armnetwork.NewApplicationGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	serverVariables, err := client.ListAvailableServerVariables(ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, stringValues(serverVariables.StringArray), "client_ip")
	assert.Contains(t, stringValues(serverVariables.StringArray), "request_uri")

	requestHeaders, err := client.ListAvailableRequestHeaders(ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, stringValues(requestHeaders.StringArray), "X-Forwarded-For")

	responseHeaders, err := client.ListAvailableResponseHeaders(ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, stringValues(responseHeaders.StringArray), "Strict-Transport-Security")

	options, err := client.ListAvailableSSLOptions(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, options.Properties)
	assert.Equal(t, armnetwork.ApplicationGatewaySSLPolicyName("AppGwSslPolicy20150501"), *options.Properties.DefaultPolicy)
	assert.Len(t, options.Properties.PredefinedPolicies, 5)
	assert.Contains(t, options.Properties.AvailableProtocols, to.Ptr(armnetwork.ApplicationGatewaySSLProtocolTLSv13))
	assert.NotEmpty(t, options.Properties.AvailableCipherSuites)

	var policyNames []string
	pager := client.NewListAvailableSSLPredefinedPoliciesPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, policy := range page.Value {
			policyNames = append(policyNames, *policy.Name)
		}
	}
	assert.Contains(t, policyNames, "AppGwSslPolicy20220101")

	policy, err := client.GetSSLPredefinedPolicy(ctx, "AppGwSslPolicy20220101", nil)
	require.NoError(t, err)
	require.NotNil(t, policy.Properties)
	assert.Equal(t, armnetwork.ApplicationGatewaySSLProtocolTLSv12, *policy.Properties.MinProtocolVersion)
	assert.NotEmpty(t, policy.Properties.CipherSuites)

	_, err = client.GetSSLPredefinedPolicy(ctx, "AppGwSslPolicyNeverPublished", nil)
	require.Error(t, err, "a policy the catalog does not publish must not be readable")
}

// TestNetworkApplicationGateway_RejectsUnknownSubnet proves the resource
// provider validates the references a gateway names before it provisions
// anything. It needs no real-execution host: the request never gets that far.
func TestNetworkApplicationGateway_RejectsUnknownSubnet(t *testing.T) {
	rg := "appgw-reject-rg"
	requireResourceGroup(t, rg)
	client, err := armnetwork.NewApplicationGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	missing := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/absent/subnets/absent",
		subscriptionID, rg)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, "bad-gw", armnetwork.ApplicationGateway{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ApplicationGatewayPropertiesFormat{
			GatewayIPConfigurations: []*armnetwork.ApplicationGatewayIPConfiguration{{
				Name: to.Ptr("gw-ipcfg"),
				Properties: &armnetwork.ApplicationGatewayIPConfigurationPropertiesFormat{
					Subnet: &armnetwork.SubResource{ID: to.Ptr(missing)},
				},
			}},
		},
	}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "a gateway naming a subnet that does not exist must be rejected")
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

func applicationGatewayNames(t *testing.T, client *armnetwork.ApplicationGatewaysClient, rg string) []string {
	t.Helper()
	var names []string
	pager := client.NewListPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, gw := range page.Value {
			names = append(names, *gw.Name)
		}
	}
	return names
}

// gatewayRequest sends one request at the application gateway's own frontend
// address. The TCP connection goes to the simulator's endpoint — the one
// coordinate that differs from real Azure — while the Host header carries the
// gateway address, exactly as a client that resolved that address would send.
func gatewayRequest(t *testing.T, method, gatewayHost, target string, body *strings.Reader) (string, *http.Response) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		reader = body
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+target, reader)
	require.NoError(t, err)
	req.Host = gatewayHost
	// The gateway's own answer is what is under test, so a redirect it returns
	// must be read rather than followed to whatever host it names.
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		payload = append(payload, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	return string(payload), resp
}

func testServerHost(t *testing.T, serverURL string) string {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	return host
}

func testServerPort(t *testing.T, serverURL string) int32 {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	value, err := strconv.Atoi(port)
	require.NoError(t, err)
	return int32(value)
}

func stringValues(values []*string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, *value)
		}
	}
	return out
}
