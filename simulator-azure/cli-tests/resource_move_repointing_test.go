package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure CLI coverage for the cross-resource-group move families that joined
// the dispatch with Microsoft.Network, and for the inbound references the move
// re-points. Routes exercised:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources
//	POST …/providers/Microsoft.DocumentDB/databaseAccounts/{account}/listKeys
//	POST …/providers/Microsoft.DocumentDB/databaseAccounts/{account}/listConnectionStrings
//	POST …/providers/Microsoft.Logic/workflows/{workflowName}/listCallbackUrl
//	POST …/providers/Microsoft.EventGrid/partnerNamespaces/{partnerNamespaceName}/listKeys
//	GET  …/providers/Microsoft.Network/virtualNetworks/{vnetName}
//	GET  …/providers/Microsoft.Network/privateDnsZones/{zone}/virtualNetworkLinks/{link}

func cliResourceID(rg, provider, typeName, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s/%s",
		subscriptionID, rg, provider, typeName, name)
}

// TestResourceMoveCosmosAccountCLI moves a Cosmos DB account with
// `az resource move` and proves the credential survived: the four master keys
// and the connection string `az cosmosdb keys list` prints are identical
// afterwards, which is what every application's connection string carries.
func TestResourceMoveCosmosAccountCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-cosmos-move")

	srcRG, dstRG := "cli-move-cosmos-src-rg", "cli-move-cosmos-dst-rg"
	const account = "climovecosmosacct"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("cosmosdb", "create", "-n", account, "-g", srcRG,
		"--locations", "regionName=eastus", "failoverPriority=0", "isZoneRedundant=False", "-o", "json"))

	type cosmosKeys struct {
		PrimaryMasterKey           string `json:"primaryMasterKey"`
		SecondaryMasterKey         string `json:"secondaryMasterKey"`
		PrimaryReadonlyMasterKey   string `json:"primaryReadonlyMasterKey"`
		SecondaryReadonlyMasterKey string `json:"secondaryReadonlyMasterKey"`
	}
	var before cosmosKeys
	parseJSON(t, runCLI(t, env.command("cosmosdb", "keys", "list", "-n", account, "-g", srcRG, "-o", "json")), &before)
	require.NotEmpty(t, before.PrimaryMasterKey)

	accountID := cliResourceID(srcRG, "Microsoft.DocumentDB", "databaseAccounts", account)
	runCLI(t, env.command("resource", "move", "--ids", accountID, "--destination-group", dstRG))

	var moved struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("cosmosdb", "show", "-n", account, "-g", dstRG, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/")
	failure := runStorageCLIExpectFailure(t, env.command("cosmosdb", "show", "-n", account, "-g", srcRG, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound")

	var after cosmosKeys
	parseJSON(t, runCLI(t, env.command("cosmosdb", "keys", "list", "-n", account, "-g", dstRG, "-o", "json")), &after)
	assert.Equal(t, before, after, "a cross-group move must not rotate a Cosmos DB account's master keys")

	type cosmosConnections struct {
		ConnectionStrings []struct {
			ConnectionString string `json:"connectionString"`
		} `json:"connectionStrings"`
	}
	var connections cosmosConnections
	parseJSON(t, runCLI(t, env.command("cosmosdb", "keys", "list", "-n", account, "-g", dstRG,
		"--type", "connection-strings", "-o", "json")), &connections)
	require.NotEmpty(t, connections.ConnectionStrings)
	assert.Contains(t, connections.ConnectionStrings[0].ConnectionString, before.PrimaryMasterKey,
		"the connection string must still carry the key an application already holds")
}

// TestResourceMoveLogicWorkflowCLI moves a Consumption Logic App with
// `az resource move` and proves the callback URL an operator already holds
// still works: `az rest` on listCallbackUrl returns the same URL, signature
// included, because the workflow is addressed on the Logic Apps service host
// by an identifier no resource group appears in.
func TestResourceMoveLogicWorkflowCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-logic-move")

	srcRG, dstRG := "cli-move-logic-src-rg", "cli-move-logic-dst-rg"
	const workflow = "climovelogicwf"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))

	workflowID := cliResourceID(srcRG, "Microsoft.Logic", "workflows", workflow)
	runCLI(t, env.command("rest", "--method", "put", "--url", workflowID+"?api-version=2019-05-01",
		"--body", `{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{},"actions":{},"outputs":{}},"parameters":{}}}`))

	type callback struct {
		Value    string `json:"value"`
		BasePath string `json:"basePath"`
		Queries  struct {
			Sig string `json:"sig"`
		} `json:"queries"`
	}
	var before callback
	parseJSON(t, runCLI(t, env.command("rest", "--method", "post",
		"--url", workflowID+"/listCallbackUrl?api-version=2019-05-01")), &before)
	require.NotEmpty(t, before.Queries.Sig)
	require.NotEmpty(t, before.Value)
	assert.NotContains(t, before.Value, "/resourceGroups/",
		"a callback URL addresses the workflow on the service host, not through its resource ID")

	runCLI(t, env.command("resource", "move", "--ids", workflowID, "--destination-group", dstRG))

	movedID := cliResourceID(dstRG, "Microsoft.Logic", "workflows", workflow)
	var after callback
	parseJSON(t, runCLI(t, env.command("rest", "--method", "post",
		"--url", movedID+"/listCallbackUrl?api-version=2019-05-01")), &after)
	assert.Equal(t, before.Queries.Sig, after.Queries.Sig,
		"a cross-group move must not invalidate an outstanding callback URL's signature")
	assert.Equal(t, before.Value, after.Value,
		"a callback URL an operator already holds must survive a cross-group move intact")
	assert.NotContains(t, after.BasePath, srcRG)
	assert.NotContains(t, after.BasePath, dstRG)
}

