package azure_sdk_test

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A machine's imageReference resolves against the marketplace catalog under
// locations/{location}/publishers, and an image the catalog lacks is refused
// with the resource provider's own NotFound.
func TestCompute_VirtualMachineImagesCatalog(t *testing.T) {
	t.Parallel()
	images, err := armcompute.NewVirtualMachineImagesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	publishers, err := images.ListPublishers(ctx, "eastus", nil)
	require.NoError(t, err)
	var names []string
	for _, p := range publishers.VirtualMachineImageResourceArray {
		names = append(names, derefString(p.Name))
	}
	assert.Contains(t, names, "Canonical")
	assert.Contains(t, names, "Debian")

	offers, err := images.ListOffers(ctx, "eastus", "Canonical", nil)
	require.NoError(t, err)
	names = names[:0]
	for _, o := range offers.VirtualMachineImageResourceArray {
		names = append(names, derefString(o.Name))
	}
	assert.Contains(t, names, "ubuntu-24_04-lts")

	skus, err := images.ListSKUs(ctx, "eastus", "Canonical", "ubuntu-24_04-lts", nil)
	require.NoError(t, err)
	names = names[:0]
	for _, s := range skus.VirtualMachineImageResourceArray {
		names = append(names, derefString(s.Name))
	}
	assert.ElementsMatch(t, []string{"server", "server-arm64"}, names)

	versions, err := images.List(ctx, "eastus", "Debian", "debian-12", "12", nil)
	require.NoError(t, err)
	require.NotEmpty(t, versions.VirtualMachineImageResourceArray)
	version := derefString(versions.VirtualMachineImageResourceArray[0].Name)
	got, err := images.Get(ctx, "eastus", "Debian", "debian-12", "12", version, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.Architecture)
	assert.Equal(t, armcompute.ArchitectureTypesX64, *got.Properties.Architecture)
	assert.Equal(t, armcompute.OperatingSystemTypesLinux, *got.Properties.OSDiskImage.OperatingSystem)

	_, err = images.ListSKUs(ctx, "eastus", "Canonical", "0001-com-ubuntu-server-jammy", nil)
	require.Error(t, err, "an offer the catalog does not carry is not found")
	assert.Contains(t, err.Error(), "NotFound")
}

// The Compute resource provider refuses an unresolvable image at request
// validation, never as a failed operation.
func TestCompute_VirtualMachineWithUnknownImageIsRefused(t *testing.T) {
	t.Parallel()
	const rg = "vm-noimage-rg"
	nicID := vmOperationsFixture(t, rg, "vm-noimage", "10.100.0.0/16")
	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = vmClient.BeginCreateOrUpdate(ctx, rg, "vm-noimage", armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypesStandardB1S)},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{
					Publisher: to.Ptr("Canonical"), Offer: to.Ptr("0001-com-ubuntu-server-jammy"), SKU: to.Ptr("22_04-lts"), Version: to.Ptr("latest"),
				},
				OSDisk: &armcompute.OSDisk{CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage)},
			},
			OSProfile:      &armcompute.OSProfile{ComputerName: to.Ptr("vm-noimage"), AdminUsername: to.Ptr("azureuser"), AdminPassword: to.Ptr("Sockerless!Pass1")},
			NetworkProfile: &armcompute.NetworkProfile{NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{ID: to.Ptr(nicID)}}},
		},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PlatformImageNotFound")
}

// runInGuest runs a command through the Custom Script extension and returns
// what it wrote.
func runInGuest(t *testing.T, rg, vmName, name, command string) string {
	t.Helper()
	extClient, err := armcompute.NewVirtualMachineExtensionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := extClient.BeginCreateOrUpdate(ctx, rg, vmName, name, armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.Extensions"),
			Type:               to.Ptr("CustomScript"),
			TypeHandlerVersion: to.Ptr("2.1"),
			Settings:           map[string]any{"commandToExecute": command},
		},
	}, nil)
	require.NoError(t, err)
	ran, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err, "the command failed in the guest")
	var output []string
	for _, status := range ran.Properties.InstanceView.Statuses {
		output = append(output, derefString(status.Message))
	}
	return strings.Join(output, "\n")
}

