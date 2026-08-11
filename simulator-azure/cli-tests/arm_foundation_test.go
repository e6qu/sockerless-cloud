package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestCLIAzureARMFoundationEndpoints(t *testing.T) {
	port := strings.TrimPrefix(baseURL, "http://127.0.0.1:")
	storageName := "clishimsa"
	storageURL := armURL("Microsoft.Storage", "storageAccounts/"+storageName, "2023-05-01")
	out := runCLI(t, azRest("PUT", storageURL, `{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}`))
	var storage struct {
		Properties struct {
			PrimaryEndpoints map[string]string `json:"primaryEndpoints"`
		} `json:"properties"`
	}
	parseJSON(t, out, &storage)
	wantBlob := fmt.Sprintf("http://%s.blob.cli-shim.localhost:%s/", storageName, port)
	if storage.Properties.PrimaryEndpoints["blob"] != wantBlob {
		t.Fatalf("blob endpoint = %q, want %q", storage.Properties.PrimaryEndpoints["blob"], wantBlob)
	}

	out = runCLI(t, azRest("PATCH", storageURL, `{"tags":{"env":"cli"}}`))
	var patchedStorage struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &patchedStorage)
	if patchedStorage.Tags["env"] != "cli" {
		t.Fatalf("storage PATCH tags not persisted: %#v", patchedStorage.Tags)
	}

	vaultName := "clishimkv"
	vaultURL := armURL("Microsoft.KeyVault", "vaults/"+vaultName, "2024-11-01")
	out = runCLI(t, azRest("PUT", vaultURL, `{"location":"eastus","properties":{"tenantId":"00000000-0000-0000-0000-000000000000","sku":{"family":"A","name":"standard"}}}`))
	var vault struct {
		Properties struct {
			VaultURI string `json:"vaultUri"`
		} `json:"properties"`
	}
	parseJSON(t, out, &vault)
	wantVaultURI := fmt.Sprintf("https://%s.vault.cli-shim.localhost:%s/", vaultName, port)
	if vault.Properties.VaultURI != wantVaultURI {
		t.Fatalf("vaultUri = %q, want %q", vault.Properties.VaultURI, wantVaultURI)
	}

	out = runCLI(t, azRest("PUT", strings.TrimSuffix(vaultURL, "?api-version=2024-11-01")+"/accessPolicies/add?api-version=2024-11-01",
		`{"properties":{"accessPolicies":[{"tenantId":"00000000-0000-0000-0000-000000000000","objectId":"22222222-2222-2222-2222-222222222222","permissions":{"secrets":["get"]}}]}}`))
	var accessPolicies struct {
		Properties struct {
			AccessPolicies []any `json:"accessPolicies"`
		} `json:"properties"`
	}
	parseJSON(t, out, &accessPolicies)
	if len(accessPolicies.Properties.AccessPolicies) != 1 {
		t.Fatalf("access policy add returned %d policies", len(accessPolicies.Properties.AccessPolicies))
	}

	out = runCLI(t, azRest("GET", fmt.Sprintf("%s/subscriptions/%s/resourceGroups?api-version=2021-04-01", baseURL, subscriptionID), ""))
	var groups struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &groups)
	if len(groups.Value) == 0 {
		t.Fatal("resource group list returned no resource groups")
	}
}

func TestCLIAzureARMRequiresAPIVersion(t *testing.T) {
	cmd := azRest("GET", fmt.Sprintf("%s/subscriptions/%s/resourceGroups", baseURL, subscriptionID), "")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected missing api-version request to fail, got output: %s", out)
	}
	text := string(out)
	if !strings.Contains(text, "InvalidApiVersionParameter") {
		t.Fatalf("expected InvalidApiVersionParameter in az output, got: %s", text)
	}
	if !strings.Contains(text, "api-version query parameter") {
		t.Fatalf("expected api-version message in az output, got: %s", text)
	}
}
