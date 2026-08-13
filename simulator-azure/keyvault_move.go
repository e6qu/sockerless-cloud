package main

// Cross-resource-group move for Microsoft.KeyVault/vaults. The hook table in
// resource_move.go dispatches Resources_MoveResources here.
//
// A vault is addressed on two planes, and only one of them carries the
// resource group. The ARM plane keys the vault record — and the
// privateEndpointConnections children beneath it — by resource ID, so both
// re-key onto the destination group. The data plane addresses the vault by its
// globally unique name through the DNS host `<vault>.vault.<suffix>`: the
// secret, key and certificate stores key on that name, the vault URI is
// derived from it on every read, and a move changes neither. So the material
// a vault holds needs no re-keying at all, and the URI clients hold keeps
// resolving to the same vault after the move — which is what makes this family
// a move real Azure Key Vault also performs without touching secret material.

// moveKeyVaultARM re-homes one key vault's ARM plane onto a new resource ID:
// the vault record itself and its private-endpoint-connection children. The
// vault's name — the only coordinate the data plane and the vault URI are
// built from — is unchanged by a cross-group move, so secrets, keys and
// certificates stay exactly where they are.
func moveKeyVaultARM(oldID, newID string) {
	vault, ok := keyVaults.Get(oldID)
	if !ok {
		return
	}
	keyVaults.Delete(oldID)
	vault.ID = newID
	keyVaults.Put(vault.ID, vault)

	rekeyRowsByPrefix(keyVaultPrivConn, oldID+"/", newID+"/",
		func(p *KeyVaultPrivateEndpointConnection) *string { return &p.ID })
}
