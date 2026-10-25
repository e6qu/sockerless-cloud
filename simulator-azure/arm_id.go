package main

// Azure Resource Manager resource IDs: a scope, then for a resource the
// provider namespace and alternating resource types and names, as in
// /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Web/sites/{site}/slots/{slot}.

func armSubscriptionID(subscription string) string {
	return "/subscriptions/" + subscription
}

func armResourceGroupID(subscription, resourceGroup string) string {
	return armSubscriptionID(subscription) + "/resourceGroups/" + resourceGroup
}

// armResourceID is the ID of a resource in a resource group; typeAndNames
// alternate resource type and name from the top-level resource down.
func armResourceID(subscription, resourceGroup, namespace string, typeAndNames ...string) string {
	return armProviderID(armResourceGroupID(subscription, resourceGroup), namespace, typeAndNames...)
}

// armSubscriptionResourceID is the ID of a resource a provider keeps at
// subscription scope, such as a location's deleted vaults.
func armSubscriptionResourceID(subscription, namespace string, typeAndNames ...string) string {
	return armProviderID(armSubscriptionID(subscription), namespace, typeAndNames...)
}

// armProviderID is the ID of a resource of namespace under scope, which may
// itself be a resource: an extension resource's ID nests this way.
func armProviderID(scope, namespace string, typeAndNames ...string) string {
	return armChildID(scope+"/providers/"+namespace, typeAndNames...)
}

// armChildID appends child resource types and names to a parent ID.
func armChildID(parentID string, typeAndNames ...string) string {
	id := parentID
	for _, segment := range typeAndNames {
		id += "/" + segment
	}
	return id
}
