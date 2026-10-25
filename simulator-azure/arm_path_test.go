package main

import (
	"net/http/httptest"
	"testing"
)

func TestAzureNormalizeRequestPathLeavesNonARMPathsAlone(t *testing.T) {
	cases := map[string]string{
		"/subscriptions/s/resourcegroups/rg/providers/microsoft.storage/storageAccounts/a": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/a",
		"/providers/microsoft.storage/operations":                                          "/providers/Microsoft.Storage/operations",
		"/ui/browse/resourcegroups":                                                        "/ui/browse/resourcegroups",
		"/logs/2026/resourcegroups/microsoft.keyvault/audit.json":                          "/logs/2026/resourcegroups/microsoft.keyvault/audit.json",
		"/v1.0/applications":                                                               "/v1.0/applications",
	}
	for in, want := range cases {
		r := httptest.NewRequest("GET", in, nil)
		azureNormalizeRequestPath(r)
		if r.URL.Path != want {
			t.Errorf("%s normalized to %s, want %s", in, r.URL.Path, want)
		}
	}
}

func TestAzureNormalizeRequestPathFoldsSiteKeyListings(t *testing.T) {
	site := "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app"
	cases := map[string]string{
		site + "/host/default/listKeys":    site + "/host/default/listkeys",
		site + "/functions/hello/listKeys": site + "/functions/hello/listkeys",
		"/subscriptions/s/resourceGroups/rg/providers/Microsoft.EventGrid/topics/t/listKeys": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.EventGrid/topics/t/listKeys",
	}
	for in, want := range cases {
		r := httptest.NewRequest("POST", in, nil)
		azureNormalizeRequestPath(r)
		if r.URL.Path != want {
			t.Errorf("%s normalized to %s, want %s", in, r.URL.Path, want)
		}
	}
}
