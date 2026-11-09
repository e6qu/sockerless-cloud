package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliGalleryResource struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Tags              map[string]string `json:"tags"`
	ProvisioningState string            `json:"provisioningState"`
	Description       string            `json:"description"`
	Identifier        struct {
		UniqueName string `json:"uniqueName"`
	} `json:"identifier"`
	PublishingProfile struct {
		ExcludeFromLatest bool   `json:"excludeFromLatest"`
		PublishedDate     string `json:"publishedDate"`
	} `json:"publishingProfile"`
	ReplicationStatus *struct {
		AggregatedState string `json:"aggregatedState"`
	} `json:"replicationStatus"`
}

// Azure Compute Gallery through the native CLI: `az sig`, `az sig
// image-definition` and `az sig image-version` create, show, list, update and
// delete; a version captured from a generalized machine, and one copied from
// it, and a machine created from the image definition, which boots the newest
// version latest admits. `az sig ... update` reads and rewrites with PUT;
// gallery applications and sharing answer a declared 501.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/galleries
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/images/{galleryImageName}/versions
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/applications/{galleryApplicationName}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}/share
func TestComputeGalleriesCLI(t *testing.T) {
	t.Parallel()
	env := startAzTLSSimulator(t)
	tagCLILogin(t, env, "sockerless-galleries")

	const rg, vnet, nic, vm = "cli-gallery-rg", "cli-gallery-vnet", "cli-gallery-nic", "cli-gallery-vm"
	const gallery, image = "cligallery", "cli-image"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("network", "vnet", "create", "-g", rg, "-n", vnet, "-l", "eastus",
		"--address-prefixes", "10.162.0.0/16", "--subnet-name", "default", "--subnet-prefixes", "10.162.1.0/24", "-o", "json"))
	createVM := func(name, nicName, imageReference string) string {
		t.Helper()
		runCLI(t, env.command("network", "nic", "create", "-g", rg, "-n", nicName, "-l", "eastus",
			"--vnet-name", vnet, "--subnet", "default", "-o", "json"))
		nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/%s", subscriptionID, rg, nicName)
		vmURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s?api-version=2024-07-01",
			env.baseURL, subscriptionID, rg, name)
		// `az vm create` deploys an ARM template through
		// Microsoft.Resources/deployments, which the simulator does not serve, so
		// write the machine as the template would.
		runCLI(t, env.command("rest", "--method", "PUT", "--url", vmURL, "--body", fmt.Sprintf(`{
			"location": "eastus",
			"properties": {
				"hardwareProfile": {"vmSize": "Standard_B1s"},
				"storageProfile": {
					"imageReference": %s,
					"osDisk": {"name": "%s-osdisk", "createOption": "FromImage", "managedDisk": {"storageAccountType": "Standard_LRS"}}
				},
				"osProfile": {"computerName": "%s", "adminUsername": "azureuser", "adminPassword": "Sockerless!Pass1"},
				"networkProfile": {"networkInterfaces": [{"id": %q}]}
			}
		}`, imageReference, name, name, nicID), "-o", "json"))
		t.Cleanup(func() { _, _, _ = runCLIStreamsResult(env.command("rest", "--method", "DELETE", "--url", vmURL)) })
		runCLI(t, env.command("vm", "wait", "-g", rg, "-n", name, "--created", "--timeout", "300"))
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s", subscriptionID, rg, name)
	}
	inGuest := func(name, command string) string {
		t.Helper()
		var ran struct {
			InstanceView struct {
				Statuses []struct {
					Message string `json:"message"`
				} `json:"statuses"`
			} `json:"instanceView"`
		}
		parseJSON(t, runCLI(t, env.command("vm", "extension", "set", "-g", rg, "--vm-name", name, "-n", "CustomScript",
			"--publisher", "Microsoft.Azure.Extensions", "--version", "2.1",
			"--settings", fmt.Sprintf(`{"commandToExecute": %q}`, command), "-o", "json")), &ran)
		var messages []string
		for _, status := range ran.InstanceView.Statuses {
			messages = append(messages, status.Message)
		}
		return strings.Join(messages, "\n")
	}

	vmID := createVM(vm, nic, `{"publisher": "Debian", "offer": "debian-12", "sku": "12", "version": "latest"}`)
	inGuest(vm, "echo captured-by-az-sig > /var/tmp/gallery-proof")
	runCLI(t, env.command("vm", "deallocate", "-g", rg, "-n", vm))
	runCLI(t, env.command("vm", "generalize", "-g", rg, "-n", vm))

	var created cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "create", "-g", rg, "-r", gallery, "--description", "built by az", "-o", "json")), &created)
	assert.Equal(t, "Succeeded", created.ProvisioningState)
	assert.Equal(t, subscriptionID+"-"+strings.ToUpper(gallery), created.Identifier.UniqueName)

	var definition cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-definition", "create", "-g", rg, "-r", gallery, "-i", image,
		"--publisher", "sockerless", "--offer", "cli", "--sku", "1", "--os-type", "Linux", "--os-state", "Generalized",
		"--hyper-v-generation", "V2", "-o", "json")), &definition)
	assert.Equal(t, "Succeeded", definition.ProvisioningState)

	var first cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-version", "create", "-g", rg, "-r", gallery, "-i", image, "-e", "1.0.0",
		"--virtual-machine", vmID, "-o", "json")), &first)
	assert.Equal(t, "Succeeded", first.ProvisioningState)
	assert.NotEmpty(t, first.PublishingProfile.PublishedDate)

	var shown cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-version", "show", "-g", rg, "-r", gallery, "-i", image, "-e", "1.0.0",
		"--expand", "ReplicationStatus", "-o", "json")), &shown)
	require.NotNil(t, shown.ReplicationStatus, "--expand ReplicationStatus reports the replication")
	assert.Equal(t, "Completed", shown.ReplicationStatus.AggregatedState)

	var copied cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-version", "create", "-g", rg, "-r", gallery, "-i", image, "-e", "1.0.1",
		"--image-version", first.ID, "--exclude-from-latest", "true", "--tags", "channel=stable", "-o", "json")), &copied)
	assert.Equal(t, "Succeeded", copied.ProvisioningState)
	assert.True(t, copied.PublishingProfile.ExcludeFromLatest)

	var tagged cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-version", "update", "-g", rg, "-r", gallery, "-i", image, "-e", "1.0.1",
		"--set", "tags.channel=canary", "-o", "json")), &tagged)
	assert.Equal(t, "canary", tagged.Tags["channel"])
	assert.Equal(t, copied.PublishingProfile.PublishedDate, tagged.PublishingProfile.PublishedDate)

	var versions []cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-version", "list", "-g", rg, "-r", gallery, "-i", image, "-o", "json")), &versions)
	require.Len(t, versions, 2)

	createVM(vm+"-2", nic+"-2", fmt.Sprintf(`{"id": %q}`, definition.ID))
	assert.Contains(t, inGuest(vm+"-2", "cat /var/tmp/gallery-proof"), "captured-by-az-sig",
		"the machine created from the image definition does not carry the captured disk")

	var redescribed cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-definition", "update", "-g", rg, "-r", gallery, "-i", image,
		"--set", "description=debian with a proof file", "-o", "json")), &redescribed)
	assert.Equal(t, "debian with a proof file", redescribed.Description)
	_, stderr, err := runCLIStreamsResult(env.command("sig", "image-definition", "update", "-g", rg, "-r", gallery, "-i", image,
		"--set", "osState=Specialized", "-o", "json"))
	require.Error(t, err, "an image definition's osState is fixed at creation")
	assert.Contains(t, stderr, "PropertyChangeNotAllowed")

	var definitions []cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "image-definition", "list", "-g", rg, "-r", gallery, "-o", "json")), &definitions)
	require.Len(t, definitions, 1)
	assert.Equal(t, image, definitions[0].Name)

	var updatedGallery cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "update", "-g", rg, "-r", gallery, "--set", "description=updated", "-o", "json")), &updatedGallery)
	assert.Equal(t, "updated", updatedGallery.Description)
	var galleries []cliGalleryResource
	parseJSON(t, runCLI(t, env.command("sig", "list", "-g", rg, "-o", "json")), &galleries)
	require.Len(t, galleries, 1)
	parseJSON(t, runCLI(t, env.command("sig", "list", "-o", "json")), &galleries)
	assert.Contains(t, galleryIDs(galleries), created.ID)

	_, stderr, err = runCLIStreamsResult(env.command("sig", "delete", "-g", rg, "-r", gallery))
	require.Error(t, err, "a gallery holding image definitions cannot be deleted")
	assert.Contains(t, stderr, "CannotDeleteResource")

	_, stderr, err = runCLIStreamsResult(env.command("sig", "gallery-application", "create", "-g", rg, "-r", gallery,
		"-n", "app", "--os-type", "Linux", "-o", "json"))
	require.Error(t, err, "the simulator installs no gallery applications")
	assert.Contains(t, stderr, "NotImplemented")
	_, stderr, err = runCLIStreamsResult(env.command("sig", "share", "add", "-g", rg, "-r", gallery,
		"--subscription-ids", subscriptionID, "-o", "json"))
	require.Error(t, err, "the simulator serves no shared galleries")
	assert.Contains(t, stderr, "NotImplemented")

	for _, version := range []string{"1.0.1", "1.0.0"} {
		runCLI(t, env.command("sig", "image-version", "delete", "-g", rg, "-r", gallery, "-i", image, "-e", version))
	}
	runCLI(t, env.command("sig", "image-definition", "delete", "-g", rg, "-r", gallery, "-i", image))
	runCLI(t, env.command("sig", "delete", "-g", rg, "-r", gallery))
	parseJSON(t, runCLI(t, env.command("sig", "list", "-g", rg, "-o", "json")), &galleries)
	assert.Empty(t, galleries)
}

func galleryIDs(galleries []cliGalleryResource) []string {
	ids := make([]string, 0, len(galleries))
	for _, g := range galleries {
		ids = append(ids, g.ID)
	}
	return ids
}
