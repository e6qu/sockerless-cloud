package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the cross-resource-group move families that joined the
// dispatch alongside Microsoft.ServiceBus. Each one derives its credential
// material from the resource ID, which embeds the resource group, so `az
// resource move` has to re-home the records without rotating the credential.
// Routes exercised:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources
//	POST …/providers/Microsoft.EventHub/namespaces/{namespaceName}/authorizationRules/{authorizationRuleName}/listKeys
//	POST …/providers/Microsoft.Cache/Redis/{name}/listKeys
//	POST …/providers/Microsoft.ContainerRegistry/registries/{registryName}/listCredentials
//	POST …/providers/Microsoft.EventGrid/topics/{topicName}/listKeys

// TestResourceMoveEventHubNamespaceCLI moves an Event Hubs namespace with
// `az resource move` and proves the credential survived: the connection
// strings `az eventhubs namespace authorization-rule keys list` prints are
// byte-identical afterwards, and the event hub child moved with the namespace.
func TestResourceMoveEventHubNamespaceCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-eh-move")

	srcRG, dstRG := "cli-move-eh-src-rg", "cli-move-eh-dst-rg"
	const namespace, hub = "climoveehns", "climoveehhub"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("eventhubs", "namespace", "create", "-n", namespace, "-g", srcRG,
		"-l", "eastus", "--sku", "Standard", "--tags", "origin=resource-create", "-o", "json"))
	runCLI(t, env.command("eventhubs", "eventhub", "create", "-n", hub,
		"--namespace-name", namespace, "-g", srcRG, "-o", "json"))

	type messagingKeys struct {
		KeyName                   string `json:"keyName"`
		PrimaryKey                string `json:"primaryKey"`
		SecondaryKey              string `json:"secondaryKey"`
		PrimaryConnectionString   string `json:"primaryConnectionString"`
		SecondaryConnectionString string `json:"secondaryConnectionString"`
	}
	var before messagingKeys
	parseJSON(t, runCLI(t, env.command("eventhubs", "namespace", "authorization-rule", "keys", "list",
		"--authorization-rule-name", "RootManageSharedAccessKey", "--namespace-name", namespace, "-g", srcRG, "-o", "json")), &before)
	require.NotEmpty(t, before.PrimaryConnectionString)

	namespaceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventHub/namespaces/%s",
		subscriptionID, srcRG, namespace)
	runCLI(t, env.command("resource", "move", "--ids", namespaceID, "--destination-group", dstRG))

	var moved struct {
		ID   string            `json:"id"`
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, runCLI(t, env.command("eventhubs", "namespace", "show",
		"-n", namespace, "-g", dstRG, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/")
	assert.Equal(t, "resource-create", moved.Tags["origin"], "the namespace's tags must move with it")
	failure := runStorageCLIExpectFailure(t, env.command("eventhubs", "namespace", "show",
		"-n", namespace, "-g", srcRG, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound")

	var movedHub struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("eventhubs", "eventhub", "show", "-n", hub,
		"--namespace-name", namespace, "-g", dstRG, "-o", "json")), &movedHub)
	assert.Contains(t, movedHub.ID, "/resourceGroups/"+dstRG+"/")

	var after messagingKeys
	parseJSON(t, runCLI(t, env.command("eventhubs", "namespace", "authorization-rule", "keys", "list",
		"--authorization-rule-name", "RootManageSharedAccessKey", "--namespace-name", namespace, "-g", dstRG, "-o", "json")), &after)
	assert.Equal(t, before, after,
		"a cross-group move must not rotate the namespace's keys or connection strings")

	var regenerated messagingKeys
	parseJSON(t, runCLI(t, env.command("eventhubs", "namespace", "authorization-rule", "keys", "renew",
		"--authorization-rule-name", "RootManageSharedAccessKey", "--namespace-name", namespace, "-g", dstRG,
		"--key", "PrimaryKey", "-o", "json")), &regenerated)
	assert.NotEqual(t, before.PrimaryKey, regenerated.PrimaryKey,
		"renewing the primary key after the move must serve new material")
}

// TestResourceMoveRedisCacheCLI moves an Azure Cache for Redis instance and
// proves `az redis list-keys` serves the same password afterwards. The
// simulator implements no Redis data plane, so the keys the control plane
// serves are as far as the credential proof reaches.
func TestResourceMoveRedisCacheCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-redis-move")

	srcRG, dstRG := "cli-move-redis-src-rg", "cli-move-redis-dst-rg"
	const cache = "climoveredis"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("redis", "create", "-n", cache, "-g", srcRG, "-l", "eastus",
		"--sku", "Basic", "--vm-size", "c0", "-o", "json"))

	type redisKeys struct {
		PrimaryKey   string `json:"primaryKey"`
		SecondaryKey string `json:"secondaryKey"`
	}
	var before redisKeys
	parseJSON(t, runCLI(t, env.command("redis", "list-keys", "-n", cache, "-g", srcRG, "-o", "json")), &before)
	require.NotEmpty(t, before.PrimaryKey)

	cacheID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/%s",
		subscriptionID, srcRG, cache)
	runCLI(t, env.command("resource", "move", "--ids", cacheID, "--destination-group", dstRG))

	var moved struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("redis", "show", "-n", cache, "-g", dstRG, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/")
	failure := runStorageCLIExpectFailure(t, env.command("redis", "show", "-n", cache, "-g", srcRG, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound")

	var after redisKeys
	parseJSON(t, runCLI(t, env.command("redis", "list-keys", "-n", cache, "-g", dstRG, "-o", "json")), &after)
	assert.Equal(t, before, after, "a cross-group move must not rotate the cache's access keys")
}

// TestResourceMoveContainerRegistryCLI moves a container registry and proves
// `az acr credential show` serves the same admin username and passwords
// afterwards, so a docker login captured before the move still works.
func TestResourceMoveContainerRegistryCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-acr-move")

	srcRG, dstRG := "cli-move-acr-src-rg", "cli-move-acr-dst-rg"
	const registry = "climoveacr"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("acr", "create", "-n", registry, "-g", srcRG, "-l", "eastus",
		"--sku", "Basic", "--admin-enabled", "true", "-o", "json"))

	type acrCredentials struct {
		Username  string `json:"username"`
		Passwords []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"passwords"`
	}
	var before acrCredentials
	parseJSON(t, runCLI(t, env.command("acr", "credential", "show", "-n", registry, "-g", srcRG, "-o", "json")), &before)
	require.NotEmpty(t, before.Username)
	require.Len(t, before.Passwords, 2)

	registryID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s",
		subscriptionID, srcRG, registry)
	runCLI(t, env.command("resource", "move", "--ids", registryID, "--destination-group", dstRG))

	var moved struct {
		ID          string `json:"id"`
		LoginServer string `json:"loginServer"`
	}
	parseJSON(t, runCLI(t, env.command("acr", "show", "-n", registry, "-g", dstRG, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/")
	assert.NotEmpty(t, moved.LoginServer, "the login server is derived from the registry name, not its group")

	var after acrCredentials
	parseJSON(t, runCLI(t, env.command("acr", "credential", "show", "-n", registry, "-g", dstRG, "-o", "json")), &after)
	assert.Equal(t, before, after, "a cross-group move must not rotate the registry's admin credentials")
}

// TestResourceMoveEventGridTopicCLI moves an Event Grid custom topic and
// proves both halves of that hook: `az eventgrid topic key list` serves the
// same keys afterwards, and the event subscription's inbound reference to the
// topic was rewritten onto the destination resource ID.
func TestResourceMoveEventGridTopicCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-eg-move")

	srcRG, dstRG := "cli-move-eg-src-rg", "cli-move-eg-dst-rg"
	const topic = "climoveegtopic"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("eventgrid", "topic", "create", "-n", topic, "-g", srcRG, "-l", "eastus", "-o", "json"))

	topicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/topics/%s",
		subscriptionID, srcRG, topic)
	runCLI(t, env.command("eventgrid", "event-subscription", "create", "-n", "climoveegsub",
		"--source-resource-id", topicID, "--endpoint-type", "webhook",
		"--endpoint", "http://127.0.0.1:1/unused", "-o", "json"))

	type eventGridKeys struct {
		Key1 string `json:"key1"`
		Key2 string `json:"key2"`
	}
	var before eventGridKeys
	parseJSON(t, runCLI(t, env.command("eventgrid", "topic", "key", "list", "-n", topic, "-g", srcRG, "-o", "json")), &before)
	require.NotEmpty(t, before.Key1)

	runCLI(t, env.command("resource", "move", "--ids", topicID, "--destination-group", dstRG))

	var moved struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("eventgrid", "topic", "show", "-n", topic, "-g", dstRG, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/")
	failure := runStorageCLIExpectFailure(t, env.command("eventgrid", "topic", "show", "-n", topic, "-g", srcRG, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound")

	var movedSub struct {
		Topic string `json:"topic"`
	}
	parseJSON(t, runCLI(t, env.command("eventgrid", "event-subscription", "show", "-n", "climoveegsub",
		"--source-resource-id", moved.ID, "-o", "json")), &movedSub)
	assert.Equal(t, moved.ID, movedSub.Topic,
		"the event subscription must name the moved topic, not its pre-move resource ID")

	var after eventGridKeys
	parseJSON(t, runCLI(t, env.command("eventgrid", "topic", "key", "list", "-n", topic, "-g", dstRG, "-o", "json")), &after)
	assert.Equal(t, before, after, "a cross-group move must not rotate the topic's access keys")
}
