package azure_sdk_test

// The SDK clients build request URLs internally, so the literal routes these
// tests drive never appear in test source. List them for
// scripts/check-simulator-tests.sh, which matches a newly mounted route's
// literal path in the changed test files.
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
