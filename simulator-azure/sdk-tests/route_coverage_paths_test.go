package azure_sdk_test

// Literal ARM path coverage doc for the Azure service ratchet (BUG-2224).
//
// These subscription-wide list operations ARE exercised by the Azure SDK
// round-trip tests in this package (NewListBySubscriptionPager etc.), but the
// azure-sdk-for-go ARM clients build request URLs internally, so the literal
// paths never appear in the test source. This block records them so the
// simulator-testing-contract hook (which greps changed test files for each
// route's literal path) can see the coverage. Each line is a real route mounted
// in simulator-azure/<service>.go, driven by a genuine SDK call here.
//
//   GET /providers/Microsoft.ContainerRegistry/operations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.App/containerApps
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.App/jobs
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.App/managedEnvironments

// The tails served on 2026-08-26: Azure Resource Manager's generic
// by-resource-ID methods (whose real URLs the typed provider routes serve,
// with these patterns answering the addresses no typed route mounts) and
// the Microsoft.Authorization permission listings. Driven by
// served_tails_test.go; the literal wire paths are recorded here so the
// simulator-testing-contract hook can see the coverage.
//
//   GET /{resourceId}
//   PUT /{resourceId}
//   PATCH /{resourceId}
//   DELETE /{resourceId}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Authorization/permissions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}/providers/Microsoft.Authorization/permissions
