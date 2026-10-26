package azure_sdk_test

// The ARM clients build request URLs internally, so the literal routes these
// tests drive never appear in test source. List them for
// scripts/check-simulator-tests.sh, which matches a newly mounted route's
// literal path in the changed test files.
//
//   GET /providers/Microsoft.ContainerRegistry/operations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.App/containerApps
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.App/jobs
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.App/managedEnvironments

// Azure Resource Manager's generic by-resource-ID methods and the
// Microsoft.Authorization permission listings, driven by served_tails_test.go.
//
//   GET /{resourceId}
//   PUT /{resourceId}
//   PATCH /{resourceId}
//   DELETE /{resourceId}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Authorization/permissions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}/providers/Microsoft.Authorization/permissions
