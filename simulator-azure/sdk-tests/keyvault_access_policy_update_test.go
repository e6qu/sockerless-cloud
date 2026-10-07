package azure_sdk_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kvPolicyViews renders each access policy as `holder: keys=… secrets=…`, for
// comparing a vault's policies regardless of order and permission casing.
func kvPolicyViews(t *testing.T, vaults *armkeyvault.VaultsClient, rg, vault string) []string {
	t.Helper()
	got, err := vaults.Get(ctx, rg, vault, nil)
	require.NoError(t, err)
	var out []string
	for _, p := range got.Properties.AccessPolicies {
		perms := func(list []string) string {
			lowered := make([]string, len(list))
			for i, v := range list {
				lowered[i] = strings.ToLower(v)
			}
			sort.Strings(lowered)
			return strings.Join(lowered, ",")
		}
		var keys, secrets []string
		for _, k := range p.Permissions.Keys {
			keys = append(keys, string(*k))
		}
		for _, s := range p.Permissions.Secrets {
			secrets = append(secrets, string(*s))
		}
		holder := *p.ObjectID
		if p.ApplicationID != nil && *p.ApplicationID != "" {
			holder += "+" + *p.ApplicationID
		}
		out = append(out, holder+": keys="+perms(keys)+" secrets="+perms(secrets))
	}
	sort.Strings(out)
	return out
}

// TestKeyVault_AccessPolicyUpdatesWorkPerPermission covers
// Vaults_UpdateAccessPolicy: add merges permissions into the policy of the
// same tenant, object and application, replace sets that policy's
// permissions, and remove takes permissions away, dropping a policy left with
// none — each leaving every other policy as it was.
func TestKeyVault_AccessPolicyUpdatesWorkPerPermission(t *testing.T) {
	rg := "kv-policy-update-rg"
	vault := uniqueAlnumName("kvpolicyupd")
	alice, bob, carol, app := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	entry := func(object, application string, keys []armkeyvault.KeyPermissions, secrets []armkeyvault.SecretPermissions) *armkeyvault.AccessPolicyEntry {
		e := &armkeyvault.AccessPolicyEntry{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(object),
			Permissions: &armkeyvault.Permissions{Keys: []*armkeyvault.KeyPermissions{}, Secrets: []*armkeyvault.SecretPermissions{}},
		}
		if application != "" {
			e.ApplicationID = to.Ptr(application)
		}
		for i := range keys {
			e.Permissions.Keys = append(e.Permissions.Keys, &keys[i])
		}
		for i := range secrets {
			e.Permissions.Secrets = append(e.Permissions.Secrets, &secrets[i])
		}
		return e
	}
	vaults := createAuthTestVault(t, rg, vault, armkeyvault.VaultProperties{
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{
			entry(alice, "", nil, []armkeyvault.SecretPermissions{armkeyvault.SecretPermissionsGet}),
			entry(bob, "", []armkeyvault.KeyPermissions{armkeyvault.KeyPermissionsGet}, nil),
		},
	})
	update := func(kind armkeyvault.AccessPolicyUpdateKind, entries ...*armkeyvault.AccessPolicyEntry) {
		t.Helper()
		resp, err := vaults.UpdateAccessPolicy(ctx, rg, vault, kind, armkeyvault.VaultAccessPolicyParameters{
			Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: entries},
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, resp.Properties)
	}

	update(armkeyvault.AccessPolicyUpdateKindAdd,
		entry(alice, "", nil, []armkeyvault.SecretPermissions{armkeyvault.SecretPermissionsList, "Get"}),
		entry(alice, app, nil, []armkeyvault.SecretPermissions{armkeyvault.SecretPermissionsGet}))
	expectPolicies(t, vaults, rg, vault, "add merges into the same holder's policy and adds a compound identity's beside it",
		alice+"+"+app+": keys= secrets=get",
		alice+": keys= secrets=get,list",
		bob+": keys=get secrets=",
	)

	update(armkeyvault.AccessPolicyUpdateKindReplace,
		entry(alice, "", []armkeyvault.KeyPermissions{armkeyvault.KeyPermissionsList}, []armkeyvault.SecretPermissions{armkeyvault.SecretPermissionsDelete}))
	expectPolicies(t, vaults, rg, vault, "replace sets the holder's permissions and nobody else's",
		alice+"+"+app+": keys= secrets=get",
		alice+": keys=list secrets=delete",
		bob+": keys=get secrets=",
	)

	update(armkeyvault.AccessPolicyUpdateKindRemove,
		entry(alice, "", nil, []armkeyvault.SecretPermissions{armkeyvault.SecretPermissionsDelete}),
		entry(carol, "", nil, []armkeyvault.SecretPermissions{armkeyvault.SecretPermissionsGet}))
	expectPolicies(t, vaults, rg, vault, "remove takes only the listed permissions, and ignores a holder with no policy",
		alice+"+"+app+": keys= secrets=get",
		alice+": keys=list secrets=",
		bob+": keys=get secrets=",
	)

	update(armkeyvault.AccessPolicyUpdateKindRemove,
		entry(alice, "", []armkeyvault.KeyPermissions{"List"}, nil))
	expectPolicies(t, vaults, rg, vault, "a policy left with no permissions is removed",
		alice+"+"+app+": keys= secrets=get",
		bob+": keys=get secrets=",
	)
}

func expectPolicies(t *testing.T, vaults *armkeyvault.VaultsClient, rg, vault, why string, want ...string) {
	t.Helper()
	sort.Strings(want)
	assert.Equal(t, want, kvPolicyViews(t, vaults, rg, vault), why)
}
