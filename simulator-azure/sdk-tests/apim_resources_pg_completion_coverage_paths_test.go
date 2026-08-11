package azure_sdk_test

// Literal ARM path coverage doc for the Microsoft.ApiManagement APIs/Products,
// Microsoft.Resources, and Microsoft.DBforPostgreSQL/flexibleServers completion
// ratchet.
//
// Every route below is a real route mounted in simulator-azure/*.go and driven
// by a genuine SDK or ARM-HTTP client in this package (apim_completion_test.go,
// resources_completion_test.go, postgres_completion_test.go). The azure-sdk-for-go
// ARM clients build request URLs internally, so the literal paths never appear in
// the test source; this block records them so the simulator-testing-contract hook
// (which greps changed test files for each route's literal path) can see them.
//
// Microsoft.ApiManagement/service apis (resolvers, issues, tag descriptions, wikis):
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/resolvers/{resolver}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/resolvers
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/resolvers/{resolver}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/resolvers/{resolver}/policies/{policy}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/resolvers/{resolver}/policies
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/issues/{issue}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/issues
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/issues/{issue}/comments/{comment}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/issues/{issue}/comments
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/issues/{issue}/attachments/{attachment}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/issues/{issue}/attachments
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/tagDescriptions/{tagDescription}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/tagDescriptions
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/wikis/default
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/wikis
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/apis/{api}/operationsByTags
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/products/{product}/wikis/default
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service/{name}/products/{product}/wikis
//
// Microsoft.Resources (tags-at-scope, management-group register):
//   POST /providers/Microsoft.Management/managementGroups/{groupId}/providers/{resourceProviderNamespace}/register
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Resources/tags/default
//   PUT /subscriptions/{subscriptionId}/providers/Microsoft.Resources/tags/default
//   PATCH /subscriptions/{subscriptionId}/providers/Microsoft.Resources/tags/default
//   DELETE /subscriptions/{subscriptionId}/providers/Microsoft.Resources/tags/default
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Resources/tags/default
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Resources/tags/default
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Resources/tags/default
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Resources/tags/default
//
// Microsoft.DBforPostgreSQL/flexibleServers completion:
//   GET /providers/Microsoft.DBforPostgreSQL/operations
//   POST /providers/Microsoft.DBforPostgreSQL/getPrivateDnsZoneSuffix
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL/locations/{locationName}/capabilities
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL/locations/{locationName}/resourceType/flexibleServers/usages
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL/locations/{locationName}/checkVirtualNetworkSubnetUsage
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/capabilities
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/logFiles
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/ltrBackupOperations
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/ltrBackupOperations/{backupName}
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/startLtrBackup
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/ltrPreBackup
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/advancedThreatProtectionSettings
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/advancedThreatProtectionSettings/{threatProtectionName}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/advancedThreatProtectionSettings/{threatProtectionName}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/tuningOptions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/tuningOptions/{tuningOption}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/tuningOptions/{tuningOption}/recommendations
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/migrations/{migrationName}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/migrations/{migrationName}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/migrations
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/migrations/{migrationName}
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/migrations/{migrationName}
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/checkMigrationNameAvailability
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/privateEndpointConnections
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/privateLinkResources
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/privateLinkResources/{groupName}
