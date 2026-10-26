package azure_sdk_test

// The simulator advertises these URLs in a response header or body and the
// client follows them, so their literal paths never appear in test source.
// List them for scripts/check-simulator-tests.sh, with the test driving each.
//
//   GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationStatuses/{opId}
//     — TestSDK_ContainerAppsApps_DeleteLROEnvelope (Azure-AsyncOperation poll)
//   GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationResults/{opId}
//     — TestSDK_ContainerAppsApps_DeleteLROEnvelope (Location poll)
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/operations/{opId}
//     — TestCompute_VirtualMachineStateOperations (armcompute Begin* pollers)
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/locations/{location}/asyncOperations/{operationId}
//     — every armredis BeginCreate poller in redis_sdk_test.go
//   GET /eventsubscriptions/{eventSubscriptionName}/validate
//     — TestEventGrid_WebhookValidationHandshakeSDK (opens the validationUrl)
