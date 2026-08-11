package azure_sdk_test

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/require"
)

// Azure validates a write against the request before its resource provider
// realizes anything, and answers a request the client got wrong with 4xx. These
// tests pin that ordering for the two writes that reach real host fabric —
// creating a virtual machine and creating a subnet. Reporting a permanently
// invalid request as the 503 OperationNotAllowed that a genuine host failure
// earns would put an SDK retry policy into a loop it can never leave, and would
// have the simulator build fabric for a request Azure would have rejected.
//
// Neither test calls requireNetworkHost: the whole point is that the rejection
// happens before the host capabilities matter, so both run on every host.

func TestCompute_VirtualMachineWithoutNetworkProfileIsRejected(t *testing.T) {
	const rg = "vm-request-validation-rg"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := vmClient.BeginCreateOrUpdate(ctx, rg, "vm-no-nic", armcompute.VirtualMachine{
		Location:   to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{},
	}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "a virtual machine with no network interface must be rejected")

	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	require.Equal(t, 400, respErr.StatusCode,
		"an unusable networkProfile is the client's error, not a service problem")
	require.Equal(t, "InvalidParameter", respErr.ErrorCode)
}

func TestCompute_VirtualMachineWithUnknownNetworkInterfaceIsRejected(t *testing.T) {
	const rg = "vm-request-validation-rg"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	missingNIC := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Network/networkInterfaces/nic-that-was-never-created"

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := vmClient.BeginCreateOrUpdate(ctx, rg, "vm-missing-nic", armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{ID: to.Ptr(missingNIC)}},
			},
		},
	}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "a virtual machine referencing an absent interface must be rejected")

	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	require.Equal(t, 404, respErr.StatusCode)
	require.Equal(t, "ResourceNotFound", respErr.ErrorCode)
}

func TestNetwork_SubnetInAbsentVirtualNetworkIsNotFound(t *testing.T) {
	const rg = "subnet-request-validation-rg"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, "vnet-that-was-never-created", "orphan-subnet",
		armnetwork.Subnet{
			Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.99.1.0/24")},
		}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "a subnet whose parent virtual network does not exist must be rejected")

	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	require.Equal(t, 404, respErr.StatusCode,
		"the parent virtual network is absent, which is ResourceNotFound — not a host problem")
	require.Equal(t, "ResourceNotFound", respErr.ErrorCode)
}
