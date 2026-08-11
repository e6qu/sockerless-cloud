package azure_sdk_test

// Literal ARM path coverage doc for the Azure Container Instances and RBAC
// ratchet.
//
// The Azure SDK ARM clients build request URLs internally, so the literal
// paths these tests drive never appear in the test source. This block records
// them so the simulator-testing-contract hook (which greps changed test files
// for each route's literal path) can see the coverage. Each line is a real
// route mounted in simulator-azure/containerinstance.go or
// simulator-azure/authorization.go, exercised by a genuine SDK or CLI call in
// this package's tests.
//
// Container Instances (containerinstance_test.go drives these via
// armcontainerinstance's OperationsClient / LocationClient):
//
//   GET /providers/Microsoft.ContainerInstance/operations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/locations/{location}/usages
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/locations/{location}/capabilities
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/locations/{location}/cachedImages
//
// Subnet service-association-link delete is driven by the az-rest CLI test
// TestACISubnetServiceAssociationLinkDelete in cli-tests:
//
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{virtualNetworkName}/subnets/{subnetName}/providers/Microsoft.ContainerInstance/serviceAssociationLinks/default
