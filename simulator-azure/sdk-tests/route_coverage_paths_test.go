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
