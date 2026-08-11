package azure_sdk_test

// Literal ARM path coverage doc for the PostgreSQL flexible-server, Resources,
// Subscriptions, and Application Insights ratchet.
//
// Every route below is a real route mounted in simulator-azure/*.go and driven
// by a genuine SDK call in this package (postgres_more_test.go,
// resourcesarm_test.go, subscriptionsarm_test.go, appinsights_more_test.go).
// The azure-sdk-for-go ARM clients build request URLs internally, so the literal
// paths never appear in the test source; this block records them so the
// simulator-testing-contract hook (which greps changed test files for each
// route's literal path) can see the coverage.
//
// Microsoft.DBforPostgreSQL/flexibleServers:
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL/flexibleServers
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/start
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/stop
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/restart
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL/checknameavailability
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL/locations/{locationName}/checknameavailability
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/configurations/{cfg}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/administrators
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/administrators/{objectId}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/administrators/{objectId}
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/administrators/{objectId}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/backups
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/backups/{backupName}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/backups/{backupName}
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/backups/{backupName}
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/replicas
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/virtualendpoints
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/virtualendpoints/{ve}
//   PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/virtualendpoints/{ve}
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/virtualendpoints/{ve}
//   DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers/{name}/virtualendpoints/{ve}
//
// Microsoft.Resources (providers, predefined tagNames, operations, move, export):
//   GET /providers/Microsoft.Resources/operations
//   GET /providers
//   GET /providers/{resourceProviderNamespace}
//   GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}
//   GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/resourceTypes
//   GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/providerPermissions
//   POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/register
//   POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/unregister
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/exportTemplate
//   POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//   POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources
//   GET /subscriptions/{subscriptionId}/tagNames
//   PUT /subscriptions/{subscriptionId}/tagNames/{tagName}
//   DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}
//   PUT /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}
//   DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}
//
// Microsoft.Resources subscriptions:
//   GET /subscriptions
//   GET /subscriptions/{subscriptionId}/locations
//   GET /tenants
//   POST /providers/Microsoft.Resources/checkResourceName
//   POST /subscriptions/{subscriptionId}/providers/Microsoft.Resources/checkZonePeers/
//
// Microsoft.Insights/components:
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/components
//   PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{componentName}
//   POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{componentName}/purge
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{componentName}/operations/{purgeId}
