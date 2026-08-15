package azure_sdk_test

import (
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/apimanagement/armapimanagement"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The families that joined the cross-resource-group move dispatch with
// Microsoft.Network, driven through the real Azure SDK for Go clients: the
// resource re-homes, its child subtree follows, the credential the resource ID
// derives is not rotated, and every reference another resource holds to it is
// re-pointed.

// validateMoveExpectingFailure runs Resources_ValidateMoveResources for a
// resource the simulator must refuse and returns the error the SDK surfaced.
func validateMoveExpectingFailure(t *testing.T, srcRG, dstRG, resourceID string) error {
	t.Helper()
	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginValidateMoveResources(ctx, srcRG, armresources.MoveInfo{
		Resources:           []*string{to.Ptr(resourceID)},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	require.Error(t, err, "the move validation of %s must fail", resourceID)
	return err
}

// TestAzureResources_MoveAPIManagementService drives the
// Microsoft.ApiManagement hook. A subscription's keys are derived from the
// subscription's own resource ID, so a move that only re-homed records would
// silently invalidate every Ocp-Apim-Subscription-Key an API consumer holds.
//
// The simulator serves the API Management control plane only — there is no
// gateway data plane a subscription key could be presented to — so the proof
// is bounded to the material listSecrets serves, which IS that header's value.
func TestAzureResources_MoveAPIManagementService(t *testing.T) {
	srcRG, dstRG := "apim-move-src-rg", "apim-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const service, subscription = "sdk-apim-move-svc", "sdk-consumer"
	services, err := armapimanagement.NewServiceClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := services.BeginCreateOrUpdate(ctx, srcRG, service, armapimanagement.ServiceResource{
		Location: to.Ptr("eastus"),
		SKU: &armapimanagement.ServiceSKUProperties{
			Name:     to.Ptr(armapimanagement.SKUTypeDeveloper),
			Capacity: to.Ptr[int32](1),
		},
		Properties: &armapimanagement.ServiceProperties{
			PublisherEmail: to.Ptr("owner@contoso.test"),
			PublisherName:  to.Ptr("Contoso"),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	serviceID := *created.ID

	subscriptions, err := armapimanagement.NewSubscriptionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = subscriptions.CreateOrUpdate(ctx, srcRG, service, subscription, armapimanagement.SubscriptionCreateParameters{
		Properties: &armapimanagement.SubscriptionCreateParameterProperties{
			DisplayName: to.Ptr("SDK consumer"),
			Scope:       to.Ptr("/apis"),
		},
	}, nil)
	require.NoError(t, err)

	secretsBefore, err := subscriptions.ListSecrets(ctx, srcRG, service, subscription, nil)
	require.NoError(t, err)
	require.NotNil(t, secretsBefore.PrimaryKey)

	moveResourceToGroup(t, srcRG, dstRG, serviceID)

	moved, err := services.Get(ctx, dstRG, service, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = services.Get(ctx, srcRG, service, nil)
	require.Error(t, err, "the moved service must no longer resolve under the source group")

	movedSubscription, err := subscriptions.Get(ctx, dstRG, service, subscription, nil)
	require.NoError(t, err, "the subscription must move with its service")
	require.NotNil(t, movedSubscription.ID)
	assert.Contains(t, *movedSubscription.ID, "/resourceGroups/"+dstRG+"/")

	secretsAfter, err := subscriptions.ListSecrets(ctx, dstRG, service, subscription, nil)
	require.NoError(t, err)
	require.NotNil(t, secretsAfter.PrimaryKey)
	assert.Equal(t, *secretsBefore.PrimaryKey, *secretsAfter.PrimaryKey,
		"a cross-group move must not rotate a subscription key")
	assert.Equal(t, *secretsBefore.SecondaryKey, *secretsAfter.SecondaryKey)
}

// TestAzureResources_MoveLogicWorkflow drives the Microsoft.Logic hook. A
// workflow signs every callback URL it issues with its access key, so the
// signature an operator already holds has to still be the signature the
// workflow computes after the move, while the advertised endpoint — which
// embeds the resource ID — follows the move.
func TestAzureResources_MoveLogicWorkflow(t *testing.T) {
	srcRG, dstRG := "logic-move-src-rg", "logic-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const workflow = "sdk-logic-move-wf"
	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created, err := workflows.CreateOrUpdate(ctx, srcRG, workflow, armlogic.Workflow{
		Location: to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{
			Definition: map[string]any{
				"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
				"contentVersion": "1.0.0.0",
				"triggers":       map[string]any{},
				"actions":        map[string]any{},
				"outputs":        map[string]any{},
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	workflowID := *created.ID

	callbackBefore, err := workflows.ListCallbackURL(ctx, srcRG, workflow, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	require.NotNil(t, callbackBefore.Queries)
	require.NotNil(t, callbackBefore.Queries.Sig)
	require.NotNil(t, callbackBefore.Value)
	signatureBefore := *callbackBefore.Queries.Sig
	valueBefore := *callbackBefore.Value
	assert.NotContains(t, valueBefore, "/resourceGroups/",
		"a callback URL addresses the workflow on the service host, not through its resource ID")

	moveResourceToGroup(t, srcRG, dstRG, workflowID)

	moved, err := workflows.Get(ctx, dstRG, workflow, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = workflows.Get(ctx, srcRG, workflow, nil)
	require.Error(t, err, "the moved workflow must no longer resolve under the source group")

	callbackAfter, err := workflows.ListCallbackURL(ctx, dstRG, workflow, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	require.NotNil(t, callbackAfter.Queries)
	require.NotNil(t, callbackAfter.Queries.Sig)
	assert.Equal(t, signatureBefore, *callbackAfter.Queries.Sig,
		"a cross-group move must not invalidate an outstanding callback URL's signature")
	require.NotNil(t, callbackAfter.Value)
	assert.Equal(t, valueBefore, *callbackAfter.Value,
		"a callback URL an operator already holds must survive a cross-group move intact")
	require.NotNil(t, callbackAfter.BasePath)
	assert.NotContains(t, *callbackAfter.BasePath, srcRG)
	assert.NotContains(t, *callbackAfter.BasePath, dstRG)
}

// TestAzureResources_MoveCosmosAccount drives the Microsoft.DocumentDB hook.
// A Cosmos DB account's four master keys are derived from its resource ID, so
// the move pins them: the connection string an application holds is unchanged.
func TestAzureResources_MoveCosmosAccount(t *testing.T) {
	srcRG, dstRG := "cosmos-move-src-rg", "cosmos-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const account = "sdkcosmosmoveacct"
	accounts, err := armcosmos.NewDatabaseAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := accounts.BeginCreateOrUpdate(ctx, srcRG, account, armcosmos.DatabaseAccountCreateUpdateParameters{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr(armcosmos.DatabaseAccountKindGlobalDocumentDB),
		Properties: &armcosmos.DatabaseAccountCreateUpdateProperties{
			DatabaseAccountOfferType: to.Ptr("Standard"),
			Locations: []*armcosmos.Location{
				{LocationName: to.Ptr("eastus"), FailoverPriority: to.Ptr[int32](0)},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	accountID := *created.ID

	keysBefore, err := accounts.ListKeys(ctx, srcRG, account, nil)
	require.NoError(t, err)
	require.NotNil(t, keysBefore.PrimaryMasterKey)
	connectionsBefore, err := accounts.ListConnectionStrings(ctx, srcRG, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, connectionsBefore.ConnectionStrings)
	require.NotNil(t, connectionsBefore.ConnectionStrings[0].ConnectionString)
	connectionBefore := *connectionsBefore.ConnectionStrings[0].ConnectionString

	moveResourceToGroup(t, srcRG, dstRG, accountID)

	moved, err := accounts.Get(ctx, dstRG, account, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = accounts.Get(ctx, srcRG, account, nil)
	require.Error(t, err, "the moved account must no longer resolve under the source group")

	keysAfter, err := accounts.ListKeys(ctx, dstRG, account, nil)
	require.NoError(t, err)
	assert.Equal(t, *keysBefore.PrimaryMasterKey, *keysAfter.PrimaryMasterKey,
		"a cross-group move must not rotate a Cosmos DB master key")
	assert.Equal(t, *keysBefore.SecondaryMasterKey, *keysAfter.SecondaryMasterKey)
	assert.Equal(t, *keysBefore.PrimaryReadonlyMasterKey, *keysAfter.PrimaryReadonlyMasterKey)
	assert.Equal(t, *keysBefore.SecondaryReadonlyMasterKey, *keysAfter.SecondaryReadonlyMasterKey)

	connectionsAfter, err := accounts.ListConnectionStrings(ctx, dstRG, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, connectionsAfter.ConnectionStrings)
	require.NotNil(t, connectionsAfter.ConnectionStrings[0].ConnectionString)
	assert.Equal(t, connectionBefore, *connectionsAfter.ConnectionStrings[0].ConnectionString,
		"the connection string an application holds must survive the move verbatim")
}

// TestAzureResources_MoveEventGridSystemTopic drives the system-topic hook and
// the inbound-reference half of the move at once. A system topic names the
// Azure resource it observes in its `source` property, so moving that source
// has to re-point the reference, and moving the system topic itself must not
// disturb it.
//
// The partner-namespace hook — the one Event Grid family that carries access
// keys — is covered in the simulator's own suite and by the Azure CLI suite
// instead: armeventgrid v1.0.0, the version this module pins, has no
// PartnerNamespaces client at all.
func TestAzureResources_MoveEventGridSystemTopic(t *testing.T) {
	srcRG, dstRG := "eg-system-move-src-rg", "eg-system-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const account, topic = "sdkegsystemsource", "sdk-eg-system-topic"
	accounts, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	accountPoller, err := accounts.BeginCreate(ctx, srcRG, account, armstorage.AccountCreateParameters{
		Location: to.Ptr("eastus"),
		SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		Kind:     to.Ptr(armstorage.KindStorageV2),
	}, nil)
	require.NoError(t, err)
	createdAccount, err := accountPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createdAccount.ID)
	accountID := *createdAccount.ID

	systemTopics, err := armeventgrid.NewSystemTopicsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	topicPoller, err := systemTopics.BeginCreateOrUpdate(ctx, srcRG, topic, armeventgrid.SystemTopic{
		Location: to.Ptr("eastus"),
		Properties: &armeventgrid.SystemTopicProperties{
			Source:    to.Ptr(accountID),
			TopicType: to.Ptr("Microsoft.Storage.StorageAccounts"),
		},
	}, nil)
	require.NoError(t, err)
	createdTopic, err := topicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createdTopic.ID)
	topicID := *createdTopic.ID

	// Moving the source re-points the reference the system topic holds to it.
	moveResourceToGroup(t, srcRG, dstRG, accountID)
	movedAccount, err := accounts.GetProperties(ctx, dstRG, account, nil)
	require.NoError(t, err)
	require.NotNil(t, movedAccount.ID)
	afterSourceMove, err := systemTopics.Get(ctx, srcRG, topic, nil)
	require.NoError(t, err)
	require.NotNil(t, afterSourceMove.Properties)
	require.NotNil(t, afterSourceMove.Properties.Source)
	assert.Equal(t, *movedAccount.ID, *afterSourceMove.Properties.Source,
		"the system topic must name the moved storage account, not the address it left")

	// Moving the system topic re-homes it without disturbing that reference.
	moveResourceToGroup(t, srcRG, dstRG, topicID)
	movedTopic, err := systemTopics.Get(ctx, dstRG, topic, nil)
	require.NoError(t, err)
	require.NotNil(t, movedTopic.ID)
	assert.Contains(t, *movedTopic.ID, "/resourceGroups/"+dstRG+"/")
	_, err = systemTopics.Get(ctx, srcRG, topic, nil)
	require.Error(t, err, "the moved system topic must no longer resolve under the source group")
	require.NotNil(t, movedTopic.Properties)
	require.NotNil(t, movedTopic.Properties.Source)
	assert.Equal(t, *movedAccount.ID, *movedTopic.Properties.Source)
}

// TestAzureResources_MoveVirtualNetworkRepointsReferrers drives the
// Microsoft.Network hook and the inbound-reference half of the move: a private
// DNS zone's virtual network link names the moved virtual network by resource
// ID, and it does not move with it.
func TestAzureResources_MoveVirtualNetworkRepointsReferrers(t *testing.T) {
	srcRG, dstRG := "vnet-move-src-rg", "vnet-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const vnet, zone, link = "sdk-vnet-move", "sdk-move.internal", "sdk-vnet-move-link"
	vnets, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnets.BeginCreateOrUpdate(ctx, srcRG, vnet, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.40.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	createdVnet, err := vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createdVnet.ID)
	vnetID := *createdVnet.ID

	zones, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePoller, err := zones.BeginCreateOrUpdate(ctx, srcRG, zone, armprivatedns.PrivateZone{
		Location: to.Ptr("global"),
	}, nil)
	require.NoError(t, err)
	_, err = zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	links, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	linkPoller, err := links.BeginCreateOrUpdate(ctx, srcRG, zone, link, armprivatedns.VirtualNetworkLink{
		Location: to.Ptr("global"),
		Properties: &armprivatedns.VirtualNetworkLinkProperties{
			VirtualNetwork:      &armprivatedns.SubResource{ID: to.Ptr(vnetID)},
			RegistrationEnabled: to.Ptr(false),
		},
	}, nil)
	require.NoError(t, err)
	_, err = linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	moveResourceToGroup(t, srcRG, dstRG, vnetID)

	movedVnet, err := vnets.Get(ctx, dstRG, vnet, nil)
	require.NoError(t, err)
	require.NotNil(t, movedVnet.ID)
	movedVnetID := *movedVnet.ID
	assert.Contains(t, movedVnetID, "/resourceGroups/"+dstRG+"/")
	_, err = vnets.Get(ctx, srcRG, vnet, nil)
	require.Error(t, err, "the moved virtual network must no longer resolve under the source group")

	movedLink, err := links.Get(ctx, srcRG, zone, link, nil)
	require.NoError(t, err, "the referring link did not move and must still resolve")
	require.NotNil(t, movedLink.Properties)
	require.NotNil(t, movedLink.Properties.VirtualNetwork)
	require.NotNil(t, movedLink.Properties.VirtualNetwork.ID)
	assert.Equal(t, movedVnetID, *movedLink.Properties.VirtualNetwork.ID,
		"the virtual network link must name the moved virtual network, not the address it left")
}

// TestAzureResources_MoveRedisCacheRepointsLinkedServer covers the inbound
// reference a geo-replication linked server holds: linkedRedisCacheId names a
// separate top-level cache that does not move with it.
func TestAzureResources_MoveRedisCacheRepointsLinkedServer(t *testing.T) {
	srcRG, dstRG := "redis-link-move-src-rg", "redis-link-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const primary, secondary = "sdk-redis-link-primary", "sdk-redis-link-secondary"
	caches, err := armredis.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	ids := map[string]string{}
	for _, name := range []string{primary, secondary} {
		poller, err := caches.BeginCreate(ctx, srcRG, name, armredis.CreateParameters{
			Location: to.Ptr("eastus"),
			Properties: &armredis.CreateProperties{
				SKU: &armredis.SKU{
					Name:     to.Ptr(armredis.SKUNamePremium),
					Family:   to.Ptr(armredis.SKUFamilyP),
					Capacity: to.Ptr[int32](1),
				},
			},
		}, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, created.ID)
		ids[name] = *created.ID
	}

	linkedServers, err := armredis.NewLinkedServerClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	linkPoller, err := linkedServers.BeginCreate(ctx, srcRG, primary, secondary, armredis.LinkedServerCreateParameters{
		Properties: &armredis.LinkedServerCreateProperties{
			LinkedRedisCacheID:       to.Ptr(ids[secondary]),
			LinkedRedisCacheLocation: to.Ptr("westus"),
			ServerRole:               to.Ptr(armredis.ReplicationRoleSecondary),
		},
	}, nil)
	require.NoError(t, err)
	_, err = linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	moveResourceToGroup(t, srcRG, dstRG, ids[secondary])

	movedSecondary, err := caches.Get(ctx, dstRG, secondary, nil)
	require.NoError(t, err)
	require.NotNil(t, movedSecondary.ID)

	link, err := linkedServers.Get(ctx, srcRG, primary, secondary, nil)
	require.NoError(t, err)
	require.NotNil(t, link.Properties)
	require.NotNil(t, link.Properties.LinkedRedisCacheID)
	assert.Equal(t, *movedSecondary.ID, *link.Properties.LinkedRedisCacheID,
		"the linked server must name the moved cache, not the address it left")
}

// TestAzureResources_MoveRefusesWhatAzureRefuses pins the other half of move
// fidelity: every type here is one Azure Resource Manager will not move
// between resource groups, so the simulator answers ResourceMoveNotSupported
// with the 409 a real move-validation failure reports.
func TestAzureResources_MoveRefusesWhatAzureRefuses(t *testing.T) {
	srcRG, dstRG := "refuse-move-src-rg", "refuse-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	for _, resourceType := range []string{
		"Microsoft.EventGrid/partnerRegistrations",
		"Microsoft.EventGrid/partnerConfigurations",
		"Microsoft.Network/privateLinkServices",
		"Microsoft.Network/applicationGateways",
		"Microsoft.Network/natGateways",
		"Microsoft.Network/networkProfiles",
		"Microsoft.Network/virtualNetworkTaps",
		"Microsoft.Network/networkManagers",
		"Microsoft.ContainerInstance/containerGroups",
	} {
		id := "/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG + "/providers/" + resourceType + "/refused"
		err := validateMoveExpectingFailure(t, srcRG, dstRG, id)
		var responseErr *azcore.ResponseError
		require.ErrorAs(t, err, &responseErr, "%s must be refused", resourceType)
		assert.Equal(t, "ResourceMoveNotSupported", responseErr.ErrorCode, "%s", resourceType)
		assert.Equal(t, http.StatusConflict, responseErr.StatusCode, "%s", resourceType)
	}
}

// TestAzureContainerRegistry_ContentIsPerRegistry proves the OCI content stores
// are keyed per registry: a repository pushed to one registry is absent from
// another, read through each registry's own login server with its own
// credential.
func TestAzureContainerRegistry_ContentIsPerRegistry(t *testing.T) {
	regA := newACRRegistryFixture(t, "acr-isolation-rg", "acrisolationonesdk",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})
	regB := newACRRegistryFixture(t, "acr-isolation-rg", "acrisolationtwosdk",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})
	authA := regA.basic(regA.passwords[0])
	authB := regB.basic(regB.passwords[0])

	const repo = "isolated/app"
	layer := []byte("per-registry-layer-bytes")
	digest := acrDigest(layer)

	resp := acrDo(t, http.MethodPost, regA.endpoint()+"/v2/"+repo+"/blobs/uploads/?digest="+digest,
		layer, "application/octet-stream", authA)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	manifest := []byte(acrAuthTestManifest)
	resp = acrDo(t, http.MethodPut, regA.endpoint()+"/v2/"+repo+"/manifests/v1",
		manifest, "application/vnd.docker.distribution.manifest.v2+json", authA)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// The registry it was pushed to serves it.
	resp = acrDo(t, http.MethodGet, regA.endpoint()+"/v2/"+repo+"/manifests/v1", nil, "", authA)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	resp = acrDo(t, http.MethodGet, regA.endpoint()+"/v2/"+repo+"/blobs/"+digest, nil, "", authA)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// The other registry has never heard of it.
	resp = acrDo(t, http.MethodGet, regB.endpoint()+"/v2/"+repo+"/manifests/v1", nil, "", authB)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"a repository pushed to one registry must be absent from another")
	resp.Body.Close()
	resp = acrDo(t, http.MethodGet, regB.endpoint()+"/v2/"+repo+"/blobs/"+digest, nil, "", authB)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"a blob pushed to one registry must be absent from another")
	resp.Body.Close()
}

// TestAzureResources_MoveRefusesOccupiedDestination drives the check Azure
// Resource Manager runs before a move re-homes anything: a resource keeps its
// name, so a destination group that already holds one of the same type under
// that name has no free identifier for it, and the move is refused rather than
// overwriting what is there.
func TestAzureResources_MoveRefusesOccupiedDestination(t *testing.T) {
	srcRG, occupiedRG := "sdk-conflict-src-rg", "sdk-conflict-dst-rg"
	createResourceGroup(t, srcRG)
	createResourceGroup(t, occupiedRG)

	const workflow = "sdkconflictwf"
	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	definition := armlogic.Workflow{
		Location: to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{
			Definition: map[string]any{
				"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
				"contentVersion": "1.0.0.0",
				"triggers":       map[string]any{},
				"actions":        map[string]any{},
				"outputs":        map[string]any{},
			},
		},
	}
	source, err := workflows.CreateOrUpdate(ctx, srcRG, workflow, definition, nil)
	require.NoError(t, err)
	require.NotNil(t, source.ID)
	occupant, err := workflows.CreateOrUpdate(ctx, occupiedRG, workflow, definition, nil)
	require.NoError(t, err)
	require.NotNil(t, occupant.Properties)
	occupantEndpoint := occupant.Properties.AccessEndpoint

	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{source.ID},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + occupiedRG),
	}

	validate, err := client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	if err == nil {
		_, err = validate.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "validating a move onto an occupied identifier must fail")
	assert.Contains(t, err.Error(), "ResourceMoveProviderValidationFailed")
	assert.Contains(t, err.Error(), "same name as a resource in the target resource group")

	perform, err := client.BeginMoveResources(ctx, srcRG, move, nil)
	if err == nil {
		_, err = perform.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "moving onto an occupied identifier must fail")
	assert.Contains(t, err.Error(), "ResourceMoveProviderValidationFailed")

	// Neither resource was touched.
	stillSource, err := workflows.Get(ctx, srcRG, workflow, nil)
	require.NoError(t, err, "the refused move must leave the source resource where it was")
	require.NotNil(t, stillSource.ID)
	stillOccupant, err := workflows.Get(ctx, occupiedRG, workflow, nil)
	require.NoError(t, err)
	require.NotNil(t, stillOccupant.Properties)
	assert.Equal(t, occupantEndpoint, stillOccupant.Properties.AccessEndpoint,
		"the refused move must not have replaced the resource already at the destination")
}