// A machine runs its customData script once at boot and reaches the Instance
// Metadata Service through its own network.
func TestCompute_VirtualMachineRunsCustomDataAndReadsItsMetadata(t *testing.T) {
	t.Parallel()
	const rg = "vm-init-rg"
	const vmName = "vm-init"
	nicID := vmOperationsFixture(t, rg, "vm-init", "10.101.0.0/16")
	script := "#!/bin/sh\necho custom-data-ran > /var/tmp/custom-data\n"
	createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.OSProfile.CustomData = to.Ptr(base64.StdEncoding.EncodeToString([]byte(script)))
		vm.Properties.UserData = to.Ptr(base64.StdEncoding.EncodeToString([]byte("for-imds-only")))
		vm.Identity = &armcompute.VirtualMachineIdentity{Type: to.Ptr(armcompute.ResourceIdentityTypeSystemAssigned)}
	})

	ran := runInGuest(t, rg, vmName, "custom-data", "cat /var/tmp/custom-data; cat /var/log/cloud-init-output.log")
	assert.Contains(t, ran, "custom-data-ran", "customData did not run at boot")
	assert.Contains(t, ran, "Cloud-init v. 22.4.2 finished at ")
	assert.Contains(t, ran, "Datasource DataSourceAzure [seed=/dev/sr0].")

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic, err := nicClient.Get(ctx, rg, "vm-init-nic", nil)
	require.NoError(t, err)
	address := derefString(nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress)

	// Reach the Instance Metadata Service over plain TCP from a shell with no
	// HTTP client.
	read := func(step, path string) string {
		return runInGuest(t, rg, vmName, step, `bash -c 'host=${AZURE_INSTANCE_METADATA_ENDPOINT#http://}; host=${host%%/*}; `+
			`exec 3<>/dev/tcp/$host/80; printf "GET `+path+` HTTP/1.0\r\nHost: 169.254.169.254\r\nMetadata: true\r\n\r\n" >&3; cat <&3'`)
	}
	imds := read("imds", "/metadata/instance?api-version=2021-02-01")
	assert.Contains(t, imds, `"name":"`+vmName+`"`, "the metadata service did not answer with the machine's own identity")
	assert.Contains(t, imds, `"privateIpAddress":"`+address+`"`, "the metadata service did not answer with the machine's own interface")
	assert.Contains(t, imds, `"userData":"`+base64.StdEncoding.EncodeToString([]byte("for-imds-only"))+`"`)
	assert.Contains(t, imds, `"offer":"debian-12"`)

	// The guest's MAC is the one the network interface resource reports.
	mac := strings.ReplaceAll(derefString(nic.Properties.MacAddress), "-", "")
	require.NotEmpty(t, mac)
	assert.Contains(t, imds, `"macAddress":"`+mac+`"`)
	eth0 := runInGuest(t, rg, vmName, "mac", "cat /sys/class/net/eth0/address | tr -d :")
	assert.Contains(t, eth0, "\n"+strings.ToLower(mac)+"\n", "the guest's interface carries the MAC the network interface reports")

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vm, err := vmClient.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	compute := read("compute", "/metadata/instance/compute?api-version=2021-02-01")
	assert.Contains(t, compute, `"vmId":"`+derefString(vm.Properties.VMID)+`"`)
	assert.Contains(t, compute, `"resourceGroupName":"`+rg+`"`)
	assert.Contains(t, compute, `"subscriptionId":"`+subscriptionID+`"`)
	assert.NotContains(t, compute, `"network"`, "the compute path answers the compute section alone")
	network := read("network", "/metadata/instance/network?api-version=2021-02-01")
	assert.Contains(t, network, `"privateIpAddress":"`+address+`"`)
	assert.Contains(t, network, `"macAddress":"`+mac+`"`)
	attested := read("attested", "/metadata/attested/document?api-version=2020-09-01&nonce=1234567890")
	assert.Contains(t, attested, `"encoding":"pkcs7"`)
	// The nonce is inside what was signed, so a document minted for one challenge
	// cannot answer another.
	signed := regexp.MustCompile(`"signature":"([^"]+)"`)
	first := signed.FindStringSubmatch(attested)
	require.Len(t, first, 2, "the attested document carries a signature: %s", attested)
	other := signed.FindStringSubmatch(read("attested-other", "/metadata/attested/document?api-version=2020-09-01&nonce=9876543210"))
	require.Len(t, other, 2)
	assert.NotEqual(t, first[1], other[1], "the caller's nonce must change the document that was signed")
	identity := read("identity", "/metadata/identity/info?api-version=2021-02-01")
	require.NotNil(t, vm.Identity)
	assert.Contains(t, identity, `"tenantId":"`+derefString(vm.Identity.TenantID)+`"`, "the metadata service names the tenant of the machine's identity")
}

