package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// newTagsTestServer builds a simulator carrying the slices the tags surface
// resolves a scope through: resource groups (a scope that keeps its tags on
// its own record), Key Vault (a tracked resource type), and the tags routes
// themselves.
func newTagsTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new azure sim server: %v", err)
	}
	registerResourceGroups(srv)
	registerKeyVault(srv)
	registerTags(srv)
	return srv
}

func tagsTestServe(t *testing.T, srv *sim.Server, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, data
}

func tagsTestDecode(t *testing.T, data []byte) TagsResource {
	t.Helper()
	var out TagsResource
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode tags response %s: %v", data, err)
	}
	return out
}

const tagsTestAPI = "?api-version=2021-04-01"

// TestTagsDefaultAndResourceAgreeOnOneSet holds the invariant Azure Resource
// Manager guarantees: a scope has one set of tags, and tags/default and the
// scope's own API are two spellings of it. A write through either surface is
// visible through the other, and through the generic resource list.
func TestTagsDefaultAndResourceAgreeOnOneSet(t *testing.T) {
	srv := newTagsTestServer(t)

	const sub = "00000000-0000-0000-0000-000000000001"
	const rg = "tags-rg"
	rgPath := "/subscriptions/" + sub + "/resourceGroups/" + rg
	if status, body := tagsTestServe(t, srv, http.MethodPut, rgPath+tagsTestAPI,
		`{"location":"eastus"}`); status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create resource group: status %d: %s", status, body)
	}

	vaultPath := rgPath + "/providers/Microsoft.KeyVault/vaults/tags-vault"
	if status, body := tagsTestServe(t, srv, http.MethodPut, vaultPath+tagsTestAPI,
		`{"location":"eastus","tags":{"origin":"resource-put"},"properties":{"tenantId":"11111111-1111-1111-1111-111111111111","sku":{"family":"A","name":"standard"}}}`); status != http.StatusOK {
		t.Fatalf("create vault: status %d: %s", status, body)
	}

	// A tag the resource's own PUT wrote is visible through tags/default.
	status, body := tagsTestServe(t, srv, http.MethodGet, vaultPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get tags at vault scope: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags["origin"]; got != "resource-put" {
		t.Errorf("tags/default at the vault scope reported origin=%q, want the tag the vault's own PUT wrote", got)
	}

	// A Merge through tags/default is visible on the resource's own GET.
	status, body = tagsTestServe(t, srv, http.MethodPatch,
		vaultPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"operation":"Merge","properties":{"tags":{"team":"platform"}}}`)
	if status != http.StatusOK {
		t.Fatalf("merge tags at vault scope: status %d: %s", status, body)
	}
	merged := tagsTestDecode(t, body).Properties.Tags
	if merged["origin"] != "resource-put" || merged["team"] != "platform" {
		t.Errorf("Merge reported %v, want both the pre-existing and the merged tag", merged)
	}
	status, body = tagsTestServe(t, srv, http.MethodGet, vaultPath+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get vault: status %d: %s", status, body)
	}
	var vault struct {
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(body, &vault); err != nil {
		t.Fatalf("decode vault: %v", err)
	}
	if vault.Tags["team"] != "platform" || vault.Tags["origin"] != "resource-put" {
		t.Errorf("the vault's own GET reported %v, want the tags written through tags/default", vault.Tags)
	}

	// Replace drops what it does not name; Delete removes only the named keys.
	status, body = tagsTestServe(t, srv, http.MethodPatch,
		vaultPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"operation":"Replace","properties":{"tags":{"only":"this"}}}`)
	if status != http.StatusOK {
		t.Fatalf("replace tags: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags; len(got) != 1 || got["only"] != "this" {
		t.Errorf("Replace reported %v, want exactly the replacing set", got)
	}
	status, body = tagsTestServe(t, srv, http.MethodPatch,
		vaultPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"operation":"Delete","properties":{"tags":{"only":"this"}}}`)
	if status != http.StatusOK {
		t.Fatalf("delete tag: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags; len(got) != 0 {
		t.Errorf("Delete reported %v, want an empty set", got)
	}

	// The generic resource list reads the resource's own tags, so it agrees
	// with tags/default in both directions.
	status, body = tagsTestServe(t, srv, http.MethodPut,
		vaultPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"properties":{"tags":{"listed":"yes"}}}`)
	if status != http.StatusOK {
		t.Fatalf("put tags: status %d: %s", status, body)
	}
	rows := azureEnumerateResources(sub, rg)
	found := false
	for _, row := range rows {
		if !strings.EqualFold(row.ID, vaultPath) {
			continue
		}
		found = true
		if row.Tags["listed"] != "yes" {
			t.Errorf("the resource list reported tags %v, want the tag written through tags/default", row.Tags)
		}
	}
	if !found {
		t.Fatalf("the resource list did not report the vault at all: %v", rows)
	}

	// A resource-scope DELETE clears the resource's tags on its own API too.
	if status, body := tagsTestServe(t, srv, http.MethodDelete,
		vaultPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, ""); status != http.StatusOK {
		t.Fatalf("delete tags: status %d: %s", status, body)
	}
	status, body = tagsTestServe(t, srv, http.MethodGet, vaultPath+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get vault after tag delete: status %d: %s", status, body)
	}
	vault.Tags = nil
	if err := json.Unmarshal(body, &vault); err != nil {
		t.Fatalf("decode vault: %v", err)
	}
	if len(vault.Tags) != 0 {
		t.Errorf("the vault still reports %v after tags/default DELETE", vault.Tags)
	}
}

