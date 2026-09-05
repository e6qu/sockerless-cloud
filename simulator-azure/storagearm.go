package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.Storage ARM management-plane control surface beyond the storage
// account + file-share + container + table CRUD that registerAzureFiles already
// mounts. This slice implements the storage-account action verbs (regenerateKey,
// ListAccountSas, ListServiceSas, failover, revokeUserDelegationKeys), the
// subscription/provider-scoped catalog operations (Operations_List, Skus_List,
// Usages_ListByLocation, DeletedAccounts, CheckNameAvailability,
// ListByResourceGroup), and the per-account child resources the official
// armstorage SDK + terraform-provider-azurerm address (managementPolicies,
// inventoryPolicies, privateEndpointConnections, privateLinkResources,
// objectReplicationPolicies, encryptionScopes, localUsers). It also completes the
// blobServices / fileServices / queueServices / tableServices service-property
// and sub-resource surfaces (blob containers legal-hold + immutability + lease,
// file-share update/restore/lease + service usages, storage queues, service
// list/set-properties). The storage account store itself lives in
// registerAzureFiles; this handler reads it through azStorageAccounts.

// storageARMChild is the common shape of an account-scoped ARM child resource
// (id/name/type + a free-form properties bag). The simulator round-trips the
// caller-supplied properties verbatim and layers in only the server-generated
// read-only members each resource's swagger declares (policyId, sid,
// provisioningState, lastModifiedTime, …), so emitted bodies stay
// spec-conformant without re-deriving the whole schema.
type storageARMChild struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Etag       string         `json:"etag,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

// azureStoragePECs holds the private endpoint connections on storage accounts.
var azureStoragePECs sim.Store[storageARMChild]

func storageAcctResourceID(sub, rg, account string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s",
		sub, rg, account)
}

// requireStorageAccount resolves the account scope from the request path and
// 404s (the real ARM shape) when the account does not exist.
func requireStorageAccount(w http.ResponseWriter, r *http.Request) (acctID, account string, ok bool) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	account = sim.PathParam(r, "accountName")
	acctID = storageAcctResourceID(sub, rg, account)
	if azStorageAccounts == nil {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Storage/storageAccounts/%s' was not found.", account)
		return "", account, false
	}
	if _, exists := azStorageAccounts.Get(acctID); !exists {
		rg := sim.PathParam(r, "resourceGroupName")
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
		return "", account, false
	}
	return acctID, account, true
}

// readARMProperties decodes the `{ "properties": { … } }` envelope every ARM PUT
// carries, tolerating an empty body (action POSTs).
func readARMProperties(r *http.Request) (map[string]any, error) {
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if r.Body != nil {
		if err := sim.ReadJSON(r, &req); err != nil && err != io.EOF {
			return nil, err
		}
	}
	if req.Properties == nil {
		req.Properties = map[string]any{}
	}
	return req.Properties, nil
}

func storageNow() string { return time.Now().UTC().Format(time.RFC3339) }

func registerStorageAccounts(srv *sim.Server) {
	makeAzureKeyGens(srv)
	managementPolicies := sim.MakeStore[storageARMChild](srv.DB(), "storage_management_policies")
	inventoryPolicies := sim.MakeStore[storageARMChild](srv.DB(), "storage_inventory_policies")
	privateEndpointConns := sim.MakeStore[storageARMChild](srv.DB(), "storage_private_endpoint_connections")
	// A private endpoint targeting a storage account opens its connection in
	// this very collection, so the endpoint's view and the account's
	// privateEndpointConnections surface read one object.
	azureStoragePECs = privateEndpointConns
	objectReplicationPolicies := sim.MakeStore[storageARMChild](srv.DB(), "storage_object_replication_policies")
	encryptionScopes := sim.MakeStore[storageARMChild](srv.DB(), "storage_encryption_scopes")
	localUsers := sim.MakeStore[storageARMChild](srv.DB(), "storage_local_users")
	immutabilityPolicies := sim.MakeStore[storageARMChild](srv.DB(), "storage_immutability_policies")
	storageQueues := sim.MakeStore[storageARMChild](srv.DB(), "storage_queues")
	serviceProperties := sim.MakeStore[map[string]any](srv.DB(), "storage_service_properties")
	// The blob/file/queue/table service resources an administrator writes here
	// configure the matching data planes; the Files data plane reads the share
	// delete-retention policy from this store.
	azStorageServiceProps = serviceProperties

	const acct = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}"

	srv.HandleFunc("GET /providers/Microsoft.Storage/operations", handleStorageOperationsList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/skus", handleStorageSkusList)
	// AzurePathNormalizationMiddleware canonicalizes the checkNameAvailability
	// action verb to lowercase, so the handler registers the lowercase spelling.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Storage/checknameavailability", handleStorageCheckNameAvailability)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/locations/{location}/usages", handleStorageUsagesByLocation)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/deletedAccounts", handleStorageDeletedAccountsList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/locations/{location}/deletedAccounts/{deletedAccountName}", handleStorageDeletedAccountGet)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts", handleStorageAccountsListByRG)

	registerStorageMigrations(srv, acct)

	srv.HandleFunc("POST "+acct+"/regenerateKey", handleStorageRegenerateKey)
	srv.HandleFunc("POST "+acct+"/ListAccountSas", handleStorageListAccountSAS)
	srv.HandleFunc("POST "+acct+"/ListServiceSas", handleStorageListServiceSAS)
	srv.HandleFunc("POST "+acct+"/failover", handleStorageFailover)
	srv.HandleFunc("POST "+acct+"/revokeUserDelegationKeys", handleStorageRevokeUserDelegationKeys)

	srv.HandleFunc("PUT "+acct+"/managementPolicies/{managementPolicyName}",
		storageChildPut(managementPolicies, "managementPolicies", "Microsoft.Storage/storageAccounts/managementPolicies", "managementPolicyName", func(p map[string]any) {
			p["lastModifiedTime"] = storageNow()
		}))
	srv.HandleFunc("GET "+acct+"/managementPolicies/{managementPolicyName}",
		storageChildGet(managementPolicies, "managementPolicies", "managementPolicyName"))
	srv.HandleFunc("DELETE "+acct+"/managementPolicies/{managementPolicyName}",
		storageChildDelete(managementPolicies, "managementPolicies", "managementPolicyName"))

	srv.HandleFunc("PUT "+acct+"/inventoryPolicies/{blobInventoryPolicyName}",
		storageChildPut(inventoryPolicies, "inventoryPolicies", "Microsoft.Storage/storageAccounts/inventoryPolicies", "blobInventoryPolicyName", func(p map[string]any) {
			p["lastModifiedTime"] = storageNow()
		}))
	srv.HandleFunc("GET "+acct+"/inventoryPolicies/{blobInventoryPolicyName}",
		storageChildGet(inventoryPolicies, "inventoryPolicies", "blobInventoryPolicyName"))
	srv.HandleFunc("DELETE "+acct+"/inventoryPolicies/{blobInventoryPolicyName}",
		storageChildDelete(inventoryPolicies, "inventoryPolicies", "blobInventoryPolicyName"))
	srv.HandleFunc("GET "+acct+"/inventoryPolicies",
		storageChildList(inventoryPolicies, "inventoryPolicies"))

	srv.HandleFunc("PUT "+acct+"/privateEndpointConnections/{privateEndpointConnectionName}",
		storageChildPut(privateEndpointConns, "privateEndpointConnections", "Microsoft.Storage/storageAccounts/privateEndpointConnections", "privateEndpointConnectionName", func(p map[string]any) {
			if _, ok := p["privateLinkServiceConnectionState"]; !ok {
				p["privateLinkServiceConnectionState"] = map[string]any{"status": "Approved"}
			}
			p["provisioningState"] = "Succeeded"
		}))
	srv.HandleFunc("GET "+acct+"/privateEndpointConnections/{privateEndpointConnectionName}",
		storageChildGet(privateEndpointConns, "privateEndpointConnections", "privateEndpointConnectionName"))
	srv.HandleFunc("DELETE "+acct+"/privateEndpointConnections/{privateEndpointConnectionName}",
		storageChildDelete(privateEndpointConns, "privateEndpointConnections", "privateEndpointConnectionName"))
	srv.HandleFunc("GET "+acct+"/privateEndpointConnections",
		storageChildList(privateEndpointConns, "privateEndpointConnections"))

	srv.HandleFunc("GET "+acct+"/privateLinkResources", handleStoragePrivateLinkResources)

	srv.HandleFunc("PUT "+acct+"/objectReplicationPolicies/{objectReplicationPolicyId}",
		storageChildPut(objectReplicationPolicies, "objectReplicationPolicies", "Microsoft.Storage/storageAccounts/objectReplicationPolicies", "objectReplicationPolicyId", func(p map[string]any) {
			if _, ok := p["policyId"]; !ok {
				p["policyId"] = generateUUID()
			}
			p["enabledTime"] = storageNow()
		}))
	srv.HandleFunc("GET "+acct+"/objectReplicationPolicies/{objectReplicationPolicyId}",
		storageChildGet(objectReplicationPolicies, "objectReplicationPolicies", "objectReplicationPolicyId"))
	srv.HandleFunc("DELETE "+acct+"/objectReplicationPolicies/{objectReplicationPolicyId}",
		storageChildDelete(objectReplicationPolicies, "objectReplicationPolicies", "objectReplicationPolicyId"))
	srv.HandleFunc("GET "+acct+"/objectReplicationPolicies",
		storageChildList(objectReplicationPolicies, "objectReplicationPolicies"))

	encScopeFinalize := func(p map[string]any) {
		if _, ok := p["source"]; !ok {
			p["source"] = "Microsoft.Storage"
		}
		if _, ok := p["state"]; !ok {
			p["state"] = "Enabled"
		}
		p["lastModifiedTime"] = storageNow()
	}
	srv.HandleFunc("PUT "+acct+"/encryptionScopes/{encryptionScopeName}",
		storageEncryptionScopePut(encryptionScopes, encScopeFinalize, false))
	srv.HandleFunc("PATCH "+acct+"/encryptionScopes/{encryptionScopeName}",
		storageEncryptionScopePut(encryptionScopes, encScopeFinalize, true))
	srv.HandleFunc("GET "+acct+"/encryptionScopes/{encryptionScopeName}",
		storageChildGet(encryptionScopes, "encryptionScopes", "encryptionScopeName"))
	srv.HandleFunc("GET "+acct+"/encryptionScopes",
		storageChildList(encryptionScopes, "encryptionScopes"))

	srv.HandleFunc("PUT "+acct+"/localUsers/{username}",
		storageChildPut(localUsers, "localUsers", "Microsoft.Storage/storageAccounts/localUsers", "username", func(p map[string]any) {
			if _, ok := p["sid"]; !ok {
				p["sid"] = "S-1-5-21-" + strings.ReplaceAll(generateUUID(), "-", "")[:24]
			}
		}))
	srv.HandleFunc("GET "+acct+"/localUsers/{username}",
		storageChildGet(localUsers, "localUsers", "username"))
	srv.HandleFunc("DELETE "+acct+"/localUsers/{username}",
		storageChildDelete(localUsers, "localUsers", "username"))
	srv.HandleFunc("GET "+acct+"/localUsers",
		storageChildList(localUsers, "localUsers"))
	srv.HandleFunc("POST "+acct+"/localUsers/{username}/listKeys", handleStorageLocalUserListKeys)
	srv.HandleFunc("POST "+acct+"/localUsers/{username}/regeneratePassword", handleStorageLocalUserRegeneratePassword)

	srv.HandleFunc("GET "+acct+"/blobServices", storageServiceListHandler("blobServices", "Microsoft.Storage/storageAccounts/blobServices", serviceProperties))

	const containers = acct + "/blobServices/default/containers"
	srv.HandleFunc("PATCH "+containers+"/{containerName}", handleStorageContainerUpdate)
	srv.HandleFunc("POST "+containers+"/{containerName}/setLegalHold", handleStorageContainerSetLegalHold)
	srv.HandleFunc("POST "+containers+"/{containerName}/clearLegalHold", handleStorageContainerClearLegalHold)
	srv.HandleFunc("POST "+containers+"/{containerName}/lease", handleStorageContainerLease)
	srv.HandleFunc("POST "+containers+"/{containerName}/migrate", handleStorageContainerObjectLevelWorm)
	srv.HandleFunc("PUT "+containers+"/{containerName}/immutabilityPolicies/{immutabilityPolicyName}",
		storageImmutabilityPut(immutabilityPolicies))
	srv.HandleFunc("GET "+containers+"/{containerName}/immutabilityPolicies/{immutabilityPolicyName}",
		storageImmutabilityGet(immutabilityPolicies))
	srv.HandleFunc("DELETE "+containers+"/{containerName}/immutabilityPolicies/{immutabilityPolicyName}",
		storageImmutabilityDelete(immutabilityPolicies))
	srv.HandleFunc("POST "+containers+"/{containerName}/immutabilityPolicies/default/lock",
		storageImmutabilityLock(immutabilityPolicies))
	srv.HandleFunc("POST "+containers+"/{containerName}/immutabilityPolicies/default/extend",
		storageImmutabilityExtend(immutabilityPolicies))

	srv.HandleFunc("GET "+acct+"/fileServices", storageServiceListHandler("fileServices", "Microsoft.Storage/storageAccounts/fileServices", serviceProperties))
	srv.HandleFunc("PUT "+acct+"/fileServices/default", storageServiceSetHandler("fileServices", "Microsoft.Storage/storageAccounts/fileServices", serviceProperties))
	srv.HandleFunc("GET "+acct+"/fileServices/default/usages", handleStorageFileServiceUsages)
	srv.HandleFunc("GET "+acct+"/fileServices/default/usages/{fileServiceUsagesName}", handleStorageFileServiceUsageGet)
	srv.HandleFunc("PATCH "+acct+"/fileServices/default/shares/{shareName}", handleStorageFileShareUpdate)
	srv.HandleFunc("POST "+acct+"/fileServices/default/shares/{shareName}/restore", handleStorageFileShareRestore)
	srv.HandleFunc("POST "+acct+"/fileServices/default/shares/{shareName}/lease", handleStorageFileShareLease)

	srv.HandleFunc("GET "+acct+"/queueServices", storageServiceListHandler("queueServices", "Microsoft.Storage/storageAccounts/queueServices", serviceProperties))
	srv.HandleFunc("PUT "+acct+"/queueServices/default", storageServiceSetHandler("queueServices", "Microsoft.Storage/storageAccounts/queueServices", serviceProperties))
	const queues = acct + "/queueServices/default/queues"
	srv.HandleFunc("PUT "+queues+"/{queueName}", storageQueuePut(storageQueues))
	srv.HandleFunc("PATCH "+queues+"/{queueName}", storageQueuePut(storageQueues))
	srv.HandleFunc("GET "+queues+"/{queueName}", storageQueueGet(storageQueues))
	srv.HandleFunc("DELETE "+queues+"/{queueName}", storageQueueDelete(storageQueues))
	srv.HandleFunc("GET "+queues, storageQueueList(storageQueues))

	srv.HandleFunc("GET "+acct+"/tableServices", storageServiceListHandler("tableServices", "Microsoft.Storage/storageAccounts/tableServices", serviceProperties))
	srv.HandleFunc("PUT "+acct+"/tableServices/default", storageServiceSetHandler("tableServices", "Microsoft.Storage/storageAccounts/tableServices", serviceProperties))

	// Cross-resource-group move. The Blob / Files / Queue / Table data planes
	// address an account by its globally unique name, which a move never
	// changes, so the stored bytes and data-plane rows stay untouched; what
	// re-keys is every ARM-plane row addressed by the account's resource ID.
	// The child stores that are locals of this register function ride along in
	// the closure.
	registerResourceMoveHook("Microsoft.Storage/storageAccounts", resourceMoveHook{
		exists: func(id string) bool { _, ok := azStorageAccounts.Get(id); return ok },
		move: func(oldID, newID, _ string) {
			moveStorageAccountARM(oldID, newID,
				managementPolicies, inventoryPolicies, privateEndpointConns,
				objectReplicationPolicies, encryptionScopes, localUsers,
				immutabilityPolicies, storageQueues)
		},
	})
}

// moveStorageAccountARM re-homes one storage account's ARM plane onto a new
// resource ID: the account record itself, the blob-container / file-share /
// table projections, every account-scoped ARM child collection, the
// {blob,file,queue,table}Services service-properties documents, and the
// access-key rotation state. Data-plane state is keyed by the account name
// and needs no re-keying.
func moveStorageAccountARM(oldID, newID string, childStores ...sim.Store[storageARMChild]) {
	acct, ok := azStorageAccounts.Get(oldID)
	if !ok {
		return
	}
	azStorageAccounts.Delete(oldID)
	acct.ID = newID
	azStorageAccounts.Put(acct.ID, acct)

	oldSub, newSub := oldID+"/", newID+"/"
	rekeyRowsByPrefix(azBlobContainers, oldSub, newSub, func(c *BlobContainer) *string { return &c.ID })
	rekeyRowsByPrefix(azFileShares, oldSub, newSub, func(s *FileShare) *string { return &s.ID })
	rekeyRowsByPrefix(storageTables, oldSub, newSub, func(tb *StorageTable) *string { return &tb.ID })
	for _, store := range childStores {
		rekeyRowsByPrefix(store, oldSub, newSub, func(c *storageARMChild) *string { return &c.ID })
	}
	for _, service := range []string{"blobServices", "fileServices", "queueServices", "tableServices"} {
		rekeyEntry(azStorageServiceProps, oldID+"/"+service, newID+"/"+service)
	}
	// The ARM blobServices/default property bag the control plane keeps for
	// container soft delete is keyed by the bare account ID.
	rekeyEntry(blobARMServiceProps, oldID, newID)

	// The account's access keys are a property of the account, not of its
	// resource group: listKeys must serve the same material after the move.
	pinAzureKeySlots(oldID, newID, azureKeyMaterial64, "key1", "key2")
}

func storageChildID(acctID, collection, name string) string {
	return acctID + "/" + collection + "/" + name
}

func storageChildPut(store sim.Store[storageARMChild], collection, typeStr, nameParam string, finalize func(map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, nameParam)
		props, err := readARMProperties(r)
		if err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if finalize != nil {
			finalize(props)
		}
		id := storageChildID(acctID, collection, name)
		child := storageARMChild{ID: id, Name: name, Type: typeStr, Properties: props}
		store.Put(id, child)
		sim.WriteJSON(w, http.StatusOK, child)
	}
}

func storageChildGet(store sim.Store[storageARMChild], collection, nameParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, nameParam)
		child, found := store.Get(storageChildID(acctID, collection, name))
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The resource %q was not found.", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, child)
	}
}

func storageChildDelete(store sim.Store[storageARMChild], collection, nameParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, nameParam)
		if store.Delete(storageChildID(acctID, collection, name)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func storageChildList(store sim.Store[storageARMChild], collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		prefix := acctID + "/" + collection + "/"
		items := store.Filter(func(c storageARMChild) bool { return strings.HasPrefix(c.ID, prefix) })
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		if items == nil {
			items = []storageARMChild{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	}
}

// storageEncryptionScopePut handles both PUT (replace) and PATCH (merge) for
// encryption scopes, preserving the creationTime read-only field across updates.
func storageEncryptionScopePut(store sim.Store[storageARMChild], finalize func(map[string]any), merge bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "encryptionScopeName")
		props, err := readARMProperties(r)
		if err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := storageChildID(acctID, "encryptionScopes", name)
		creationTime := storageNow()
		if existing, found := store.Get(id); found {
			if ct, ok := existing.Properties["creationTime"].(string); ok {
				creationTime = ct
			}
			if merge {
				merged := map[string]any{}
				for k, v := range existing.Properties {
					merged[k] = v
				}
				for k, v := range props {
					merged[k] = v
				}
				props = merged
			}
		}
		if finalize != nil {
			finalize(props)
		}
		props["creationTime"] = creationTime
		child := storageARMChild{ID: id, Name: name, Type: "Microsoft.Storage/storageAccounts/encryptionScopes", Properties: props}
		store.Put(id, child)
		sim.WriteJSON(w, http.StatusOK, child)
	}
}

func handleStorageOperationsList(w http.ResponseWriter, r *http.Request) {
	op := func(name, resource, operation, desc string) map[string]any {
		return map[string]any{
			"name": name,
			"display": map[string]any{
				"provider":    "Microsoft Storage",
				"resource":    resource,
				"operation":   operation,
				"description": desc,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{
			op("Microsoft.Storage/storageAccounts/read", "Storage Accounts", "List/Get Storage Account(s)", "Returns the list of storage accounts or gets the properties for the specified storage account."),
			op("Microsoft.Storage/storageAccounts/write", "Storage Accounts", "Create/Update Storage Account", "Creates a storage account with the specified parameters or update the properties or tags or adds custom domain for the specified storage account."),
			op("Microsoft.Storage/storageAccounts/delete", "Storage Accounts", "Delete Storage Account", "Deletes an existing storage account."),
			op("Microsoft.Storage/storageAccounts/listkeys/action", "Storage Accounts", "List Storage Account Keys", "Returns the access keys for the specified storage account."),
			op("Microsoft.Storage/storageAccounts/regeneratekey/action", "Storage Accounts", "Regenerate Storage Account Keys", "Regenerates the access keys for the specified storage account."),
		},
	})
}

func handleStorageSkusList(w http.ResponseWriter, r *http.Request) {
	skus := []struct{ name, tier, kind string }{
		{"Standard_LRS", "Standard", "StorageV2"},
		{"Standard_GRS", "Standard", "StorageV2"},
		{"Standard_RAGRS", "Standard", "StorageV2"},
		{"Standard_ZRS", "Standard", "StorageV2"},
		{"Premium_LRS", "Premium", "BlockBlobStorage"},
		{"Premium_ZRS", "Premium", "FileStorage"},
		{"Standard_GZRS", "Standard", "StorageV2"},
		{"Standard_RAGZRS", "Standard", "StorageV2"},
	}
	var value []map[string]any
	for _, s := range skus {
		value = append(value, map[string]any{
			"resourceType": "storageAccounts",
			"name":         s.name,
			"tier":         s.tier,
			"kind":         s.kind,
			"locations":    []string{"eastus", "westus", "westeurope"},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

func handleStorageCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	taken := false
	if azStorageAccounts != nil {
		for _, a := range azStorageAccounts.List() {
			if strings.EqualFold(a.Name, req.Name) {
				taken = true
				break
			}
		}
	}
	if taken {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"nameAvailable": false,
			"reason":        "AlreadyExists",
			"message":       "The storage account named " + req.Name + " is already taken.",
		})
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"nameAvailable": true})
}

func handleStorageUsagesByLocation(w http.ResponseWriter, r *http.Request) {
	current := 0
	if azStorageAccounts != nil {
		current = len(azStorageAccounts.List())
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{
			{
				"unit":         "Count",
				"currentValue": current,
				"limit":        250,
				"name": map[string]any{
					"value":          "StorageAccounts",
					"localizedValue": "Storage Accounts",
				},
			},
		},
	})
}

func handleStorageDeletedAccountsList(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleStorageDeletedAccountGet(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "deletedAccountName")
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The deleted account %q was not found.", name)
}

func handleStorageAccountsListByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/", sub, rg)
	var value []StorageAccount
	if azStorageAccounts != nil {
		value = azStorageAccounts.Filter(func(a StorageAccount) bool { return strings.HasPrefix(a.ID, prefix) })
		for i := range value {
			applyStorageAccountEndpoints(r, &value[i])
		}
	}
	if value == nil {
		value = []StorageAccount{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

// storageAccountKeysBody is the AccountListKeysResult shape listKeys and
// regenerateKey both return, reflecting every rotation performed so far.
func storageAccountKeysBody(acctID string) map[string]any {
	return map[string]any{
		"keys": []map[string]any{
			{"keyName": "key1", "value": azureKeyMaterial64(acctID, "key1"), "permissions": "Full"},
			{"keyName": "key2", "value": azureKeyMaterial64(acctID, "key2"), "permissions": "Full"},
		},
	}
}

// storageDropAccountKeyGens removes a deleted account's key-rotation state so
// a later account created under the same name starts from fresh keys.
func storageDropAccountKeyGens(acctID string) {
	azureDropKeyGens(acctID, "key1", "key2")
}

func handleStorageRegenerateKey(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		KeyName string `json:"keyName"`
	}
	_ = sim.ReadJSON(r, &req)
	// AccountRegenerateKeyParameters names one of the two access keys; kerb1 /
	// kerb2 exist only on accounts with Azure AD DS authentication, which the
	// simulator does not model.
	if req.KeyName != "key1" && req.KeyName != "key2" {
		AzureErrorf(w, "InvalidRequestPropertyValue", http.StatusBadRequest,
			"The value '%s' is not valid for property 'keyName'.", req.KeyName)
		return
	}
	azureBumpKeyGen(acctID, req.KeyName, "")
	sim.WriteJSON(w, http.StatusOK, storageAccountKeysBody(acctID))
}

func storageSASToken(seed string) string {
	sig := simListKey32(seed, "sas")
	return "sv=2024-01-01&ss=bfqt&srt=sco&sp=rwdlacupx&se=2030-01-01T00:00:00Z&st=2020-01-01T00:00:00Z&spr=https&sig=" + url.QueryEscape(sig)
}

func handleStorageListAccountSAS(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"accountSasToken": storageSASToken(acctID + "|account")})
}

func handleStorageListServiceSAS(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"serviceSasToken": storageSASToken(acctID + "|service")})
}

func handleStorageFailover(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	location := "eastus"
	if acct, found := azStorageAccounts.Get(acctID); found {
		location = acct.Location
	}
	writeStorageAsyncAccepted(w, r, location)
}

// writeStorageAsyncAccepted issues a Microsoft.Storage long-running-operation
// 202 with both poll URLs pointing at the shared operation-status handler so the
// SDK's azure-async-operation and Location-based pollers both resolve.
func writeStorageAsyncAccepted(w http.ResponseWriter, r *http.Request, location string) {
	sub := sim.PathParam(r, "subscriptionId")
	apiVersion := r.URL.Query().Get("api-version")
	opID := issueAzureAsyncOperation(nil)
	statusURL := azureAsyncOperationHeader(r, sub, "Microsoft.Storage", location, "operationStatuses", opID, apiVersion)
	resultURL := azureAsyncOperationHeader(r, sub, "Microsoft.Storage", location, "operationResults", opID, apiVersion)
	writeAzureAsyncCreateHeaders(w, statusURL, resultURL)
	w.WriteHeader(http.StatusAccepted)
}

func handleStorageRevokeUserDelegationKeys(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := requireStorageAccount(w, r); !ok {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleStoragePrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	groups := []string{"blob", "file", "queue", "table", "web", "dfs"}
	var value []map[string]any
	for _, g := range groups {
		value = append(value, map[string]any{
			"id":   acctID + "/privateLinkResources/" + g,
			"name": g,
			"type": "Microsoft.Storage/storageAccounts/privateLinkResources",
			"properties": map[string]any{
				"groupId":           g,
				"requiredMembers":   []string{g},
				"requiredZoneNames": []string{"privatelink." + g + ".core.windows.net"},
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

func handleStorageLocalUserListKeys(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	username := sim.PathParam(r, "username")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"sharedKey": simListKey64(acctID+"|"+username, "localuser"),
	})
}

func handleStorageLocalUserRegeneratePassword(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	username := sim.PathParam(r, "username")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"sshPassword": simListKey32(acctID+"|"+username, "sshpassword"),
	})
}

func storageServiceResponse(acctID, service, typeStr string, props map[string]any) map[string]any {
	merged := map[string]any{"cors": map[string]any{"corsRules": []any{}}}
	for k, v := range props {
		merged[k] = v
	}
	return map[string]any{
		"id":         acctID + "/" + service + "/default",
		"name":       "default",
		"type":       typeStr,
		"properties": merged,
	}
}

func storageServiceListHandler(service, typeStr string, store sim.Store[map[string]any]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		props, _ := store.Get(acctID + "/" + service)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []map[string]any{storageServiceResponse(acctID, service, typeStr, props)},
		})
	}
}

func storageServiceSetHandler(service, typeStr string, store sim.Store[map[string]any]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		props, err := readARMProperties(r)
		if err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		store.Put(acctID+"/"+service, props)
		sim.WriteJSON(w, http.StatusOK, storageServiceResponse(acctID, service, typeStr, props))
	}
}

func storageContainerID(acctID, name string) string {
	return acctID + "/blobServices/default/containers/" + name
}

func requireBlobContainer(w http.ResponseWriter, r *http.Request) (BlobContainer, bool) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return BlobContainer{}, false
	}
	name := sim.PathParam(r, "containerName")
	if azBlobContainers == nil {
		AzureErrorf(w, "ContainerNotFound", http.StatusNotFound, "The specified container %q was not found.", name)
		return BlobContainer{}, false
	}
	c, found := azBlobContainers.Get(storageContainerID(acctID, name))
	if !found {
		AzureErrorf(w, "ContainerNotFound", http.StatusNotFound, "The specified container %q was not found.", name)
		return BlobContainer{}, false
	}
	return c, true
}

func handleStorageContainerUpdate(w http.ResponseWriter, r *http.Request) {
	c, ok := requireBlobContainer(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			PublicAccess string            `json:"publicAccess"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"properties"`
	}
	_ = sim.ReadJSON(r, &req)
	azBlobContainers.Update(c.ID, func(stored *BlobContainer) {
		if req.Properties.PublicAccess != "" {
			stored.Properties.PublicAccess = req.Properties.PublicAccess
		}
		if req.Properties.Metadata != nil {
			stored.Properties.Metadata = req.Properties.Metadata
		}
		stored.Properties.LastModifiedTime = storageNow()
	})
	updated, _ := azBlobContainers.Get(c.ID)
	sim.WriteJSON(w, http.StatusOK, updated)
}

func handleStorageContainerSetLegalHold(w http.ResponseWriter, r *http.Request) {
	c, ok := requireBlobContainer(w, r)
	if !ok {
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	_ = sim.ReadJSON(r, &req)
	azBlobContainers.Update(c.ID, func(stored *BlobContainer) { stored.Properties.HasLegalHold = len(req.Tags) > 0 })
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"hasLegalHold": len(req.Tags) > 0,
		"tags":         req.Tags,
	})
}

func handleStorageContainerClearLegalHold(w http.ResponseWriter, r *http.Request) {
	c, ok := requireBlobContainer(w, r)
	if !ok {
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	_ = sim.ReadJSON(r, &req)
	azBlobContainers.Update(c.ID, func(stored *BlobContainer) { stored.Properties.HasLegalHold = false })
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"hasLegalHold": false,
		"tags":         []string{},
	})
}

func handleStorageContainerLease(w http.ResponseWriter, r *http.Request) {
	c, ok := requireBlobContainer(w, r)
	if !ok {
		return
	}
	var req struct {
		Action          string `json:"action"`
		ProposedLeaseID string `json:"proposedLeaseId"`
		LeaseID         string `json:"leaseId"`
	}
	_ = sim.ReadJSON(r, &req)
	resp := map[string]any{}
	switch req.Action {
	case "Break":
		resp["leaseTimeSeconds"] = "0"
	default:
		leaseID := req.ProposedLeaseID
		if leaseID == "" {
			leaseID = req.LeaseID
		}
		if leaseID == "" {
			leaseID = generateUUID()
		}
		resp["leaseId"] = leaseID
	}
	_ = c
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleStorageContainerObjectLevelWorm(w http.ResponseWriter, r *http.Request) {
	c, ok := requireBlobContainer(w, r)
	if !ok {
		return
	}
	_ = c
	writeStorageAsyncAccepted(w, r, "eastus")
}

// immutability policies (one per container; Azure names it "default")

func storageImmutabilityPut(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := requireBlobContainer(w, r)
		if !ok {
			return
		}
		props, err := readARMProperties(r)
		if err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		props["state"] = "Unlocked"
		id := c.ID + "/immutabilityPolicies/default"
		policy := storageARMChild{
			ID:         id,
			Name:       "default",
			Type:       "Microsoft.Storage/storageAccounts/blobServices/containers/immutabilityPolicies",
			Etag:       fmt.Sprintf("%q", generateUUID()),
			Properties: props,
		}
		store.Put(id, policy)
		azBlobContainers.Update(c.ID, func(stored *BlobContainer) { stored.Properties.HasImmutability = true })
		w.Header().Set("ETag", policy.Etag)
		sim.WriteJSON(w, http.StatusOK, policy)
	}
}

func storageImmutabilityGet(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := requireBlobContainer(w, r)
		if !ok {
			return
		}
		policy, found := store.Get(c.ID + "/immutabilityPolicies/default")
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "No immutability policy found on container %q.", c.Name)
			return
		}
		w.Header().Set("ETag", policy.Etag)
		sim.WriteJSON(w, http.StatusOK, policy)
	}
}

func storageImmutabilityDelete(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := requireBlobContainer(w, r)
		if !ok {
			return
		}
		id := c.ID + "/immutabilityPolicies/default"
		policy, found := store.Get(id)
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "No immutability policy found on container %q.", c.Name)
			return
		}
		store.Delete(id)
		azBlobContainers.Update(c.ID, func(stored *BlobContainer) { stored.Properties.HasImmutability = false })
		w.Header().Set("ETag", policy.Etag)
		sim.WriteJSON(w, http.StatusOK, policy)
	}
}

func storageImmutabilityMutate(store sim.Store[storageARMChild], lock bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := requireBlobContainer(w, r)
		if !ok {
			return
		}
		id := c.ID + "/immutabilityPolicies/default"
		policy, found := store.Get(id)
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "No immutability policy found on container %q.", c.Name)
			return
		}
		if lock {
			policy.Properties["state"] = "Locked"
		} else {
			ext, err := readARMProperties(r)
			if err == nil {
				if v, present := ext["immutabilityPeriodSinceCreationInDays"]; present {
					policy.Properties["immutabilityPeriodSinceCreationInDays"] = v
				}
			}
		}
		policy.Etag = fmt.Sprintf("%q", generateUUID())
		store.Put(id, policy)
		w.Header().Set("ETag", policy.Etag)
		sim.WriteJSON(w, http.StatusOK, policy)
	}
}

func storageImmutabilityLock(store sim.Store[storageARMChild]) http.HandlerFunc {
	return storageImmutabilityMutate(store, true)
}

func storageImmutabilityExtend(store sim.Store[storageARMChild]) http.HandlerFunc {
	return storageImmutabilityMutate(store, false)
}

func handleStorageFileServiceUsages(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{
			{
				"id":         acctID + "/fileServices/default/usages/default",
				"name":       "default",
				"type":       "Microsoft.Storage/storageAccounts/fileServices/usages",
				"properties": map[string]any{},
			},
		},
	})
}

func handleStorageFileServiceUsageGet(w http.ResponseWriter, r *http.Request) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	name := sim.PathParam(r, "fileServiceUsagesName")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":         acctID + "/fileServices/default/usages/" + name,
		"name":       name,
		"type":       "Microsoft.Storage/storageAccounts/fileServices/usages",
		"properties": map[string]any{},
	})
}

func requireFileShare(w http.ResponseWriter, r *http.Request) (FileShare, bool) {
	acctID, _, ok := requireStorageAccount(w, r)
	if !ok {
		return FileShare{}, false
	}
	name := sim.PathParam(r, "shareName")
	shareID := acctID + "/fileServices/default/shares/" + name
	if azFileShares == nil {
		AzureErrorf(w, "ShareNotFound", http.StatusNotFound, "The file share %q was not found.", name)
		return FileShare{}, false
	}
	share, found := azFileShares.Get(shareID)
	if !found {
		AzureErrorf(w, "ShareNotFound", http.StatusNotFound, "The file share %q was not found.", name)
		return FileShare{}, false
	}
	return share, true
}

func handleStorageFileShareUpdate(w http.ResponseWriter, r *http.Request) {
	share, ok := requireFileShare(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			ShareQuota int               `json:"shareQuota"`
			AccessTier string            `json:"accessTier"`
			Metadata   map[string]string `json:"metadata"`
		} `json:"properties"`
	}
	_ = sim.ReadJSON(r, &req)
	azFileShares.Update(share.ID, func(stored *FileShare) {
		if req.Properties.ShareQuota != 0 {
			stored.Properties.ShareQuota = req.Properties.ShareQuota
		}
		if req.Properties.AccessTier != "" {
			stored.Properties.AccessTier = req.Properties.AccessTier
		}
		if req.Properties.Metadata != nil {
			stored.Properties.Metadata = req.Properties.Metadata
		}
		stored.Properties.LastModifiedTime = storageNow()
	})
	updated, _ := azFileShares.Get(share.ID)
	sim.WriteJSON(w, http.StatusOK, updated)
}

func handleStorageFileShareRestore(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFileShare(w, r); !ok {
		// Restore can target a soft-deleted share; treat the named share as the
		// restore subject and succeed when the live share exists.
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleStorageFileShareLease(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFileShare(w, r); !ok {
		return
	}
	var req struct {
		Action          string `json:"action"`
		ProposedLeaseID string `json:"proposedLeaseId"`
		LeaseID         string `json:"leaseId"`
	}
	_ = sim.ReadJSON(r, &req)
	resp := map[string]any{}
	if req.Action == "Break" {
		resp["leaseTimeSeconds"] = "0"
	} else {
		leaseID := req.ProposedLeaseID
		if leaseID == "" {
			leaseID = req.LeaseID
		}
		if leaseID == "" {
			leaseID = generateUUID()
		}
		resp["leaseId"] = leaseID
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func storageQueueID(acctID, name string) string {
	return acctID + "/queueServices/default/queues/" + name
}

func storageQueuePut(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "queueName")
		var req struct {
			Properties struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"properties"`
		}
		if r.Body != nil {
			if err := sim.ReadJSON(r, &req); err != nil && err != io.EOF {
				AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		props := map[string]any{}
		if req.Properties.Metadata != nil {
			props["metadata"] = req.Properties.Metadata
		}
		id := storageQueueID(acctID, name)
		q := storageARMChild{
			ID:         id,
			Name:       name,
			Type:       "Microsoft.Storage/storageAccounts/queueServices/queues",
			Properties: props,
		}
		store.Put(id, q)
		sim.WriteJSON(w, http.StatusOK, q)
	}
}

func storageQueueGet(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "queueName")
		q, found := store.Get(storageQueueID(acctID, name))
		if !found {
			AzureErrorf(w, "QueueNotFound", http.StatusNotFound, "The specified queue %q was not found.", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, q)
	}
}

func storageQueueDelete(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		name := sim.PathParam(r, "queueName")
		if store.Delete(storageQueueID(acctID, name)) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		AzureErrorf(w, "QueueNotFound", http.StatusNotFound, "The specified queue %q was not found.", name)
	}
}

func storageQueueList(store sim.Store[storageARMChild]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acctID, _, ok := requireStorageAccount(w, r)
		if !ok {
			return
		}
		prefix := acctID + "/queueServices/default/queues/"
		items := store.Filter(func(c storageARMChild) bool { return strings.HasPrefix(c.ID, prefix) })
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		if items == nil {
			items = []storageARMChild{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	}
}
