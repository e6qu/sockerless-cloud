package main

import (
	"net/http/httptest"
	"testing"
)

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
