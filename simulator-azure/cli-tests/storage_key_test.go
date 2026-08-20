package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// The storage data plane authorizes every request, so `az storage` runs with
// the account's real key.
//
// Every storage CLI test used to pass one hardcoded constant as
// `--account-key`. The az CLI signs each request with whatever key it is
// handed — the signing is real — and the simulator accepted the result only
// because it verified nothing. Now that it verifies, a made-up key is exactly
// what it looks like: a credential the account's keys did not produce.
//
// cliStorageAccountKey is the operator's flow instead: the account is created
// through Azure Resource Manager if the subscription does not hold it, and the
// key handed to az is the one listKeys serves — the same two steps
// `az storage account create` + `az storage account keys list` perform.
// Provisioning goes over raw ARM HTTP rather than az because az adds seconds
// per call and the flow is identical on the wire.

var (
	cliStorageKeyMu    sync.Mutex
	cliStorageKeys     = map[string]string{}
	cliStorageKeyGroup = "cli-storage-key-rg"
)

// cliStorageAccountKey panics on failure rather than taking a testing.T,
// because the az argument builders it feeds are plain functions; a panic in a
// test fails that test with the message intact.
func cliStorageAccountKey(account string) string {
	cliStorageKeyMu.Lock()
	defer cliStorageKeyMu.Unlock()
	if key, ok := cliStorageKeys[account]; ok {
		return key
	}
	arm := func(method, path, body string) (int, []byte) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			panic(fmt.Sprintf("build ARM request: %v", err))
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+armBearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			panic(fmt.Sprintf("ARM request %s %s: %v", method, path, err))
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, payload
	}

	// The account may already exist — a test that moves or creates it owns its
	// resource group — and an account name is a hostname, so at most one
	// holds it and its keys are the only keys.
	acctID := ""
	if code, body := arm(http.MethodGet,
		"/subscriptions/"+subscriptionID+"/providers/Microsoft.Storage/storageAccounts?api-version=2023-05-01", ""); code < 300 {
		var listed struct {
			Value []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"value"`
		}
		if json.Unmarshal(body, &listed) == nil {
			for _, held := range listed.Value {
				if strings.EqualFold(held.Name, account) {
					acctID = held.ID
				}
			}
		}
	}
	if acctID == "" {
		base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + cliStorageKeyGroup
		if code, body := arm(http.MethodPut, base+"?api-version=2023-07-01", `{"location":"eastus"}`); code >= 300 {
			panic(fmt.Sprintf("create resource group %s: status %d: %s", cliStorageKeyGroup, code, body))
		}
		acctID = base + "/providers/Microsoft.Storage/storageAccounts/" + account
		if code, body := arm(http.MethodPut, acctID+"?api-version=2023-05-01",
			`{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}`); code >= 300 {
			panic(fmt.Sprintf("create storage account %s: status %d: %s", account, code, body))
		}
	}
	code, body := arm(http.MethodPost, acctID+"/listKeys?api-version=2023-05-01", "")
	if code >= 300 {
		panic(fmt.Sprintf("listKeys for %s: status %d: %s", account, code, body))
	}
	var listed struct {
		Keys []struct {
			KeyName string `json:"keyName"`
			Value   string `json:"value"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		panic(fmt.Sprintf("decode listKeys for %s: %v: %s", account, err, body))
	}
	for _, k := range listed.Keys {
		if k.KeyName == "key1" && k.Value != "" {
			cliStorageKeys[account] = k.Value
			return k.Value
		}
	}
	panic(fmt.Sprintf("listKeys for %s served no key1: %s", account, body))
}