// TestResourceMoveEventGridPartnerNamespaceCLI moves the one Event Grid
// Partner Events resource that carries access keys and proves both keys
// survive, and that the namespace's channel moved with it.
func TestResourceMoveEventGridPartnerNamespaceCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-eg-partner-move")

	srcRG, dstRG := "cli-move-egp-src-rg", "cli-move-egp-dst-rg"
	const namespace, channel = "climoveegpns", "climoveegpchannel"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))

	namespaceID := cliResourceID(srcRG, "Microsoft.EventGrid", "partnerNamespaces", namespace)
	registration := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.EventGrid/partnerRegistrations/contoso", subscriptionID)
	runCLI(t, env.command("rest", "--method", "put", "--url", namespaceID+"?api-version=2022-06-15",
		"--body", fmt.Sprintf(`{"location":"eastus","properties":{"partnerRegistrationFullyQualifiedId":%q}}`, registration)))
	runCLI(t, env.command("rest", "--method", "put",
		"--url", namespaceID+"/channels/"+channel+"?api-version=2022-06-15",
		"--body", fmt.Sprintf(`{"properties":{"channelType":"PartnerTopic","partnerTopicInfo":{"azureSubscriptionId":%q,"resourceGroupName":%q,"name":"downstream"}}}`,
			subscriptionID, dstRG)))

	type partnerKeys struct {
		Key1 string `json:"key1"`
		Key2 string `json:"key2"`
	}
	var before partnerKeys
	parseJSON(t, runCLI(t, env.command("rest", "--method", "post",
		"--url", namespaceID+"/listKeys?api-version=2022-06-15")), &before)
	require.NotEmpty(t, before.Key1)

	runCLI(t, env.command("resource", "move", "--ids", namespaceID, "--destination-group", dstRG))

	movedID := cliResourceID(dstRG, "Microsoft.EventGrid", "partnerNamespaces", namespace)
	var moved struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("rest", "--method", "get", "--url", movedID+"?api-version=2022-06-15")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/")

	var movedChannel struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("rest", "--method", "get",
		"--url", movedID+"/channels/"+channel+"?api-version=2022-06-15")), &movedChannel)
	assert.Contains(t, movedChannel.ID, "/resourceGroups/"+dstRG+"/",
		"a partner namespace's channels must move with it")

	var after partnerKeys
	parseJSON(t, runCLI(t, env.command("rest", "--method", "post",
		"--url", movedID+"/listKeys?api-version=2022-06-15")), &after)
	assert.Equal(t, before, after, "a cross-group move must not rotate a partner namespace's access keys")
}

