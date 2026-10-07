package azure_cli_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// CLI coverage for Vaults_UpdateAccessPolicy, which no az command wraps:
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/vaults/{vaultName}/accessPolicies/{operationKind}
//
// add merges permissions into the policy of the same tenant, object and
// application, replace sets that policy's permissions, and remove takes
// permissions away, dropping a policy left with none.
func TestKeyVault_CLI_AccessPolicyUpdatesWorkPerPermission(t *testing.T) {
	const (
		vault = "cli-kv-policy-update"
		alice = "a1a1a1a1-0000-0000-0000-000000000001"
		bob   = "b0b0b0b0-0000-0000-0000-000000000002"
	)
	vaultURL := armURL("Microsoft.KeyVault", "vaults/"+vault, "2024-11-01")
	runCLI(t, azRest("PUT", vaultURL, fmt.Sprintf(`{"location":"eastus","properties":{"tenantId":%q,"sku":{"family":"A","name":"standard"},
		"accessPolicies":[
			{"tenantId":%q,"objectId":%q,"permissions":{"secrets":["get"]}},
			{"tenantId":%q,"objectId":%q,"permissions":{"keys":["get"]}}
		]}}`, simTenantID, simTenantID, alice, simTenantID, bob)))
	t.Cleanup(func() { _ = azRest("DELETE", vaultURL, "").Run() })

	update := func(kind, policies string) {
		t.Helper()
		runCLI(t, azRest("PUT", armURL("Microsoft.KeyVault", "vaults/"+vault+"/accessPolicies/"+kind, "2024-11-01"),
			`{"properties":{"accessPolicies":[`+policies+`]}}`))
	}
	entry := func(object, perms string) string {
		return fmt.Sprintf(`{"tenantId":%q,"objectId":%q,"permissions":%s}`, simTenantID, object, perms)
	}
	policies := func() []string {
		t.Helper()
		var got struct {
			Properties struct {
				AccessPolicies []struct {
					ObjectID    string `json:"objectId"`
					Permissions struct {
						Keys    []string `json:"keys"`
						Secrets []string `json:"secrets"`
					} `json:"permissions"`
				} `json:"accessPolicies"`
			} `json:"properties"`
		}
		parseJSON(t, runCLI(t, azRest("GET", vaultURL, "")), &got)
		var out []string
		for _, p := range got.Properties.AccessPolicies {
			keys := append([]string(nil), p.Permissions.Keys...)
			secrets := append([]string(nil), p.Permissions.Secrets...)
			sort.Strings(keys)
			sort.Strings(secrets)
			out = append(out, p.ObjectID+": keys="+strings.Join(keys, ",")+" secrets="+strings.Join(secrets, ","))
		}
		sort.Strings(out)
		return out
	}

	update("add", entry(alice, `{"secrets":["list","get"]}`))
	assert.Equal(t, []string{alice + ": keys= secrets=get,list", bob + ": keys=get secrets="}, policies(),
		"add merges into the same holder's policy")

	update("replace", entry(alice, `{"keys":["list"],"secrets":["delete"]}`))
	assert.Equal(t, []string{alice + ": keys=list secrets=delete", bob + ": keys=get secrets="}, policies(),
		"replace sets the holder's permissions and nobody else's")

	update("remove", entry(alice, `{"secrets":["delete"]}`))
	assert.Equal(t, []string{alice + ": keys=list secrets=", bob + ": keys=get secrets="}, policies(),
		"remove takes only the listed permissions")

	update("remove", entry(alice, `{"keys":["list"]}`))
	assert.Equal(t, []string{bob + ": keys=get secrets="}, policies(), "a policy left with no permissions is removed")
}
