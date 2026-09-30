package azure_sdk_test

// Literal path coverage for the routes an Azure SDK poller or a returned link
// reaches. The simulator advertises each URL in a response header or body and
// the client follows it, so the literal path never appears in test source;
// this block records the "METHOD /path" tokens for the
// simulator-testing-contract hook, naming the test that drives each one.
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
