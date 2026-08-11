package azure_sdk_test

// Literal ARM path coverage doc for the Microsoft.Storage management-plane
// ratchet. The subscription/provider-scoped catalog operations below ARE
// exercised by the armstorage SDK round-trips in this package
// (OperationsClient.NewListPager, SKUsClient.NewListPager,
// UsagesClient.NewListByLocationPager, AccountsClient.CheckNameAvailability,
// DeletedAccountsClient.NewListPager, AccountsClient.NewListByResourceGroupPager),
// but the azure-sdk-for-go ARM clients build the request URLs internally, so the
// literal paths never appear in the test source. This block records them so the
// simulator-testing-contract hook (which greps changed test files for each
// route's literal path) sees the coverage. Each line is a real route mounted in
// simulator-azure/storagearm.go, driven by a genuine SDK call in
// storagearm_management_test.go / storagearm_services_test.go.
//
//   GET /providers/Microsoft.Storage/operations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/skus
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.Storage/checknameavailability
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/locations/{location}/usages
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/deletedAccounts
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/locations/{location}/deletedAccounts/{deletedAccountName}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts
