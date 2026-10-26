package azure_sdk_test

// The ARM clients build request URLs internally, so the literal routes these
// tests drive never appear in test source. List them for
// scripts/check-simulator-tests.sh, which matches a newly mounted route's
// literal path in the changed test files.
//
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/databaseAccounts
//   HEAD /providers/Microsoft.DocumentDB/databaseAccountNames/{account}
//   GET /providers/Microsoft.DocumentDB/operations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations/{location}
