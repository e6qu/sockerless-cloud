package azure_tf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformEntraApplyDestroy drives Microsoft Entra ID through the
// official `hashicorp/azuread` provider — a different provider from the
// `azurerm` one every other configuration here uses, reaching a different
// service (Microsoft Graph rather than Azure Resource Manager) over a
// different base URI.
//
// The provider is pointed at the simulator by exactly one coordinate: its
// `metadata_host`. From `https://<metadata_host>/metadata/endpoints` it
// resolves the whole cloud environment, takes the Microsoft Graph base URI out
// of `microsoftGraphResourceId` and the token endpoint out of
// `loginEndpoint`, and then speaks unmodified Microsoft Graph. Because the
// provider hardcodes the `https://` scheme when it resolves that document
// (internal/provider/provider.go: `environments.FromEndpoint(ctx,
// fmt.Sprintf("https://%s", metadataHost))`), the configuration only works
// behind this harness's HTTPS gateway.
//
// The round trip exercises the Graph surface in the shape the provider drives
// it, across both Graph versions:
//
//   - applications: create with an `owners@odata.bind`, the display-name
//     replication patches, the v1.0 read, the beta read for
//     `oauth2RequirePostResponse`, the owner reference collection, addPassword
//     and removePassword, and delete-then-poll-for-404.
//   - service principals: the `appId eq` list the provider uses to detect an
//     existing principal, create, patch, the beta read for `samlMetadataUrl`,
//     owners, and delete.
//   - users: create, the beta patch and beta read for `showInAddressList`, the
//     `$select` read of the extended property set, and the manager navigation
//     property.
//   - groups: the whole family through the beta endpoint — create, patch,
//     read, owners resolved through `GET /v1.0/directoryObjects/{id}`, the
//     member reference collection, and memberOf.
//
// It runs against the standalone entra/ configuration as its own CI shard
// (`tf (azure entra)`): every create and delete sits through the provider's
// consistency polling, which does not fit the shared stack's budget.
func TestTerraformEntraApplyDestroy(t *testing.T) {
	dir := tfEntraWorkspace(t)
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	// Idempotency: a second plan must show no drift. -detailed-exitcode makes
	// terraform exit 2 (non-zero) on any drift, which runTimed surfaces as an
	// error — so a clean plan (exit 0) is the only pass.
	out, err = runTimed(t, "terraform plan", terraformCmd(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "terraform plan showed drift after apply (not idempotent):\n%s", out)

	outputs := readOutputs(t, dir)

	// The provider derives the calling principal from the `oid` claim on the
	// token the simulator's client_credentials grant mints, and that claim has
	// to name the bootstrap service principal — every owner reference the
	// provider writes is built from it.
	callerObjectID := outputs.must(t, "azuread_client_config_object_id")
	require.Equal(t, "00000000-0000-0000-0000-0000000000b2", callerObjectID,
		"azuread_client_config must report the bootstrap service principal's object ID; got %s", callerObjectID)
	require.Equal(t, "test-client-id", outputs.must(t, "azuread_client_config_client_id"))
	require.Equal(t, "11111111-1111-1111-1111-111111111111", outputs.must(t, "azuread_client_config_tenant_id"))

	// The application registration is addressed by its object ID, and its
	// resource ID is Graph's `/applications/{id}` path.
	appObjectID := outputs.must(t, "azuread_application_object_id")
	require.NotEmpty(t, appObjectID)
	require.Equal(t, "/applications/"+appObjectID, outputs.must(t, "azuread_application_id"),
		"azuread_application id must be the Graph application path")

	// The service principal materializes the application, so it carries the
	// application's own appId rather than an identifier of its own.
	appClientID := outputs.must(t, "azuread_application_client_id")
	require.NotEmpty(t, appClientID)
	require.Equal(t, appClientID, outputs.must(t, "azuread_service_principal_client_id"),
		"the service principal must report the backing application's client ID")
	require.NotEmpty(t, outputs.must(t, "azuread_service_principal_object_id"))
	require.NotEqual(t, appObjectID, outputs.must(t, "azuread_service_principal_object_id"),
		"the service principal is its own directory object, with its own object ID")

	// addPassword minted a credential on the application registration.
	require.NotEmpty(t, outputs.must(t, "azuread_application_password_key_id"))

	// The manager navigation property round-trips: the member's manager_id is
	// read back from GET /users/{id}/manager as the manager's object ID.
	managerObjectID := outputs.must(t, "azuread_user_manager_object_id")
	require.NotEmpty(t, managerObjectID)
	require.Equal(t, managerObjectID, outputs.must(t, "azuread_user_member_manager_id"),
		"the user's manager must read back as the manager's object ID")

	// The group membership resource IDs the member reference it created.
	groupObjectID := outputs.must(t, "azuread_group_object_id")
	memberObjectID := outputs.must(t, "azuread_user_object_id")
	require.NotEmpty(t, groupObjectID)
	require.Equal(t, groupObjectID+"/member/"+memberObjectID, outputs.must(t, "azuread_group_member_id"),
		"azuread_group_member id must name the group and the member it bound")

	out, err = runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
	require.True(t, strings.Contains(string(out), "Destroy complete!"),
		"terraform destroy did not report completion:\n%s", out)
}

// tfEntraWorkspace copies the standalone Microsoft Entra ID configuration into
// a working directory private to this run.
func tfEntraWorkspace(t *testing.T) string {
	t.Helper()
	return tfWorkspaceFrom(t, tfEntraDir())
}
