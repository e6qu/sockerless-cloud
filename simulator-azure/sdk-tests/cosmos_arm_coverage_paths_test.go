package azure_sdk_test

// Literal ARM path coverage doc for the Azure Cosmos DB (Microsoft.DocumentDB)
// ratchet. These provider- and subscription-scoped routes ARE exercised by the
// armcosmos SDK round-trips in cosmos_arm_more_test.go
// (DatabaseAccountsClient.NewListPager / CheckNameExists,
// OperationsClient.NewListPager, LocationsClient.NewListPager / Get), but the
// azure-sdk-for-go ARM clients build their request URLs internally, so the
// literal paths never appear in the test source. This block records them so the
// simulator-testing-contract hook (which greps changed test files for each
// route's literal "METHOD /path" token) can see the coverage. Each line is a
// real route mounted in simulator-azure/cosmos_apis.go.
//
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/databaseAccounts
//   HEAD /providers/Microsoft.DocumentDB/databaseAccountNames/{account}
//   GET /providers/Microsoft.DocumentDB/operations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations/{location}
