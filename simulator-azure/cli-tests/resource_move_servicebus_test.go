package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the Microsoft.ServiceBus cross-resource-group move: the
// native `az resource move` re-homes a namespace, and the credential an
// operator holds survives it — a namespace's Shared Access Signature keys are
// derived from each authorization rule's resource ID, which embeds the resource
// group, so a move that only re-homed the records would silently rotate every
// connection string. Routes exercised:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ServiceBus/namespaces/{namespaceName}
//	POST …/namespaces/{namespaceName}/authorizationRules/{authorizationRuleName}/listKeys
//	POST …/namespaces/{namespaceName}/queues/{queueName}/authorizationRules/{authorizationRuleName}/listKeys

// sbMoveKeys is the AccessKeys body `az servicebus … authorization-rule keys
// list` prints.
type sbMoveKeys struct {
	KeyName                   string `json:"keyName"`
	PrimaryKey                string `json:"primaryKey"`
	SecondaryKey              string `json:"secondaryKey"`
	PrimaryConnectionString   string `json:"primaryConnectionString"`
	SecondaryConnectionString string `json:"secondaryConnectionString"`
}

// TestResourceMoveServiceBusNamespaceCLI moves a Service Bus namespace between
// resource groups with `az resource move` and proves the move kept it
// coherent: `az servicebus namespace show` answers under the destination group
// only, the queue child moved with it, the tags moved with it, and every
// authorization rule serves byte-identical keys and connection strings.
func TestResourceMoveServiceBusNamespaceCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-sb-move")

	srcRG, dstRG := "cli-move-sb-src-rg", "cli-move-sb-dst-rg"
	const namespace, queue = "climovesbns", "climovesbqueue"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("servicebus", "namespace", "create", "-n", namespace, "-g", srcRG,
		"-l", "eastus", "--sku", "Standard", "--tags", "origin=resource-create", "-o", "json"))
	runCLI(t, env.command("servicebus", "queue", "create", "-n", queue,
		"--namespace-name", namespace, "-g", srcRG, "-o", "json"))
	runCLI(t, env.command("servicebus", "queue", "authorization-rule", "create",
		"-n", "sender", "--queue-name", queue, "--namespace-name", namespace, "-g", srcRG,
		"--rights", "Send", "Listen", "-o", "json"))

	var nsKeysBefore, queueKeysBefore sbMoveKeys
	parseJSON(t, runCLI(t, env.command("servicebus", "namespace", "authorization-rule", "keys", "list",
		"-n", "RootManageSharedAccessKey", "--namespace-name", namespace, "-g", srcRG, "-o", "json")), &nsKeysBefore)
	require.NotEmpty(t, nsKeysBefore.PrimaryKey)
	require.NotEmpty(t, nsKeysBefore.PrimaryConnectionString)
	parseJSON(t, runCLI(t, env.command("servicebus", "queue", "authorization-rule", "keys", "list",
		"-n", "sender", "--queue-name", queue, "--namespace-name", namespace, "-g", srcRG, "-o", "json")), &queueKeysBefore)
	require.NotEmpty(t, queueKeysBefore.PrimaryKey)

	// A tag written through `az tag` at the namespace's scope before the move.
	namespaceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ServiceBus/namespaces/%s",
		subscriptionID, srcRG, namespace)
	runCLI(t, env.command("tag", "update", "--resource-id", namespaceID,
		"--operation", "Merge", "--tags", "team=messaging", "-o", "json"))

	// The move itself, by resource ID.
	runCLI(t, env.command("resource", "move", "--ids", namespaceID, "--destination-group", dstRG))

	// The ARM record relocated.
	var moved struct {
		ID   string            `json:"id"`
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, runCLI(t, env.command("servicebus", "namespace", "show",
		"-n", namespace, "-g", dstRG, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/",
		"the namespace moved into the destination group")
	assert.Equal(t, map[string]string{"origin": "resource-create", "team": "messaging"}, moved.Tags,
		"the namespace's tags must move with it")
	failure := runStorageCLIExpectFailure(t, env.command("servicebus", "namespace", "show",
		"-n", namespace, "-g", srcRG, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound",
		"the moved namespace must no longer resolve under the source group")

	// The queue child moved with it.
	var movedQueue struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("servicebus", "queue", "show", "-n", queue,
		"--namespace-name", namespace, "-g", dstRG, "-o", "json")), &movedQueue)
	assert.Contains(t, movedQueue.ID, "/resourceGroups/"+dstRG+"/")

	// A move never rotates a namespace's keys, so every connection string an
	// operator already holds keeps working verbatim.
	var nsKeysAfter, queueKeysAfter sbMoveKeys
	parseJSON(t, runCLI(t, env.command("servicebus", "namespace", "authorization-rule", "keys", "list",
		"-n", "RootManageSharedAccessKey", "--namespace-name", namespace, "-g", dstRG, "-o", "json")), &nsKeysAfter)
	assert.Equal(t, nsKeysBefore, nsKeysAfter,
		"a cross-group move must not rotate the namespace's keys or connection strings")
	parseJSON(t, runCLI(t, env.command("servicebus", "queue", "authorization-rule", "keys", "list",
		"-n", "sender", "--queue-name", queue, "--namespace-name", namespace, "-g", dstRG, "-o", "json")), &queueKeysAfter)
	assert.Equal(t, queueKeysBefore, queueKeysAfter,
		"a cross-group move must not rotate a queue rule's keys either")

	// A rotation after the move still yields new material, so the pin did not
	// freeze the rotation contract.
	var regenerated sbMoveKeys
	parseJSON(t, runCLI(t, env.command("servicebus", "namespace", "authorization-rule", "keys", "renew",
		"-n", "RootManageSharedAccessKey", "--namespace-name", namespace, "-g", dstRG,
		"--key", "PrimaryKey", "-o", "json")), &regenerated)
	assert.NotEqual(t, nsKeysBefore.PrimaryKey, regenerated.PrimaryKey,
		"renewing the primary key after the move must serve new material")

	// A type Azure Resource Manager does not move is still refused.
	aciID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerInstance/containerGroups/cli-move-sb-never-movable",
		subscriptionID, srcRG)
	refused := runStorageCLIExpectFailure(t, env.command("resource", "move",
		"--ids", aciID, "--destination-group", dstRG))
	assert.Contains(t, refused, "ResourceMoveNotSupported",
		"an unmovable type must still be refused after the Service Bus hook landed")
}
