package main

// Cross-resource-group move for Microsoft.DocumentDB/databaseAccounts
// (dispatched from resource_move.go). Azure supports it for the request-unit
// architecture the simulator serves. The account's subtree keys by resource ID
// and re-homes onto the destination group; the document endpoint derives from
// the globally unique name and the data plane keys documents by account name,
// so neither changes.
//
// The four master keys derive from the resource ID, which embeds the resource
// group, yet Azure never rotates them on a move: pin the served material onto
// the moved ID until the next regenerateKey.

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
