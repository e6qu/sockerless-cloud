package azure_cli_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// The marketplace catalog the Compute resource provider lists under
// locations/{location}/publishers, read the way `az vm image list --all`
// walks it: publishers, then an offer's SKUs, then a SKU's versions, then one
// version.
func TestComputeVirtualMachineImagesCLI(t *testing.T) {
	t.Parallel()
	catalog := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Compute/locations/eastus/publishers", baseURL, subscriptionID)
	const apiVersion = "?api-version=2022-03-01"
	if out := runCLI(t, azRest("GET", catalog+apiVersion, "")); !strings.Contains(out, "Canonical") || !strings.Contains(out, "Debian") {
		t.Fatalf("expected the Canonical and Debian publishers, got %s", out)
	}
	if out := runCLI(t, azRest("GET", catalog+"/Canonical/artifacttypes/vmimage/offers"+apiVersion, "")); !strings.Contains(out, "ubuntu-24_04-lts") {
		t.Fatalf("expected the ubuntu-24_04-lts offer, got %s", out)
	}
	if out := runCLI(t, azRest("GET", catalog+"/Canonical/artifacttypes/vmimage/offers/ubuntu-24_04-lts/skus"+apiVersion, "")); !strings.Contains(out, `"server"`) {
		t.Fatalf("expected the server SKU, got %s", out)
	}
	var versions []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, azRest("GET", catalog+"/Debian/artifacttypes/vmimage/offers/debian-12/skus/12/versions"+apiVersion, "")), &versions)
	if len(versions) == 0 {
		t.Fatal("the catalog lists no Debian 12 version")
	}
	// terraform-provider-azurerm spells the artifact segments artifactTypes
	// and vmImage, which Azure Resource Manager answers like the lowercase.
	if out := runCLI(t, azRest("GET", catalog+"/Debian/artifactTypes/vmImage/offers/debian-12/skus/12/versions"+apiVersion, "")); !strings.Contains(out, versions[0].Name) {
		t.Fatalf("the camel-cased catalog path must list the same versions, got %s", out)
	}
	out := runCLI(t, azRest("GET", catalog+"/Debian/artifacttypes/vmimage/offers/debian-12/skus/12/versions/"+versions[0].Name+apiVersion, ""))
	if !strings.Contains(out, `"architecture": "x64"`) || !strings.Contains(out, `"operatingSystem": "Linux"`) {
		t.Fatalf("expected the Debian 12 image's properties, got %s", out)
	}
	cmd := azRest("GET", catalog+"/Canonical/artifacttypes/vmimage/offers/0001-com-ubuntu-server-jammy/skus"+apiVersion, "")
	if stdout, stderr, err := runCLIStreamsResult(cmd); err == nil || !strings.Contains(stdout+stderr, "NotFound") {
		t.Fatalf("an offer the catalog does not carry must answer NotFound, got %v: %s %s", err, stdout, stderr)
	}
}