// TestTagsDefaultAtResourceGroupScopeWritesTheGroup pins the resource-group
// scope onto the group's own record, so `az tag` and `az group show` report
// the same tags.
func TestTagsDefaultAtResourceGroupScopeWritesTheGroup(t *testing.T) {
	srv := newTagsTestServer(t)

	const sub = "00000000-0000-0000-0000-000000000001"
	rgPath := "/subscriptions/" + sub + "/resourceGroups/tags-group-rg"
	if status, body := tagsTestServe(t, srv, http.MethodPut, rgPath+tagsTestAPI,
		`{"location":"eastus","tags":{"origin":"group-put"}}`); status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create resource group: status %d: %s", status, body)
	}

	status, body := tagsTestServe(t, srv, http.MethodGet, rgPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get tags at group scope: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags["origin"]; got != "group-put" {
		t.Errorf("tags/default at the group scope reported origin=%q, want the tag the group's own PUT wrote", got)
	}

	if status, body := tagsTestServe(t, srv, http.MethodPut,
		rgPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"properties":{"tags":{"cost":"42"}}}`); status != http.StatusOK {
		t.Fatalf("put tags at group scope: status %d: %s", status, body)
	}
	status, body = tagsTestServe(t, srv, http.MethodGet, rgPath+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get resource group: status %d: %s", status, body)
	}
	var group ResourceGroup
	if err := json.Unmarshal(body, &group); err != nil {
		t.Fatalf("decode resource group: %v", err)
	}
	if group.Tags["cost"] != "42" || group.Tags["origin"] != "" {
		t.Errorf("the group's own GET reported %v, want exactly the set tags/default wrote", group.Tags)
	}
}

// TestTagsDefaultAtSubscriptionScopeIsItsOwnStore covers the one scope with no
// record of its own: a subscription's tags live in the tags store, and a scope
// carrying none reads back the empty set rather than a 404, because the
// subscription itself always exists.
func TestTagsDefaultAtSubscriptionScopeIsItsOwnStore(t *testing.T) {
	srv := newTagsTestServer(t)

	subPath := "/subscriptions/00000000-0000-0000-0000-000000000001"
	status, body := tagsTestServe(t, srv, http.MethodGet, subPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get tags at subscription scope: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags; len(got) != 0 {
		t.Errorf("an untagged subscription reported %v, want the empty set", got)
	}

	if status, body := tagsTestServe(t, srv, http.MethodPut,
		subPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"properties":{"tags":{"env":"sandbox"}}}`); status != http.StatusOK {
		t.Fatalf("put tags at subscription scope: status %d: %s", status, body)
	}
	status, body = tagsTestServe(t, srv, http.MethodGet, subPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("re-read tags at subscription scope: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags["env"]; got != "sandbox" {
		t.Errorf("subscription tags read back %q, want sandbox", got)
	}

	// A management group is the other scope the simulator keeps no record for.
	mgPath := "/providers/Microsoft.Management/managementGroups/mygroup"
	if status, body := tagsTestServe(t, srv, http.MethodPut,
		mgPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI,
		`{"properties":{"tags":{"owner":"platform"}}}`); status != http.StatusOK {
		t.Fatalf("put tags at management-group scope: status %d: %s", status, body)
	}
	status, body = tagsTestServe(t, srv, http.MethodGet, mgPath+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, "")
	if status != http.StatusOK {
		t.Fatalf("get tags at management-group scope: status %d: %s", status, body)
	}
	if got := tagsTestDecode(t, body).Properties.Tags["owner"]; got != "platform" {
		t.Errorf("management-group tags read back %q, want platform", got)
	}
}

