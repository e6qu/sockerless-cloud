package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The managed identity of a virtual machine: the identity the machine
// reports, the tokens the Instance Metadata Service mints for it from inside
// the machine (/metadata/identity/oauth2/token and /metadata/identity/info),
// what those tokens open, and SystemAssignedIdentities_GetByScope on the
// machine:
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{resourceType}/{resourceName}/providers/Microsoft.ManagedIdentity/identities/default

// imdsResponse is one HTTP/1.0 exchange a guest made with the Instance
// Metadata Service: the status code and the JSON body.
type imdsResponse struct {
	status int
	body   map[string]any
}

// imdsFromGuest reads path from the Instance Metadata Service inside the
// machine, with the Metadata header unless bare, the way a shell with no HTTP
// client does.
func imdsFromGuest(t *testing.T, rg, vmName, step, path string, bare bool) imdsResponse {
	t.Helper()
	header := `Metadata: true\r\n`
	if bare {
		header = ""
	}
	// printf reads the request line as its format, so the path's percent
	// escapes are doubled to reach the wire as they are.
	out := runInGuest(t, rg, vmName, step, `bash -c 'host=${AZURE_INSTANCE_METADATA_ENDPOINT#http://}; host=${host%%/*}; `+
		`exec 3<>/dev/tcp/$host/80; printf "GET `+strings.ReplaceAll(path, "%", "%%")+` HTTP/1.0\r\nHost: 169.254.169.254\r\n`+header+`\r\n" >&3; cat <&3'`)
	status := regexp.MustCompile(`HTTP/1\.[01] (\d{3})`).FindStringSubmatch(out)
	require.Len(t, status, 2, "the guest got no HTTP response for %s: %s", path, out)
	bodyText := regexp.MustCompile(`\{.*\}`).FindString(out)
	require.NotEmpty(t, bodyText, "the response to %s carries no JSON body: %s", path, out)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(bodyText), &body), bodyText)
	code := 0
	for _, c := range status[1] {
		code = code*10 + int(c-'0')
	}
	return imdsResponse{status: code, body: body}
}

func (r imdsResponse) token(t *testing.T) string {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, "%v", r.body)
	token, _ := r.body["access_token"].(string)
	require.NotEmpty(t, token)
	return token
}

// requireIdentityNotFound asserts IMDS's refusal of an identity the machine
// does not carry: 400 invalid_request, "Identity not found".
func requireIdentityNotFound(t *testing.T, r imdsResponse) {
	t.Helper()
	assert.Equal(t, http.StatusBadRequest, r.status)
	assert.Equal(t, "invalid_request", r.body["error"])
	assert.Equal(t, "Identity not found", r.body["error_description"])
}

// kvSecretWithBearer reads a secret's current version straight from the
// vault's data plane with the given bearer, as a workload holding a managed
// identity token does.
func kvSecretWithBearer(t *testing.T, vault, secret, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/secrets/"+secret+"?api-version=7.6", nil)
	require.NoError(t, err)
	req.Host = kvDataPlaneHost(vault)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), "%s", raw)
	return resp.StatusCode, body
}

// A machine with a system-assigned and a user-assigned identity reports both,
// and the Instance Metadata Service inside it mints tokens for exactly those:
// the system-assigned identity's without a selector, the user-assigned one's
// by client_id, object_id or msi_res_id, and no other identity's. The token
// is what Key Vault authorizes the machine's identity with.
func TestCompute_VirtualMachineManagedIdentityTokens(t *testing.T) {
	t.Parallel()
	const rg = "vm-identity-rg"
	const vmName = "vm-identity"
	nicID := vmOperationsFixture(t, rg, "vm-identity", "10.107.0.0/16")
	vaults, _, _, vault, uai := acaIdentityVault(t, rg, "vmid")
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	foreign, err := identities.CreateOrUpdate(ctx, rg, "vm-identity-foreign", armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = identities.Delete(ctx, rg, "vm-identity-foreign", nil) })

	vmClient := createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Identity = &armcompute.VirtualMachineIdentity{
			Type:                   to.Ptr(armcompute.ResourceIdentityTypeSystemAssignedUserAssigned),
			UserAssignedIdentities: map[string]*armcompute.UserAssignedIdentitiesValue{*uai.ID: {}},
		}
	})
	vm, err := vmClient.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	require.NotNil(t, vm.Identity, "the machine reports the identity it was created with")
	assert.Equal(t, armcompute.ResourceIdentityTypeSystemAssignedUserAssigned, *vm.Identity.Type)
	systemPrincipal := derefString(vm.Identity.PrincipalID)
	require.NotEmpty(t, systemPrincipal, "a system-assigned identity has a principal")
	assert.Equal(t, simTenantID, derefString(vm.Identity.TenantID))
	attached, ok := vm.Identity.UserAssignedIdentities[*uai.ID]
	require.True(t, ok, "the machine carries the user-assigned identity: %v", vm.Identity.UserAssignedIdentities)
	assert.Equal(t, derefString(uai.Properties.ClientID), derefString(attached.ClientID))
	assert.Equal(t, derefString(uai.Properties.PrincipalID), derefString(attached.PrincipalID))

	// SystemAssignedIdentities_GetByScope on the machine answers its
	// system-assigned identity.
	saClient, err := armmsi.NewSystemAssignedIdentitiesClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	byScope, err := saClient.GetByScope(ctx, strings.TrimPrefix(*vm.ID, "/"), nil)
	require.NoError(t, err)
	assert.Equal(t, systemPrincipal, derefString(byScope.Properties.PrincipalID))
	assert.NotEmpty(t, derefString(byScope.Properties.ClientID))

	const tokenPath = "/metadata/identity/oauth2/token?api-version=2018-02-01&resource=https%3A%2F%2Fvault.azure.net"
	system := imdsFromGuest(t, rg, vmName, "token-system", tokenPath, false)
	systemToken := system.token(t)
	claims := decodeJWTClaims(t, systemToken)
	assert.Equal(t, systemPrincipal, claims["oid"], "a token without a selector is the system-assigned identity's")
	assert.Equal(t, "https://vault.azure.net", claims["aud"])
	assert.Equal(t, simTenantID, claims["tid"])
	assert.Equal(t, derefString(byScope.Properties.ClientID), system.body["client_id"])
	assert.Equal(t, "Bearer", system.body["token_type"])
	assert.Equal(t, "https://vault.azure.net", system.body["resource"])
	for _, member := range []string{"expires_in", "expires_on", "ext_expires_in", "not_before"} {
		assert.NotEmpty(t, system.body[member], member)
	}
	_, hasRefresh := system.body["refresh_token"]
	assert.False(t, hasRefresh, "IdentityTokenResponse has no refresh_token member")

	user := imdsFromGuest(t, rg, vmName, "token-user", tokenPath+"&client_id="+derefString(uai.Properties.ClientID), false)
	assert.Equal(t, derefString(uai.Properties.PrincipalID), decodeJWTClaims(t, user.token(t))["oid"],
		"client_id selects the user-assigned identity")
	assert.Equal(t, derefString(uai.Properties.ClientID), user.body["client_id"])
	byObject := imdsFromGuest(t, rg, vmName, "token-object", tokenPath+"&object_id="+derefString(uai.Properties.PrincipalID), false)
	assert.Equal(t, derefString(uai.Properties.PrincipalID), decodeJWTClaims(t, byObject.token(t))["oid"])
	assert.Equal(t, derefString(uai.Properties.PrincipalID), byObject.body["object_id"])
	byResource := imdsFromGuest(t, rg, vmName, "token-resid", tokenPath+"&msi_res_id="+strings.ReplaceAll(*uai.ID, "/", "%2F"), false)
	assert.Equal(t, derefString(uai.Properties.PrincipalID), decodeJWTClaims(t, byResource.token(t))["oid"])
	assert.Equal(t, *uai.ID, byResource.body["msi_res_id"])

	// An identity the machine does not carry is refused, and so is a request
	// without the Metadata header.
	requireIdentityNotFound(t, imdsFromGuest(t, rg, vmName, "token-foreign",
		tokenPath+"&client_id="+derefString(foreign.Properties.ClientID), false))
	bare := imdsFromGuest(t, rg, vmName, "token-bare", tokenPath, true)
	assert.Equal(t, http.StatusBadRequest, bare.status)
	assert.Equal(t, "bad_request_102", bare.body["error"])
	assert.Equal(t, "Required metadata header not specified", bare.body["error_description"])

	info := imdsFromGuest(t, rg, vmName, "identity-info", "/metadata/identity/info?api-version=2021-02-01", false)
	require.Equal(t, http.StatusOK, info.status, "%v", info.body)
	assert.Equal(t, simTenantID, info.body["tenantId"])

	// The token opens what the identity is authorized for: a vault that grants
	// the machine's identity secret reads answers it, and refuses the
	// identity's token before the grant.
	status, body := kvSecretWithBearer(t, vault, "db-password", systemToken)
	assert.Equal(t, http.StatusForbidden, status, "%v", body)
	grantSecretGet(t, vaults, rg, vault, systemPrincipal)
	status, body = kvSecretWithBearer(t, vault, "db-password", systemToken)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, "hunter2", body["value"])

	// Removing the identity removes what the metadata service speaks for.
	_, err = vmClient.BeginUpdate(ctx, rg, vmName, armcompute.VirtualMachineUpdate{
		Identity: &armcompute.VirtualMachineIdentity{Type: to.Ptr(armcompute.ResourceIdentityTypeNone)},
	}, nil)
	require.NoError(t, err)
	vm, err = vmClient.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	assert.Nil(t, vm.Identity, "type None removes the identity")
	requireIdentityNotFound(t, imdsFromGuest(t, rg, vmName, "token-none", tokenPath, false))
	_, err = saClient.GetByScope(ctx, strings.TrimPrefix(*vm.ID, "/"), nil)
	require.Error(t, err, "a scope with no system-assigned identity has no identities/default")
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
