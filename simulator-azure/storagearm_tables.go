package main

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerStorageTablesARM(srv *sim.Server, armBase string, storageAccounts sim.Store[StorageAccount]) {
	tableBasePath := armBase + "/storageAccounts/{accountName}/tableServices/default/tables"

	writeStorageTableNotFound := func(w http.ResponseWriter, table, account string) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Storage table %q not found in storage account %q", table, account)
	}
	readStorageTableBody := func(r *http.Request) (StorageTable, error) {
		var req StorageTable
		if r.Body == nil {
			return req, nil
		}
		err := sim.ReadJSON(r, &req)
		if err != nil && err != io.EOF {
			return req, err
		}
		return req, nil
	}
	upsertStorageTable := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		table := sim.PathParam(r, "tableName")
		acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, account)
		if _, ok := storageAccounts.Get(acctID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		req, err := readStorageTableBody(r)
		if err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		resourceID := storageTableResourceID(sub, rg, account, table)
		props := req.Properties
		props.TableName = table
		out := StorageTable{
			ID:         resourceID,
			Name:       table,
			Type:       "Microsoft.Storage/storageAccounts/tableServices/tables",
			Properties: props,
		}
		storageTables.Put(resourceID, out)
		upsertTableDataPlaneProjection(account, table)
		sim.WriteJSON(w, http.StatusOK, out)
	}
	srv.HandleFunc("PUT "+tableBasePath+"/{tableName}", upsertStorageTable)
	srv.HandleFunc("PATCH "+tableBasePath+"/{tableName}", upsertStorageTable)

	srv.HandleFunc("GET "+tableBasePath+"/{tableName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		table := sim.PathParam(r, "tableName")
		resourceID := storageTableResourceID(sub, rg, account, table)
		t, ok := storageTables.Get(resourceID)
		if !ok {
			writeStorageTableNotFound(w, table, account)
			return
		}
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("DELETE "+tableBasePath+"/{tableName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		table := sim.PathParam(r, "tableName")
		resourceID := storageTableResourceID(sub, rg, account, table)
		if !storageTables.Delete(resourceID) {
			writeStorageTableNotFound(w, table, account)
			return
		}
		deleteTableDataPlaneProjection(account, table)
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET "+tableBasePath, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/tableServices/default/tables/",
			sub, rg, account)
		all := storageTables.Filter(func(t StorageTable) bool {
			return strings.HasPrefix(t.ID, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		if all == nil {
			all = []StorageTable{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})
}

// StorageTable is a Microsoft.Storage ARM table resource. It is the
// control-plane projection of an Azure Tables data-plane table.
type StorageTable struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Properties StorageTableProperties `json:"properties"`
}

type StorageTableProperties struct {
	TableName         string           `json:"tableName,omitempty"`
	SignedIdentifiers []map[string]any `json:"signedIdentifiers,omitempty"`
}

func storageTableResourceID(sub, rg, account, table string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/tableServices/default/tables/%s",
		sub, rg, account, table)
}

func storageTableResourceIDForAccount(account, table string) (string, bool) {
	acct, ok := azureStorageAccountByName(account)
	if !ok {
		return "", false
	}
	return acct.ID + "/tableServices/default/tables/" + table, true
}

func upsertStorageTableARMProjection(account, table string) {
	if storageTables == nil {
		return
	}
	resourceID, ok := storageTableResourceIDForAccount(account, table)
	if !ok {
		return
	}
	storageTables.Put(resourceID, StorageTable{
		ID:   resourceID,
		Name: table,
		Type: "Microsoft.Storage/storageAccounts/tableServices/tables",
		Properties: StorageTableProperties{
			TableName: table,
		},
	})
}

func deleteStorageTableARMProjection(account, table string) {
	if storageTables == nil {
		return
	}
	if resourceID, ok := storageTableResourceIDForAccount(account, table); ok {
		storageTables.Delete(resourceID)
	}
}