// TestTagsDefaultRefusesScopesThatHoldNoResource pins the 404 a scope naming no
// resource answers with. A scope resolves to exactly one holder of its tags or
// to none at all, so a write against a scope with no holder is refused rather
// than accepted into a plane nothing else reads.
func TestTagsDefaultRefusesScopesThatHoldNoResource(t *testing.T) {
	srv := newTagsTestServer(t)

	const sub = "00000000-0000-0000-0000-000000000001"
	rgPath := "/subscriptions/" + sub + "/resourceGroups/tags-404-rg"
	if status, body := tagsTestServe(t, srv, http.MethodPut, rgPath+tagsTestAPI,
		`{"location":"eastus"}`); status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create resource group: status %d: %s", status, body)
	}

	for _, tc := range []struct {
		name  string
		scope string
		code  string
	}{
		{
			name:  "tracked type, no such resource",
			scope: rgPath + "/providers/Microsoft.KeyVault/vaults/never-created",
			code:  "ResourceNotFound",
		},
		{
			name:  "type the registry does not track",
			scope: rgPath + "/providers/Microsoft.KeyVault/vaults/never-created/secrets/nope",
			code:  "ResourceNotFound",
		},
		{
			name:  "no such resource group",
			scope: "/subscriptions/" + sub + "/resourceGroups/never-created",
			code:  "ResourceGroupNotFound",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				body := ""
				switch method {
				case http.MethodPut:
					body = `{"properties":{"tags":{"a":"b"}}}`
				case http.MethodPatch:
					body = `{"operation":"Merge","properties":{"tags":{"a":"b"}}}`
				}
				status, data := tagsTestServe(t, srv, method,
					tc.scope+"/providers/Microsoft.Resources/tags/default"+tagsTestAPI, body)
				if status != http.StatusNotFound {
					t.Errorf("%s: status %d, want 404: %s", method, status, data)
				}
				if !strings.Contains(string(data), tc.code) {
					t.Errorf("%s: response %s, want error code %s", method, data, tc.code)
				}
			}
		})
	}
}

// TestAzureResourceTypeKeyOfID pins the scope-to-registry-key reading the tags
// surface resolves a resource scope with, including the nested spelling the
// table uses for a child resource type.
func TestAzureResourceTypeKeyOfID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		key  string
		want bool
	}{
		{id: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/v", key: "microsoft.keyvault/vaults", want: true},
		{id: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/w/slots/staging", key: "microsoft.web/sites/slots", want: true},
		{id: "/subscriptions/s/resourceGroups/rg", want: false},
		{id: "/subscriptions/s", want: false},
		// A management group is a provider resource by ID shape; the tags
		// surface routes it to the tags store because the simulator keeps no
		// record for one.
		{id: "/providers/Microsoft.Management/managementGroups/mg", key: "microsoft.management/managementgroups", want: true},
		// A trailing type with no name is not a resource scope.
		{id: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.KeyVault/vaults", want: false},
	} {
		key, ok := azureResourceTypeKeyOfID(tc.id)
		if ok != tc.want {
			t.Errorf("azureResourceTypeKeyOfID(%q) ok = %v, want %v", tc.id, ok, tc.want)
			continue
		}
		if ok && key != tc.key {
			t.Errorf("azureResourceTypeKeyOfID(%q) = %q, want %q", tc.id, key, tc.key)
		}
	}
}
