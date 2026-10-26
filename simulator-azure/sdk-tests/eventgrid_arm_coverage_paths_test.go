package azure_sdk_test

// The ARM clients and az build request URLs internally, so the literal routes
// these tests drive never appear in test source. List them for
// scripts/check-simulator-tests.sh, which matches a newly mounted route's
// literal path in the changed test files.
//
//   GET /providers/Microsoft.EventGrid/operations
//   GET /providers/Microsoft.EventGrid/topicTypes
//   GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}
//   GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventTypes
//   GET /providers/Microsoft.EventGrid/verifiedPartners
//   GET /providers/Microsoft.EventGrid/verifiedPartners/{verifiedPartnerName}
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerRegistrations
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerNamespaces
//   GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerConfigurations
