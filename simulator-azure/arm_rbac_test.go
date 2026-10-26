package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestARMOperationOfNamesTheActionAzureChecks(t *testing.T) {
	const rg = "/subscriptions/s/resourceGroups/rg"
	cases := []struct {
		method, path, action, scope string
		collection                  bool
	}{
		{http.MethodGet, "/subscriptions", "Microsoft.Resources/subscriptions/read", "/", true},
		{http.MethodGet, "/subscriptions/s/resourceGroups", "Microsoft.Resources/subscriptions/resourceGroups/read", "/subscriptions/s", true},
		{http.MethodPut, rg, "Microsoft.Resources/subscriptions/resourceGroups/write", rg, false},
		{http.MethodDelete, rg, "Microsoft.Resources/subscriptions/resourceGroups/delete", rg, false},
		{http.MethodHead, rg, "Microsoft.Resources/subscriptions/resourceGroups/read", rg, false},
		{http.MethodGet, rg + "/resources", "Microsoft.Resources/subscriptions/resourceGroups/resources/read", rg, true},
		{http.MethodPost, rg + "/moveResources", "Microsoft.Resources/subscriptions/resourceGroups/moveResources/action", rg, false},
		{http.MethodPost, "/subscriptions/s/providers/Microsoft.Storage/register", "Microsoft.Storage/register/action", "/subscriptions/s", false},
		{http.MethodGet, rg + "/providers/Microsoft.Storage/storageAccounts", "Microsoft.Storage/storageAccounts/read", rg, true},
		{http.MethodPut, rg + "/providers/Microsoft.Storage/storageAccounts/a", "Microsoft.Storage/storageAccounts/write", rg + "/providers/Microsoft.Storage/storageAccounts/a", false},
		{http.MethodPatch, rg + "/providers/Microsoft.Storage/storageAccounts/a", "Microsoft.Storage/storageAccounts/write", rg + "/providers/Microsoft.Storage/storageAccounts/a", false},
		{http.MethodPost, rg + "/providers/Microsoft.Storage/storageAccounts/a/listKeys", "Microsoft.Storage/storageAccounts/listKeys/action", rg + "/providers/Microsoft.Storage/storageAccounts/a", false},
		{http.MethodPut, rg + "/providers/Microsoft.Storage/storageAccounts/a/blobServices/default/containers/c",
			"Microsoft.Storage/storageAccounts/blobServices/containers/write", rg + "/providers/Microsoft.Storage/storageAccounts/a/blobServices/default/containers/c", false},
		{http.MethodPost, rg + "/providers/Microsoft.Web/sites/w/config/appsettings/list", "Microsoft.Web/sites/config/list/action", rg + "/providers/Microsoft.Web/sites/w/config/appsettings", false},
		{http.MethodPost, rg + "/providers/Microsoft.Web/sites/w/networkTrace/start", "Microsoft.Web/sites/networkTrace/start/action", rg + "/providers/Microsoft.Web/sites/w/networkTrace", false},
		{http.MethodPut, rg + "/providers/Microsoft.KeyVault/vaults/v/providers/Microsoft.Authorization/roleAssignments/g",
			"Microsoft.Authorization/roleAssignments/write", rg + "/providers/Microsoft.KeyVault/vaults/v/providers/Microsoft.Authorization/roleAssignments/g", false},
		{http.MethodGet, "/subscriptions/s/providers/Microsoft.Authorization/roleAssignments", "Microsoft.Authorization/roleAssignments/read", "/subscriptions/s", true},
		{http.MethodPut, "/subscriptions/s/tagNames/n/tagValues/v", "Microsoft.Resources/subscriptions/tagNames/tagValues/write", "/subscriptions/s/tagNames/n/tagValues/v", false},
	}
	for _, c := range cases {
		got := armOperationOf(c.method, c.path)
		if got.action != c.action || got.scope != c.scope || got.collection != c.collection {
			t.Errorf("%s %s = %+v, want action %q scope %q collection %v", c.method, c.path, got, c.action, c.scope, c.collection)
		}
	}
}

// TestARMOperationOfDerivesEveryVendoredOperation derives the action of every
// Azure Resource Manager operation the vendored Swagger documents: its
// namespace is the provider the path names after its last /providers/
// segment, a POST to a literal segment is that segment's /action, and every
// other verb maps to read, write or delete.
func TestARMOperationOfDerivesEveryVendoredOperation(t *testing.T) {
	all, _ := loadSwaggerPaths(t)
	checked := 0
	for _, op := range all {
		if len(op.Raw) == 0 || (!strings.EqualFold(op.Raw[0], "subscriptions") && !strings.EqualFold(op.Raw[0], "providers")) {
			continue
		}
		var segs []string
		scoped := false
		for _, seg := range op.Raw {
			if strings.HasPrefix(seg, "{") {
				name := strings.Trim(seg, "{}")
				if op.PathScopes[name] {
					scoped = true
					break
				}
				segs = append(segs, "x"+strings.ToLower(name))
				continue
			}
			segs = append(segs, seg)
		}
		if scoped {
			continue
		}
		path := "/" + strings.Join(segs, "/")
		got := armOperationOf(op.Method, path)
		namespace := "Microsoft.Resources"
		for i := len(op.Raw) - 2; i >= 0; i-- {
			if strings.EqualFold(op.Raw[i], "providers") {
				namespace = segs[i+1]
				break
			}
		}
		if !strings.HasPrefix(got.action, namespace+"/") {
			t.Errorf("%s %s: action %q is not in namespace %q", op.Method, path, got.action, namespace)
		}
		last := op.Raw[len(op.Raw)-1]
		var suffix string
		switch op.Method {
		case http.MethodGet, http.MethodHead:
			suffix = "/read"
		case http.MethodPut, http.MethodPatch:
			suffix = "/write"
		case http.MethodDelete:
			suffix = "/delete"
		case http.MethodPost:
			if strings.HasPrefix(last, "{") {
				continue
			}
			suffix = "/" + last + "/action"
		}
		if suffix != "" && !strings.HasSuffix(strings.ToLower(got.action), strings.ToLower(suffix)) {
			t.Errorf("%s %s: action %q does not end in %q", op.Method, path, got.action, suffix)
		}
		if strings.ContainsAny(got.action, "{}") || !strings.HasPrefix(got.scope, "/") {
			t.Errorf("%s %s: malformed operation %+v", op.Method, path, got)
		}
		checked++
	}
	if checked < 500 {
		t.Fatalf("derived %d vendored operations, want the whole vendored ARM surface", checked)
	}
}
