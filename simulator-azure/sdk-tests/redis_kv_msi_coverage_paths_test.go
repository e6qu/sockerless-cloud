package azure_sdk_test

// The ARM clients build request URLs internally, so the literal routes these
// tests drive never appear in test source. List them for
// scripts/check-simulator-tests.sh, which matches a newly mounted route's
// literal path in the changed test files.
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
//     — TestMSI_OperationsAndSystemAssigned (armmsi SystemAssignedIdentitiesClient.GetByScope, a scope with no identity)
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{resourceType}/{resourceName}/{childType}/{childName}/providers/Microsoft.ManagedIdentity/identities/default
//     — TestSDK_WebMore_Slots (armmsi SystemAssignedIdentitiesClient.GetByScope on a deployment slot)
