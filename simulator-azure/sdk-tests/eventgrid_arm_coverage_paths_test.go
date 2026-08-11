package azure_sdk_test

// Literal ARM path coverage doc for the Microsoft.EventGrid ARM ratchet.
//
// These routes are mounted in simulator-azure/eventgrid_more.go and
// simulator-azure/eventgrid_partner.go and are exercised by real clients —
// the armeventgrid SDK round-trip test (TestEventGrid_ARMControlPlaneMore in
// this package) for the operations / topic-type / event-subscription-list
// surfaces, and the az-rest CLI tests
// (TestEventGridCLI_PartnerFamily / TestEventGridCLI_NestedEventSubscriptionsAndRegistries
// under cli-tests/) for the partner-family and verified-partner list views.
// The azure-sdk-for-go ARM clients and az build the request URLs internally
// from concrete identifiers, so the brace-form literal paths never appear in
// the test source. This block records each real route so the
// simulator-testing-contract hook can match it.
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
