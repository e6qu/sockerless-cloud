package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// armBody reads, closes, and JSON-decodes an ARM response into a generic map.
func armBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out), "response body: %s", raw)
	return out
}

// TestConformance_ServiceBusQueueServerDefaults verifies the ARM queue create
// path stamps the server-assigned defaults (lockDuration, defaultMessageTimeToLive,
// etc.) real Azure returns on GET when the client omits them.
func TestConformance_ServiceBusQueueServerDefaults(t *testing.T) {
	rg := "conf-sb-rg"
	ns := "conf-servicebus-ns"

	factory, err := armservicebus.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := factory.NewNamespacesClient()
	queues := factory.NewQueuesClient()

	nsPoller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armservicebus.SBNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armservicebus.SBSKU{
			Name: to.Ptr(armservicebus.SKUNameStandard),
			Tier: to.Ptr(armservicebus.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	_, err = nsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Create a queue with only the name (empty properties).
	_, err = queues.CreateOrUpdate(ctx, rg, ns, "confqueue", armservicebus.SBQueue{
		Properties: &armservicebus.SBQueueProperties{},
	}, nil)
	require.NoError(t, err)

	got, err := queues.Get(ctx, rg, ns, "confqueue", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.LockDuration, "queue must carry a server-default lockDuration")
	assert.Equal(t, "PT1M", *got.Properties.LockDuration)
	require.NotNil(t, got.Properties.DefaultMessageTimeToLive, "queue must carry a server-default defaultMessageTimeToLive")
	assert.NotEmpty(t, *got.Properties.DefaultMessageTimeToLive)
	require.NotNil(t, got.Properties.MaxDeliveryCount)
	assert.Equal(t, int32(10), *got.Properties.MaxDeliveryCount)
}

// TestConformance_StorageAccountWritableProperties verifies the storage account
// PUT carries accessTier, allowBlobPublicAccess, and minimumTlsVersion through
// to GET (terraform-provider-azurerm drift source otherwise).
func TestConformance_StorageAccountWritableProperties(t *testing.T) {
	rg := "conf-storage-rg"
	account := "confstorageacct"
	ensureRG(t, rg)

	base := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Storage/storageAccounts/" + account + "?api-version=2023-05-01"

	putBody := `{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"},` +
		`"properties":{"allowBlobPublicAccess":false,"accessTier":"Cool","minimumTlsVersion":"TLS1_2"}}`
	putReq, _ := http.NewRequestWithContext(ctx, "PUT", base, strings.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("Authorization", simARMBearer)
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	getReq, _ := http.NewRequestWithContext(ctx, "GET", base, nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	body := armBody(t, getResp)
	props, _ := body["properties"].(map[string]any)
	require.NotNil(t, props, "storage account GET must carry properties")
	assert.Equal(t, "Cool", props["accessTier"], "accessTier must round-trip")
	assert.Equal(t, false, props["allowBlobPublicAccess"], "allowBlobPublicAccess must round-trip")
	assert.Equal(t, "TLS1_2", props["minimumTlsVersion"], "minimumTlsVersion must round-trip")
}

// TestConformance_ACRRegistryRoundTrip verifies the registry honors a client's
// publicNetworkAccess and echoes sku.tier == sku.name when only the name is sent.
func TestConformance_ACRRegistryRoundTrip(t *testing.T) {
	rg := "conf-acr-rg"
	registry := "confacrregistry"
	ensureRG(t, rg)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreate(ctx, rg, registry, armcontainerregistry.Registry{
		Location: to.Ptr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameStandard)},
		Properties: &armcontainerregistry.RegistryProperties{
			PublicNetworkAccess: to.Ptr(armcontainerregistry.PublicNetworkAccessDisabled),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := client.Get(ctx, rg, registry, nil)
	require.NoError(t, err)
	require.NotNil(t, got.SKU)
	require.NotNil(t, got.SKU.Tier, "sku.tier must be populated from sku.name")
	assert.Equal(t, armcontainerregistry.SKUTierStandard, *got.SKU.Tier)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.PublicNetworkAccess)
	assert.Equal(t, armcontainerregistry.PublicNetworkAccessDisabled, *got.Properties.PublicNetworkAccess)
}

// TestConformance_VMAdminPasswordStripped verifies the write-only
// osProfile.adminPassword is never echoed back on GET. Host-gated because VM
// create requires the real-execution network host.
func TestConformance_VMAdminPasswordStripped(t *testing.T) {
	requireNetworkHost(t)
	rg := "conf-vm-rg"
	vnetName := "conf-vm-vnet"
	subnetName := "conf-vm-subnet"
	nicName := "conf-vm-nic"
	vmName := "conf-vm"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.91.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.91.1.0/24")},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, nicName, armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                    &armnetwork.Subnet{ID: subnetResp.ID},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					Primary:                   to.Ptr(true),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	nicResp, err := nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vmPoller, err := vmClient.BeginCreateOrUpdate(ctx, rg, vmName, armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypesStandardB1S)},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{
					Publisher: to.Ptr("Canonical"),
					Offer:     to.Ptr("0001-com-ubuntu-server-jammy"),
					SKU:       to.Ptr("22_04-lts"),
					Version:   to.Ptr("latest"),
				},
				OSDisk: &armcompute.OSDisk{
					Name:         to.Ptr("conf-vm-osdisk"),
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
				},
			},
			OSProfile: &armcompute.OSProfile{
				ComputerName:  to.Ptr(vmName),
				AdminUsername: to.Ptr("azureuser"),
				AdminPassword: to.Ptr("Str0ng-password-12345!"),
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{
					ID:         nicResp.ID,
					Properties: &armcompute.NetworkInterfaceReferenceProperties{Primary: to.Ptr(true)},
				}},
			},
		},
	}, nil)
	require.NoError(t, err)
	createResp, err := vmPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createResp.Properties)
	require.NotNil(t, createResp.Properties.OSProfile)
	assert.Nil(t, createResp.Properties.OSProfile.AdminPassword, "create response must not echo adminPassword")

	got, err := vmClient.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.OSProfile)
	assert.Nil(t, got.Properties.OSProfile.AdminPassword, "GET must not echo adminPassword")
}

// TestConformance_ACISecureValueStripped verifies env secureValue is accepted on
// create but never returned on GET.
func TestConformance_ACISecureValueStripped(t *testing.T) {
	rg := "conf-aci-rg"
	groupName := "conf-aci-group"
	ensureRG(t, rg)

	groups, err := armcontainerinstance.NewContainerGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	cpu := 1.0
	mem := 1.0
	poller, err := groups.BeginCreateOrUpdate(ctx, rg, groupName, armcontainerinstance.ContainerGroup{
		Location: to.Ptr("eastus"),
		Properties: &armcontainerinstance.ContainerGroupProperties{
			OSType:        to.Ptr(armcontainerinstance.OperatingSystemTypesLinux),
			RestartPolicy: to.Ptr(armcontainerinstance.ContainerGroupRestartPolicyNever),
			Containers: []*armcontainerinstance.Container{{
				Name: to.Ptr("main"),
				Properties: &armcontainerinstance.ContainerProperties{
					Image:   to.Ptr(commandImageName),
					Command: []*string{to.Ptr("/usr/local/bin/container-command"), to.Ptr("hold")},
					EnvironmentVariables: []*armcontainerinstance.EnvironmentVariable{{
						Name:        to.Ptr("API_TOKEN"),
						SecureValue: to.Ptr("super-secret"),
					}},
					Resources: &armcontainerinstance.ResourceRequirements{
						Requests: &armcontainerinstance.ResourceRequests{CPU: &cpu, MemoryInGB: &mem},
					},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		delPoller, err := groups.BeginDelete(ctx, rg, groupName, nil)
		if err == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	})

	assertNoSecureValue := func(group armcontainerinstance.ContainerGroup) {
		require.NotNil(t, group.Properties)
		require.NotEmpty(t, group.Properties.Containers)
		envs := group.Properties.Containers[0].Properties.EnvironmentVariables
		require.NotEmpty(t, envs)
		assert.Equal(t, "API_TOKEN", ptrVal(envs[0].Name))
		assert.Nil(t, envs[0].SecureValue, "secureValue must not be returned")
	}
	assertNoSecureValue(created.ContainerGroup)

	got, err := groups.Get(ctx, rg, groupName, nil)
	require.NoError(t, err)
	assertNoSecureValue(got.ContainerGroup)
}

// TestConformance_PrivateDNSAutoSOA verifies the auto-created SOA record set
// carries a populated soaRecord (terraform-provider-azurerm reads minimum_ttl
// off it). The record-set provisioningState is not exposed by the armprivatedns
// SDK model, so it is not asserted here.
func TestConformance_PrivateDNSAutoSOA(t *testing.T) {
	rg := "conf-dns-rg"
	zoneName := "conf.internal"
	ensureRG(t, rg)

	zones, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePoller, err := zones.BeginCreateOrUpdate(ctx, rg, zoneName, armprivatedns.PrivateZone{
		Location: to.Ptr("global"),
	}, nil)
	require.NoError(t, err)
	_, err = zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	records, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	gotSOA, err := records.Get(ctx, rg, zoneName, armprivatedns.RecordTypeSOA, "@", nil)
	require.NoError(t, err)
	require.NotNil(t, gotSOA.Properties)
	require.NotNil(t, gotSOA.Properties.SoaRecord, "auto SOA must carry a soaRecord")
	require.NotNil(t, gotSOA.Properties.SoaRecord.MinimumTTL)
	assert.Greater(t, *gotSOA.Properties.SoaRecord.MinimumTTL, int64(0))
}

// TestConformance_CosmosDefaultConsistencyPolicy verifies a create without a
// consistencyPolicy reads back the default Session policy.
func TestConformance_CosmosDefaultConsistencyPolicy(t *testing.T) {
	rg := "conf-cosmos-rg"
	account := "confcosmosacct"
	ensureRG(t, rg)

	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.DocumentDB/databaseAccounts/" + account

	resp := armReq(t, "PUT", base, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	getResp := armReq(t, "GET", base, "")
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	body := armBody(t, getResp)
	props, _ := body["properties"].(map[string]any)
	require.NotNil(t, props)
	cp, _ := props["consistencyPolicy"].(map[string]any)
	require.NotNil(t, cp, "cosmos GET must carry a consistencyPolicy")
	assert.Equal(t, "Session", cp["defaultConsistencyLevel"])
}

// TestConformance_RedisDefaultVersion verifies a Basic Redis without an explicit
// redisVersion reads back 6.0 (the real Basic/Standard default).
func TestConformance_RedisDefaultVersion(t *testing.T) {
	rg := "conf-redis-rg"
	name := "conf-redis"
	ensureRG(t, rg)

	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Cache/Redis/" + name

	resp := armReq(t, "PUT", base, `{"location":"eastus","properties":{"sku":{"name":"Basic","family":"C","capacity":0}}}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	opURL := resp.Header.Get("Azure-AsyncOperation")
	resp.Body.Close()
	require.NotEmpty(t, opURL)
	waitAzureAsyncOperation(t, opURL)

	getResp := armReq(t, "GET", base, "")
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	body := armBody(t, getResp)
	props, _ := body["properties"].(map[string]any)
	require.NotNil(t, props)
	assert.Equal(t, "6.0", props["redisVersion"], "Basic Redis default version must be 6.0")
}

// TestConformance_EventHubDefaultRetention verifies a hub created without an
// explicit messageRetentionInDays reads back 7 (the real default).
func TestConformance_EventHubDefaultRetention(t *testing.T) {
	rg := "conf-eh-rg"
	ns := "conf-eh-ns"
	hub := "conf-hub"
	ensureRG(t, rg)

	nsClient, err := armeventhub.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nsPoller, err := nsClient.BeginCreateOrUpdate(ctx, rg, ns, armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU:      &armeventhub.SKU{Name: to.Ptr(armeventhub.SKUNameStandard)},
	}, nil)
	require.NoError(t, err)
	_, err = nsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	hubsClient, err := armeventhub.NewEventHubsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = hubsClient.CreateOrUpdate(ctx, rg, ns, hub, armeventhub.Eventhub{
		Properties: &armeventhub.Properties{PartitionCount: to.Ptr[int64](2)},
	}, nil)
	require.NoError(t, err)

	got, err := hubsClient.Get(ctx, rg, ns, hub, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.MessageRetentionInDays)
	assert.Equal(t, int64(7), *got.Properties.MessageRetentionInDays, "default messageRetentionInDays must be 7")
}

// TestConformance_EventGridTopicDefaultsAndValidation verifies the topic create
// defaults publicNetworkAccess to Enabled and rejects an invalid inputSchema.
func TestConformance_EventGridTopicDefaultsAndValidation(t *testing.T) {
	rg := "conf-eg-rg"
	ensureRG(t, rg)

	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.EventGrid/topics/"

	good := armReq(t, "PUT", base+"conf-topic", `{"location":"eastus","properties":{}}`)
	require.Equal(t, http.StatusCreated, good.StatusCode)
	body := armBody(t, good)
	props, _ := body["properties"].(map[string]any)
	require.NotNil(t, props)
	assert.Equal(t, "Enabled", props["publicNetworkAccess"], "publicNetworkAccess must default to Enabled")

	bad := armReq(t, "PUT", base+"conf-topic-bad", `{"location":"eastus","properties":{"inputSchema":"Bogus"}}`)
	require.Equal(t, http.StatusBadRequest, bad.StatusCode, "invalid inputSchema must be rejected")
	bad.Body.Close()
}

// TestConformance_ACRRegistryList verifies the registry list-by-resource-group
// pager (armcontainerregistry.RegistriesClient.NewListByResourceGroupPager) and
// the subscription-level pager surface a registry that was just created. Both
// 404 without the list routes.
func TestConformance_ACRRegistryList(t *testing.T) {
	rg := "conf-acr-list-rg"
	registry := "confacrlistreg"
	ensureRG(t, rg)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreate(ctx, rg, registry, armcontainerregistry.Registry{
		Location: to.Ptr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameStandard)},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	rgPager := client.NewListByResourceGroupPager(rg, nil)
	var rgNames []string
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		rgNames = append(rgNames, acrRegistryNames(page.Value)...)
	}
	assert.Contains(t, rgNames, registry, "list-by-RG must contain the created registry")

	subPager := client.NewListPager(nil)
	var subNames []string
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		subNames = append(subNames, acrRegistryNames(page.Value)...)
	}
	assert.Contains(t, subNames, registry, "list-by-subscription must contain the created registry")
}

// acrRegistryNames extracts the names from a slice of ACR registries.
func acrRegistryNames(regs []*armcontainerregistry.Registry) []string {
	var names []string
	for _, reg := range regs {
		if reg != nil && reg.Name != nil {
			names = append(names, *reg.Name)
		}
	}
	return names
}

// TestConformance_ACRListCredentials verifies the admin-credential endpoint
// (RegistriesClient.ListCredentials, az acr credential show) returns the
// username and two passwords terraform reads into admin_username/admin_password.
func TestConformance_ACRListCredentials(t *testing.T) {
	rg := "conf-acr-cred-rg"
	registry := "confacrcredreg"
	ensureRG(t, rg)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreate(ctx, rg, registry, armcontainerregistry.Registry{
		Location: to.Ptr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameStandard)},
		Properties: &armcontainerregistry.RegistryProperties{
			AdminUserEnabled: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	creds, err := client.ListCredentials(ctx, rg, registry, nil)
	require.NoError(t, err)
	require.NotNil(t, creds.Username)
	assert.Equal(t, registry, *creds.Username)
	require.Len(t, creds.Passwords, 2, "ACR admin creds must carry two passwords")
	for _, pw := range creds.Passwords {
		require.NotNil(t, pw.Value)
		assert.NotEmpty(t, *pw.Value)
	}
}

// TestConformance_StorageBlobServicesPut verifies a PUT of blobServices/default
// with isVersioningEnabled reads back through GET (azurerm blob_properties
// otherwise drifts). Raw armReq to mirror the existing storage conformance test.
func TestConformance_StorageBlobServicesPut(t *testing.T) {
	rg := "conf-blobsvc-rg"
	account := "confblobsvcacct"
	ensureRG(t, rg)

	acctBase := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Storage/storageAccounts/" + account + "?api-version=2023-05-01"
	putAcct, _ := http.NewRequestWithContext(ctx, "PUT", acctBase,
		strings.NewReader(`{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"},"properties":{}}`))
	putAcct.Header.Set("Content-Type", "application/json")
	putAcct.Header.Set("Authorization", simARMBearer)
	putAcctResp, err := http.DefaultClient.Do(putAcct)
	require.NoError(t, err)
	putAcctResp.Body.Close()
	require.Equal(t, http.StatusOK, putAcctResp.StatusCode)

	svcBase := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Storage/storageAccounts/" + account + "/blobServices/default?api-version=2023-05-01"
	putSvc, _ := http.NewRequestWithContext(ctx, "PUT", svcBase,
		strings.NewReader(`{"properties":{"isVersioningEnabled":true,"deleteRetentionPolicy":{"enabled":true,"days":5}}}`))
	putSvc.Header.Set("Content-Type", "application/json")
	putSvc.Header.Set("Authorization", simARMBearer)
	putSvcResp, err := http.DefaultClient.Do(putSvc)
	require.NoError(t, err)
	putSvcResp.Body.Close()
	require.Equal(t, http.StatusOK, putSvcResp.StatusCode)

	getSvc, _ := http.NewRequestWithContext(ctx, "GET", svcBase, nil)
	getSvc.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getSvc)
	require.NoError(t, err)
	body := armBody(t, getResp)
	props, _ := body["properties"].(map[string]any)
	require.NotNil(t, props, "blobServices GET must carry properties")
	assert.Equal(t, true, props["isVersioningEnabled"], "isVersioningEnabled must round-trip")
	drp, _ := props["deleteRetentionPolicy"].(map[string]any)
	require.NotNil(t, drp, "deleteRetentionPolicy must round-trip")
	assert.Equal(t, true, drp["enabled"])
}

// TestConformance_TableEntityNotFoundODataError verifies a GetEntity on a
// missing row surfaces an *azcore.ResponseError carrying the OData error code
// EntityNotFound (real Azure Tables uses the {"odata.error":{...}} envelope; the
// ARM {"error":{...}} envelope yields the wrong code).
func TestConformance_TableEntityNotFoundODataError(t *testing.T) {
	account := "conftableerracct"
	tableName := "ConfErrTable"

	serviceClient, err := aztables.NewServiceClientWithNoCredential(storageSDKURL(t, account, "table"),
		&aztables.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = serviceClient.CreateTable(ctx, tableName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteTable(ctx, tableName, nil) })

	tableClient := serviceClient.NewClient(tableName)
	_, err = tableClient.GetEntity(ctx, "missing-pk", "missing-rk", nil)
	require.Error(t, err)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "error must be *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, string(aztables.EntityNotFound), respErr.ErrorCode)

	missingClient := serviceClient.NewClient("NoSuchTableConf")
	_, err = missingClient.GetEntity(ctx, "p", "r", nil)
	require.Error(t, err)
	require.True(t, errors.As(err, &respErr), "error must be *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, string(aztables.TableNotFound), respErr.ErrorCode)
}

// TestConformance_EventGridGetTopicPure verifies GET topic does not mutate
// persisted state when called with different Host headers — the stored
// properties must not accumulate or flip across reads.
func TestConformance_EventGridGetTopicPure(t *testing.T) {
	rg := "conf-eg-pure-rg"
	topic := "conf-eg-pure-topic"
	ensureRG(t, rg)

	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.EventGrid/topics/" + topic

	create := armReq(t, "PUT", base, `{"location":"eastus","properties":{}}`)
	require.Equal(t, http.StatusCreated, create.StatusCode)
	createBody := armBody(t, create)
	createProps, _ := createBody["properties"].(map[string]any)
	require.NotNil(t, createProps)
	createdEndpoint, _ := createProps["endpoint"].(string)
	require.NotEmpty(t, createdEndpoint, "create must stamp an endpoint")

	getEndpoint := func(hostHeader string) string {
		req, err := http.NewRequestWithContext(ctx, "GET", baseURL+base+"?api-version=2025-01-01", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", simARMBearer)
		if hostHeader != "" {
			req.Host = hostHeader
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body := armBody(t, resp)
		props, _ := body["properties"].(map[string]any)
		require.NotNil(t, props)
		ep, _ := props["endpoint"].(string)
		return ep
	}

	// The endpoint is a stable create-time property: a GET with an arbitrary
	// Host header must return the create-time value, not one derived from the
	// request Host, and must not corrupt stored state for later reads.
	assert.Equal(t, createdEndpoint, getEndpoint("first.example.localhost"),
		"GET must return the create-time endpoint regardless of request Host")
	assert.Equal(t, createdEndpoint, getEndpoint("second.example.localhost"),
		"GET must return the create-time endpoint regardless of request Host")
	assert.Equal(t, createdEndpoint, getEndpoint(""),
		"GET must not persist a request-Host-derived endpoint into stored state")
}

// TestConformance_ServiceBusTopicListPagination verifies the ARM topic list
// honors $top and emits a nextLink that walks the remaining topics.
func TestConformance_ServiceBusTopicListPagination(t *testing.T) {
	rg := "conf-sb-page-rg"
	ns := "conf-sb-page-ns"

	factory, err := armservicebus.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := factory.NewNamespacesClient()
	topics := factory.NewTopicsClient()

	nsPoller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armservicebus.SBNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armservicebus.SBSKU{
			Name: to.Ptr(armservicebus.SKUNameStandard),
			Tier: to.Ptr(armservicebus.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	_, err = nsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	for _, name := range []string{"page-topic-a", "page-topic-b"} {
		_, err = topics.CreateOrUpdate(ctx, rg, ns, name, armservicebus.SBTopic{
			Properties: &armservicebus.SBTopicProperties{},
		}, nil)
		require.NoError(t, err)
	}

	// $top=1 → exactly one topic per page; the pager must walk the nextLink to
	// reach the second.
	pager := topics.NewListByNamespacePager(rg, ns, &armservicebus.TopicsClientListByNamespaceOptions{
		Top: to.Ptr(int32(1)),
	})
	var got []string
	pages := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(page.Value), 1, "each $top=1 page holds at most one topic")
		for _, tp := range page.Value {
			require.NotNil(t, tp.Name)
			got = append(got, *tp.Name)
		}
	}
	assert.GreaterOrEqual(t, pages, 2, "$top=1 over two topics must span at least two pages")
	assert.Contains(t, got, "page-topic-a")
	assert.Contains(t, got, "page-topic-b")
}

// TestConformance_ListBlobsHierarchyPrefixDelimiter verifies the blob data-plane
// honors prefix filtering and delimiter virtual-directory rollup
// (NewListBlobsHierarchyPager): with a/1, a/2, b in a container, delimiter="/"
// yields one BlobPrefix "a/" plus the blob "b".
func TestConformance_ListBlobsHierarchyPrefixDelimiter(t *testing.T) {
	account := "confhieracct"
	containerName := "conf-hier-container"

	svc, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = svc.CreateContainer(ctx, containerName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.DeleteContainer(ctx, containerName, nil) })

	for _, name := range []string{"a/1", "a/2", "b"} {
		_, err = svc.UploadBuffer(ctx, containerName, name, []byte("x"), nil)
		require.NoError(t, err)
	}

	containerClient, err := container.NewClientWithNoCredential(
		storageSDKURL(t, account, "blob")+containerName,
		&container.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	pager := containerClient.NewListBlobsHierarchyPager("/", nil)
	var blobNames, prefixes []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		require.NotNil(t, page.Segment)
		for _, b := range page.Segment.BlobItems {
			require.NotNil(t, b.Name)
			blobNames = append(blobNames, *b.Name)
		}
		for _, p := range page.Segment.BlobPrefixes {
			require.NotNil(t, p.Name)
			prefixes = append(prefixes, *p.Name)
		}
	}
	assert.Equal(t, []string{"b"}, blobNames, "top-level blob list must be just b")
	assert.Equal(t, []string{"a/"}, prefixes, "a/1 and a/2 must roll up under the a/ BlobPrefix")

	// prefix="a/" with the same delimiter lists the two blobs under a/ directly.
	prefixPager := containerClient.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{
		Prefix: to.Ptr("a/"),
	})
	var underA []string
	for prefixPager.More() {
		page, err := prefixPager.NextPage(ctx)
		require.NoError(t, err)
		for _, b := range page.Segment.BlobItems {
			require.NotNil(t, b.Name)
			underA = append(underA, *b.Name)
		}
	}
	assert.ElementsMatch(t, []string{"a/1", "a/2"}, underA, "prefix=a/ must list both blobs under a/")
}