// TestResourceMoveVirtualNetworkRepointsLinkCLI moves a virtual network and
// proves the private DNS zone's virtual network link — which did not move —
// names the moved virtual network afterwards rather than the address it left.
func TestResourceMoveVirtualNetworkRepointsLinkCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-vnet-move")

	srcRG, dstRG := "cli-move-vnet-src-rg", "cli-move-vnet-dst-rg"
	const vnet, zone, link = "climovevnet", "climove.internal", "climovelink"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("network", "vnet", "create", "-n", vnet, "-g", srcRG,
		"-l", "eastus", "--address-prefixes", "10.50.0.0/16", "-o", "json"))
	runCLI(t, env.command("network", "private-dns", "zone", "create", "-n", zone, "-g", srcRG, "-o", "json"))

	vnetID := cliResourceID(srcRG, "Microsoft.Network", "virtualNetworks", vnet)
	runCLI(t, env.command("network", "private-dns", "link", "vnet", "create", "-n", link,
		"-g", srcRG, "-z", zone, "-v", vnetID, "-e", "false", "-o", "json"))

	runCLI(t, env.command("resource", "move", "--ids", vnetID, "--destination-group", dstRG))

	var movedVnet struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("network", "vnet", "show", "-n", vnet, "-g", dstRG, "-o", "json")), &movedVnet)
	assert.Contains(t, movedVnet.ID, "/resourceGroups/"+dstRG+"/")
	failure := runStorageCLIExpectFailure(t, env.command("network", "vnet", "show", "-n", vnet, "-g", srcRG, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound")

	var movedLink struct {
		VirtualNetwork struct {
			ID string `json:"id"`
		} `json:"virtualNetwork"`
	}
	parseJSON(t, runCLI(t, env.command("network", "private-dns", "link", "vnet", "show",
		"-n", link, "-g", srcRG, "-z", zone, "-o", "json")), &movedLink)
	assert.Equal(t, movedVnet.ID, movedLink.VirtualNetwork.ID,
		"the virtual network link must name the moved virtual network, not the address it left")
}

// TestResourceMoveRefusesUnsupportedTypesCLI pins the other half of move
// fidelity through the CLI: every type here is one Azure Resource Manager will
// not move between resource groups, so `az resource invoke-action`-style
// validation answers ResourceMoveNotSupported.
func TestResourceMoveRefusesUnsupportedTypesCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-move-refusals")

	srcRG, dstRG := "cli-refuse-move-src-rg", "cli-refuse-move-dst-rg"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))

	validate := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/validateMoveResources?api-version=2021-04-01",
		subscriptionID, srcRG)
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
		id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/refused",
			subscriptionID, srcRG, resourceType)
		body := fmt.Sprintf(`{"resources":[%q],"targetResourceGroup":"/subscriptions/%s/resourceGroups/%s"}`,
			id, subscriptionID, dstRG)
		failure := runStorageCLIExpectFailure(t, env.command("rest", "--method", "post",
			"--url", validate, "--body", body))
		assert.Contains(t, failure, "ResourceMoveNotSupported",
			"%s must be refused the way real Azure refuses it", resourceType)
	}
}

// TestResourceMoveOntoOccupiedIdentifierCLI drives the refusal an operator sees
// from `az resource move` when the destination group already holds a resource
// of the same type and name: the move is refused, and the resource already
// there is still the one that answers.
func TestResourceMoveOntoOccupiedIdentifierCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-move-conflict")

	srcRG, dstRG := "cli-conflict-src-rg", "cli-conflict-dst-rg"
	const workflow = "cliconflictwf"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))

	definition := `{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{},"actions":{},"outputs":{}},"parameters":{}}}`
	sourceID := cliResourceID(srcRG, "Microsoft.Logic", "workflows", workflow)
	occupiedID := cliResourceID(dstRG, "Microsoft.Logic", "workflows", workflow)
	for _, id := range []string{sourceID, occupiedID} {
		runCLI(t, env.command("rest", "--method", "put", "--url", id+"?api-version=2019-05-01", "--body", definition))
	}

	type workflowResource struct {
		Properties struct {
			AccessEndpoint string `json:"accessEndpoint"`
		} `json:"properties"`
	}
	var occupantBefore workflowResource
	parseJSON(t, runCLI(t, env.command("rest", "--method", "get", "--url", occupiedID+"?api-version=2019-05-01")), &occupantBefore)

	failure := runStorageCLIExpectFailure(t,
		env.command("resource", "move", "--ids", sourceID, "--destination-group", dstRG))
	assert.Contains(t, failure, "ResourceMoveProviderValidationFailed",
		"the refusal must be the provider-validation conflict")

	runCLI(t, env.command("rest", "--method", "get", "--url", sourceID+"?api-version=2019-05-01"))
	var occupantAfter workflowResource
	parseJSON(t, runCLI(t, env.command("rest", "--method", "get", "--url", occupiedID+"?api-version=2019-05-01")), &occupantAfter)
	assert.Equal(t, occupantBefore.Properties.AccessEndpoint, occupantAfter.Properties.AccessEndpoint,
		"the refused move must not have replaced the resource already at the destination")
}
