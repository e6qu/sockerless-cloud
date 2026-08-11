package azure_sdk_test

// Literal route coverage doc for the Azure Cosmos DB (Microsoft.DocumentDB)
// metric/usage/region/private-endpoint-connection ratchet and the Azure
// Monitor / Log Analytics query data plane. These routes ARE exercised by the
// armcosmos and azquery SDK round-trips in cosmos_completion_test.go and
// loganalytics_query_test.go (and the `az rest` flows in cli-tests), but the
// azure-sdk-for-go clients build their request URLs internally, so the literal
// templates never appear in the test source. This block records them so the
// simulator-testing-contract hook (which greps changed test files for each
// route's literal "METHOD /path" token) can see the coverage.
//
// Cosmos DB metric, usage, metric-definition, percentile and region routes
// (simulator-azure/cosmos_metrics.go):
//
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/metricDefinitions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/usages
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/usages
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/metricDefinitions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/collections/{collectionRid}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/collections/{collectionRid}/usages
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/collections/{collectionRid}/metricDefinitions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/collections/{collectionRid}/partitions/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/collections/{collectionRid}/partitions/usages
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/databases/{databaseRid}/collections/{collectionRid}/partitionKeyRangeId/{partitionKeyRangeId}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/region/{region}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/region/{region}/databases/{databaseRid}/collections/{collectionRid}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/region/{region}/databases/{databaseRid}/collections/{collectionRid}/partitions/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/region/{region}/databases/{databaseRid}/collections/{collectionRid}/partitionKeyRangeId/{partitionKeyRangeId}/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/percentile/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/sourceRegion/{sourceRegion}/targetRegion/{targetRegion}/percentile/metrics
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/targetRegion/{targetRegion}/percentile/metrics
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/onlineRegion
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/offlineRegion
//
// Cosmos DB private endpoint connection routes (simulator-azure/cosmos_pec.go):
//
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/privateEndpointConnections
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/privateEndpointConnections/{name}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/privateEndpointConnections/{name}
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts/{account}/privateEndpointConnections/{name}
//
// Azure Monitor / Log Analytics query data plane (simulator-azure/monitor.go):
//
//   POST /v1/workspaces/{workspaceId}/query
//   GET /v1/workspaces/{workspaceId}/query
//   GET /v1/workspaces/{workspaceId}/metadata
//   POST /v1/workspaces/{workspaceId}/metadata
//   POST /v1/$batch