// A machine runs its customData, a deallocate keeps its disk, and an image
// created from the generalized machine lists under Microsoft.Compute/images.
func TestComputeVirtualMachineCustomDataAndImageCLI(t *testing.T) {
	t.Parallel()

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-image-vnet", "2024-05-01")
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.99.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-image-vnet/subnets/cli-image-subnet", "2024-05-01")
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.99.1.0/24"}}`))
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-image-vnet/subnets/cli-image-subnet",
		subscriptionID, resourceGroup)
	nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/cli-image-nic",
		subscriptionID, resourceGroup)
	runCLI(t, azRest("PUT", armURL("Microsoft.Network", "networkInterfaces/cli-image-nic", "2024-05-01"), fmt.Sprintf(
		`{"location":"eastus","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q},"privateIPAllocationMethod":"Dynamic","privateIPAddressVersion":"IPv4","primary":true}}]}}`,
		subnetID)))

	vmPath := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/cli-image-vm",
		baseURL, subscriptionID, resourceGroup)
	customData := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho cli-custom-data > /var/tmp/cli-custom-data\n"))
	azRestLongRunning(t, "PUT", vmPath+"?api-version=2022-03-01", fmt.Sprintf(
		`{"location":"eastus","properties":{"hardwareProfile":{"vmSize":"Standard_B1s"},`+
			`"storageProfile":{"imageReference":{"publisher":"Canonical","offer":"ubuntu-24_04-lts","sku":"server","version":"latest"},"osDisk":{"name":"cli-image-osdisk","createOption":"FromImage","managedDisk":{"storageAccountType":"Standard_LRS"}}},`+
			`"osProfile":{"computerName":"cli-image-vm","adminUsername":"azureuser","adminPassword":"Str0ng-password-12345!","customData":%q},`+
			`"networkProfile":{"networkInterfaces":[{"id":%q,"properties":{"primary":true}}]}}}`, customData, nicID))

	extURL := vmPath + "/extensions/read?api-version=2022-03-01"
	out := runCLI(t, azRest("PUT", extURL, `{"location":"eastus","properties":{`+
		`"publisher":"Microsoft.Azure.Extensions","type":"CustomScript","typeHandlerVersion":"2.1",`+
		`"settings":{"commandToExecute":"cat /var/tmp/cli-custom-data"}}}`))
	if !strings.Contains(out, "cli-custom-data") {
		t.Fatalf("customData did not run in the guest: %s", out)
	}

	azRestLongRunning(t, "POST", vmPath+"/deallocate?api-version=2022-03-01", "")
	azRestLongRunning(t, "POST", vmPath+"/start?api-version=2022-03-01", "")
	out = runCLI(t, azRest("PUT", vmPath+"/extensions/reread?api-version=2022-03-01", `{"location":"eastus","properties":{`+
		`"publisher":"Microsoft.Azure.Extensions","type":"CustomScript","typeHandlerVersion":"2.1",`+
		`"settings":{"commandToExecute":"cat /var/tmp/cli-custom-data"}}}`))
	if !strings.Contains(out, "cli-custom-data") {
		t.Fatalf("the machine did not start again on the disk it was deallocated with: %s", out)
	}

	azRestLongRunning(t, "POST", vmPath+"/deallocate?api-version=2022-03-01", "")
	runCLI(t, azRest("POST", vmPath+"/generalize?api-version=2022-03-01", ""))
	vmID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/cli-image-vm", subscriptionID, resourceGroup)
	imageURL := armURL("Microsoft.Compute", "images/cli-image", "2022-03-01")
	out = runCLI(t, azRest("PUT", imageURL, fmt.Sprintf(`{"location":"eastus","properties":{"sourceVirtualMachine":{"id":%q}}}`, vmID)))
	if !strings.Contains(out, `"provisioningState": "Succeeded"`) {
		t.Fatalf("expected the image to provision from the generalized machine, got %s", out)
	}
	if out := runCLI(t, azRest("GET", armURL("Microsoft.Compute", "images", "2022-03-01"), "")); !strings.Contains(out, "cli-image") {
		t.Fatalf("expected the image in the resource group's list, got %s", out)
	}
	if out := runCLI(t, azRest("PATCH", imageURL, `{"tags":{"source":"cli"}}`)); !strings.Contains(out, `"source": "cli"`) {
		t.Fatalf("expected the image's tags to update, got %s", out)
	}
	listAllURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Compute/images?api-version=2022-03-01", baseURL, subscriptionID)
	if out := runCLI(t, azRest("GET", listAllURL, "")); !strings.Contains(out, "cli-image") {
		t.Fatalf("expected the image in the subscription's list, got %s", out)
	}
	if out := runCLI(t, azRest("GET", imageURL, "")); !strings.Contains(out, "cli-image") {
		t.Fatalf("expected the image to read back, got %s", out)
	}
	runCLI(t, azRest("DELETE", imageURL, ""))
	runCLI(t, azRest("DELETE", vmPath+"?api-version=2022-03-01", ""))
}
