package main

// Cross-resource-group move for Microsoft.DocumentDB/databaseAccounts. The
// hook table in resource_move.go dispatches Resources_MoveResources here.
//
// Azure Resource Manager moves a Cosmos DB account between resource groups
// ("Azure resource types for move operations", Microsoft.DocumentDB /
// databaseaccounts: Resource group = Yes; the note below that table restricts
// the support to the request-unit architecture the simulator serves, not the
// vCore clusters it does not model).
//
// An account keys its ARM record — and its SQL databases and containers, its
// tables, its throughput settings and its private endpoint connections — by
// resource ID, so the whole subtree re-homes onto the destination group through
// the repointing pass in resource_move.go. The account's document endpoint is
// derived from its globally unique name rather than its resource group, so the
// URL an application dials is unchanged, and the data plane addresses stored
// documents by account name, so every database, container and document the
// account holds is untouched by the move.
//
// The account's four master keys are derived from the resource ID, which
// embeds the resource group, so a naive re-key would silently rotate the
// credential in every connection string an application holds. Real Azure never
// rotates an account's keys on a move, so the material listKeys serves is
// pinned onto the moved ID; the next regenerateKey clears the pin and derives
// fresh material.

// moveCosmosAccountARM re-homes one Cosmos DB account's ARM record onto a new
// resource ID, pinning its master keys so the connection string an application
// holds keeps working.
func moveCosmosAccountARM(oldID, newID string) {
	account, ok := cosmosAccounts.Get(oldID)
	if !ok {
		return
	}
	pinCosmosKeys(oldID, newID)
	cosmosAccounts.Delete(oldID)
	account.ID = newID
	cosmosAccounts.Put(account.ID, account)
}
