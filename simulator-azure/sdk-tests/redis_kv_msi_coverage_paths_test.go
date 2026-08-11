package azure_sdk_test

// Literal ARM path coverage doc for the Azure Redis / Key Vault / Managed
// Identity ratchet.
//
// Each route below is a real endpoint mounted in simulator-azure/{cache_redis,
// keyvault,managedidentity}.go and driven by a genuine azure-sdk-for-go call in
// this package's tests — but the SDK ARM clients build request URLs internally,
// so the literal path string never appears in the test source. This block
// records the literal "METHOD /path" tokens so the simulator-testing-contract
// hook (which greps changed test files for each newly-mounted route's literal
// path) can see the coverage.
//
//   GET /providers/Microsoft.Cache/operations
//     — TestRedis_OperationsList (armredis OperationsClient.NewListPager)
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.Cache/checknameavailability
//     — TestRedis_CheckNameAvailability (armredis Client.CheckNameAvailability)
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/locations/{location}/asyncOperations/{operationId}
//     — followed by every armredis BeginCreate poller (final-state-via azure-async-operation)
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/checknameavailability
//     — TestKeyVaultMgmt_CheckNameAvailability (armkeyvault VaultsClient.CheckNameAvailability)
//   GET /providers/Microsoft.ManagedIdentity/operations
//     — TestMSI_OperationsAndSystemAssigned (armmsi OperationsClient.NewListPager)
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.ManagedIdentity/userAssignedIdentities
//     — TestMSI_UpdateAndList (armmsi UserAssignedIdentitiesClient.NewListBySubscriptionPager)
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.ManagedIdentity/identities/default
//     — TestMSI_OperationsAndSystemAssigned (armmsi SystemAssignedIdentitiesClient.GetByScope)