// A deallocated machine keeps its OS disk; an image from the generalized
// machine carries that disk, and a machine from the image boots it.
func TestCompute_VirtualMachineDeallocateKeepsTheOSDiskAndAnImageCapturesIt(t *testing.T) {
	t.Parallel()
	const rg = "vm-disk-rg"
	const vmName = "vm-disk"
	nicID := vmOperationsFixture(t, rg, "vm-disk", "10.102.0.0/16")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)

	runInGuest(t, rg, vmName, "write", "echo kept-across-deallocate > /var/tmp/kept")

	deallocate, err := vmClient.BeginDeallocate(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = deallocate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
	require.NoError(t, err)
	assert.True(t, containsVMStatus(view.Statuses, "PowerState/deallocated"))

	start, err := vmClient.BeginStart(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = start.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, runInGuest(t, rg, vmName, "read", "cat /var/tmp/kept"), "kept-across-deallocate",
		"the machine did not start again on the disk it was deallocated with")

	deallocate, err = vmClient.BeginDeallocate(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = deallocate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = vmClient.Generalize(ctx, rg, vmName, nil)
	require.NoError(t, err)

	imagesClient, err := armcompute.NewImagesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vmID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s", subscriptionID, rg, vmName)
	imagePoller, err := imagesClient.BeginCreateOrUpdate(ctx, rg, "vm-disk-image", armcompute.Image{
		Location:   to.Ptr("eastus"),
		Properties: &armcompute.ImageProperties{SourceVirtualMachine: &armcompute.SubResource{ID: to.Ptr(vmID)}},
	}, nil)
	require.NoError(t, err)
	image, err := imagePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, image.ID)
	assert.Equal(t, "Succeeded", derefString(image.Properties.ProvisioningState))
	t.Cleanup(func() {
		if del, err := imagesClient.BeginDelete(ctx, rg, "vm-disk-image", nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	listed := imagesClient.NewListByResourceGroupPager(rg, nil)
	require.True(t, listed.More())
	page, err := listed.NextPage(ctx)
	require.NoError(t, err)
	var names []string
	for _, i := range page.Value {
		names = append(names, derefString(i.Name))
	}
	assert.Contains(t, names, "vm-disk-image")

	// The file written into the source machine is on the new machine's disk.
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/vm-disk-vnet/subnets/vm-disk-subnet", subscriptionID, rg)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "vm-disk-nic-2", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                    &armnetwork.Subnet{ID: to.Ptr(subnetID)},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					Primary:                   to.Ptr(true),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	nic2, err := nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	createOperationsVM(t, rg, vmName+"-2", *nic2.ID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.StorageProfile.ImageReference = &armcompute.ImageReference{ID: image.ID}
	})
	assert.Contains(t, runInGuest(t, rg, vmName+"-2", "read", "cat /var/tmp/kept"), "kept-across-deallocate",
		"the machine created from the image does not carry the captured disk")
}

// A machine receives only what its interface's network security group allows,
// and a rule added while it runs applies at once.
func TestCompute_NetworkSecurityGroupFiltersWhatAMachineReceives(t *testing.T) {
	t.Parallel()
	const rg = "vm-nsg-rg"
	serverNIC := vmOperationsFixture(t, rg, "vm-nsg", "10.255.0.0/16")
	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rule := func(name string, priority int32, access armnetwork.SecurityRuleAccess, port string) *armnetwork.SecurityRule {
		return &armnetwork.SecurityRule{Name: to.Ptr(name), Properties: &armnetwork.SecurityRulePropertiesFormat{
			Priority: to.Ptr(priority), Direction: to.Ptr(armnetwork.SecurityRuleDirectionInbound), Access: to.Ptr(access),
			Protocol: to.Ptr(armnetwork.SecurityRuleProtocolTCP), SourceAddressPrefix: to.Ptr("VirtualNetwork"), SourcePortRange: to.Ptr("*"),
			DestinationAddressPrefix: to.Ptr("*"), DestinationPortRange: to.Ptr(port),
		}}
	}
	nsgPoller, err := nsgClient.BeginCreateOrUpdate(ctx, rg, "vm-nsg-web", armnetwork.SecurityGroup{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.SecurityGroupPropertiesFormat{SecurityRules: []*armnetwork.SecurityRule{
			rule("allow-80", 100, armnetwork.SecurityRuleAccessAllow, "80"),
			rule("deny-the-rest", 200, armnetwork.SecurityRuleAccessDeny, "*"),
		}},
	}, nil)
	require.NoError(t, err)
	nsg, err := nsgPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic, err := nicClient.Get(ctx, rg, "vm-nsg-nic", nil)
	require.NoError(t, err)
	nic.Properties.NetworkSecurityGroup = &armnetwork.SecurityGroup{ID: nsg.ID}
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "vm-nsg-nic", nic.Interface, nil)
	require.NoError(t, err)
	_, err = nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	serverIP := derefString(nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress)
	clientPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "vm-nsg-probe-nic", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                    nic.Properties.IPConfigurations[0].Properties.Subnet,
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					PrivateIPAddressVersion:   to.Ptr(armnetwork.IPVersionIPv4),
					Primary:                   to.Ptr(true),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	clientNIC, err := clientPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	const serverScript = `#!/bin/sh
serve() { perl -MIO::Socket::INET -e '$s=IO::Socket::INET->new(LocalPort=>$ARGV[0],Listen=>5,Reuse=>1) or die; while($c=$s->accept){print $c "HTTP/1.0 200 OK\r\n\r\nok\n"; close $c}' "$1" & }
serve 80
serve 81
`
	createOperationsVM(t, rg, "vm-nsg-web", serverNIC, func(vm *armcompute.VirtualMachine) {
		vm.Properties.OSProfile.CustomData = to.Ptr(base64.StdEncoding.EncodeToString([]byte(serverScript)))
	})
	createOperationsVM(t, rg, "vm-nsg-probe", derefString(clientNIC.ID), nil)
	probe := func(port int) string {
		return strings.TrimSpace(runInGuest(t, rg, "vm-nsg-probe", fmt.Sprintf("probe-%d", port),
			fmt.Sprintf(`bash -c 'if timeout 5 bash -c "exec 3<>/dev/tcp/%s/%d"; then echo port=open; else echo port=closed; fi'`, serverIP, port)))
	}
	require.Eventually(t, func() bool { return strings.Contains(probe(80), "port=open") }, 30*time.Second, time.Second, "the allowed port never answered the neighbour")
	assert.Contains(t, probe(81), "port=closed", "a port the group denies answered the neighbour")

	rulesClient, err := armnetwork.NewSecurityRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rulePoller, err := rulesClient.BeginCreateOrUpdate(ctx, rg, "vm-nsg-web", "allow-81", *rule("allow-81", 150, armnetwork.SecurityRuleAccessAllow, "81"), nil)
	require.NoError(t, err)
	_, err = rulePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, probe(81), "port=open", "the rule added while the machine runs does not allow its port")
}
