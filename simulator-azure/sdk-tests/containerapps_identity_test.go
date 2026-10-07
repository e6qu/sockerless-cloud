package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the managed identities of container apps and Container Apps
// jobs: the identity the resource reports, the IDENTITY_ENDPOINT its workload
// acquires the identities' tokens from, and secrets that reference Key Vault,
// which Container Apps reads as the identity each names and hands the
// workload through secretRef environment variables.

// acaIdentityWorkloadScript prints the secrets the workload sees, then the
// tokens its identity endpoint issues the system-assigned identity and the
// user-assigned identity $UAI_CLIENT_ID names, then "done".
const acaIdentityWorkloadScript = `echo "db=$DB pinned=$PINNED plain=$PLAIN"
token() { wget -qO- --header "X-IDENTITY-HEADER: $IDENTITY_HEADER" "$IDENTITY_ENDPOINT?api-version=2019-08-01&resource=https://vault.azure.net$1"; echo; }
token ""
token "&client_id=$UAI_CLIENT_ID"
echo done`

// acaKeepServing keeps an app's replica running after its script, and ends it
// as soon as the platform stops the replica: sh as PID 1 ignores SIGTERM
// unless it traps it.
const acaKeepServing = `trap 'exit 0' TERM
sleep 3600 &
wait`

// acaIdentityVault creates a vault holding db-password in two versions and a
// user-assigned identity, and returns the vault client, the secret's URL, the
// first version's URL and the identity.
func acaIdentityVault(t *testing.T, rg, prefix string) (*armkeyvault.VaultsClient, string, string, string, armmsi.Identity) {
	t.Helper()
	vault := uniqueAlnumName(prefix)
	vaults := createAuthTestVault(t, rg, vault, armkeyvault.VaultProperties{
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(simCallerObjectID),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsSet)}},
		}},
	})
	secrets := kvSecretsClient(t, vault)
	first, err := secrets.SetSecret(ctx, "db-password", azsecrets.SetSecretParameters{Value: to.Ptr("first-password")}, nil)
	require.NoError(t, err)
	_, err = secrets.SetSecret(ctx, "db-password", azsecrets.SetSecretParameters{Value: to.Ptr("hunter2")}, nil)
	require.NoError(t, err)

	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := uniqueName(prefix + "-uai")
	created, err := identities.CreateOrUpdate(ctx, rg, name, armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = identities.Delete(ctx, rg, name, nil) })
	return vaults, kvVaultURL(vault) + "/secrets/db-password", string(*first.ID), vault, created.Identity
}

func grantSecretGet(t *testing.T, vaults *armkeyvault.VaultsClient, rg, vault, principal string) {
	t.Helper()
	_, err := vaults.UpdateAccessPolicy(ctx, rg, vault, armkeyvault.AccessPolicyUpdateKindAdd, armkeyvault.VaultAccessPolicyParameters{
		Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(principal),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsGet)}},
		}}},
	}, nil)
	require.NoError(t, err)
}

func requireSecretInvalid(t *testing.T, err error, identity string) {
	t.Helper()
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, 400, respErr.StatusCode)
	assert.Contains(t, err.Error(), "Unable to get value using Managed identity "+identity)
}

func TestSDK_ContainerAppsApps_ManagedIdentityAndKeyVaultSecrets(t *testing.T) {
	rg := "sdk-aca-identity-rg"
	ensureRG(t, rg)
	vaults, secretURL, firstVersionURL, vault, uai := acaIdentityVault(t, rg, "acaappid")
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.App/managedEnvironments/sim-env"
	name := uniqueName("sdk-aca-id")

	app := func(identity *armappcontainers.ManagedServiceIdentity, secrets []*armappcontainers.Secret, env []*armappcontainers.EnvironmentVar) armappcontainers.ContainerApp {
		return armappcontainers.ContainerApp{
			Location: to.Ptr("eastus"),
			Identity: identity,
			Properties: &armappcontainers.ContainerAppProperties{
				EnvironmentID: to.Ptr(envID),
				Configuration: &armappcontainers.Configuration{Secrets: secrets},
				Template: &armappcontainers.Template{
					Containers: []*armappcontainers.Container{{
						Name:    to.Ptr("main"),
						Image:   to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
						Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(acaIdentityWorkloadScript + "\n" + acaKeepServing)},
						Env:     env,
					}},
					Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
				},
			},
		}
	}
	put := func(body armappcontainers.ContainerApp) (armappcontainers.ContainerApp, error) {
		poller, err := client.BeginCreateOrUpdate(ctx, rg, name, body, nil)
		if err != nil {
			return armappcontainers.ContainerApp{}, err
		}
		done, err := poller.PollUntilDone(ctx, nil)
		return done.ContainerApp, err
	}
	t.Cleanup(func() {
		if poller, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = poller.PollUntilDone(ctx, nil)
		}
	})
	both := &armappcontainers.ManagedServiceIdentity{
		Type:                   to.Ptr(armappcontainers.ManagedServiceIdentityTypeSystemAssignedUserAssigned),
		UserAssignedIdentities: map[string]*armappcontainers.UserAssignedIdentity{*uai.ID: {}},
	}
	plainEnv := []*armappcontainers.EnvironmentVar{
		{Name: to.Ptr("PLAIN"), SecretRef: to.Ptr("plain")},
		{Name: to.Ptr("UAI_CLIENT_ID"), Value: uai.Properties.ClientID},
	}
	plain := []*armappcontainers.Secret{{Name: to.Ptr("plain"), Value: to.Ptr("plain-value")}}

	created, err := put(app(both, plain, plainEnv))
	require.NoError(t, err)
	require.NotNil(t, created.Identity)
	assert.Equal(t, armappcontainers.ManagedServiceIdentityTypeSystemAssignedUserAssigned, *created.Identity.Type)
	assert.Equal(t, simTenantID, ptrVal(created.Identity.TenantID))
	systemPrincipal := ptrVal(created.Identity.PrincipalID)
	require.NotEmpty(t, systemPrincipal)
	attached := created.Identity.UserAssignedIdentities[*uai.ID]
	require.NotNil(t, attached)
	assert.Equal(t, *uai.Properties.PrincipalID, ptrVal(attached.PrincipalID))
	assert.Equal(t, *uai.Properties.ClientID, ptrVal(attached.ClientID))

	// A secret read as an identity the vault does not grant fails the update,
	// and so does one read as an identity the app does not have.
	kvSecrets := []*armappcontainers.Secret{
		plain[0],
		{Name: to.Ptr("db"), KeyVaultURL: to.Ptr(secretURL), Identity: uai.ID},
		{Name: to.Ptr("pinned"), KeyVaultURL: to.Ptr(firstVersionURL), Identity: to.Ptr("system")},
	}
	kvEnv := append([]*armappcontainers.EnvironmentVar{
		{Name: to.Ptr("DB"), SecretRef: to.Ptr("db")},
		{Name: to.Ptr("PINNED"), SecretRef: to.Ptr("pinned")},
	}, plainEnv...)
	_, err = put(app(both, kvSecrets, kvEnv))
	requireSecretInvalid(t, err, *uai.ID)
	_, err = put(app(&armappcontainers.ManagedServiceIdentity{Type: to.Ptr(armappcontainers.ManagedServiceIdentityTypeSystemAssigned)}, kvSecrets, kvEnv))
	requireSecretInvalid(t, err, *uai.ID)

	grantSecretGet(t, vaults, rg, vault, *uai.Properties.PrincipalID)
	_, err = put(app(both, kvSecrets, kvEnv))
	requireSecretInvalid(t, err, "system")
	grantSecretGet(t, vaults, rg, vault, systemPrincipal)
	updated, err := put(app(both, kvSecrets, kvEnv))
	require.NoError(t, err)
	assert.Equal(t, systemPrincipal, ptrVal(updated.Identity.PrincipalID), "the system-assigned identity keeps its principal")

	listed, err := client.ListSecrets(ctx, rg, name, nil)
	require.NoError(t, err)
	byName := map[string]*armappcontainers.ContainerAppSecret{}
	for _, s := range listed.Value {
		byName[ptrVal(s.Name)] = s
	}
	require.Contains(t, byName, "db")
	assert.Equal(t, secretURL, ptrVal(byName["db"].KeyVaultURL))
	assert.Equal(t, *uai.ID, ptrVal(byName["db"].Identity))

	lines := readContainerAppConsole(t, client, rg, name, ptrVal(updated.Properties.LatestRevisionName), "main",
		func(line string) bool { return line == "done" })
	assert.Contains(t, lines, "db=hunter2 pinned=first-password plain=plain-value",
		"the workload sees each Key Vault secret's value, at the version its URL pins")
	var tokens []map[string]any
	for _, line := range lines {
		if strings.HasPrefix(line, "{") {
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &body), line)
			tokens = append(tokens, body)
		}
	}
	require.Len(t, tokens, 2, "%q", lines)
	system := decodeJWTClaims(t, tokens[0]["access_token"].(string))
	assert.Equal(t, systemPrincipal, system["oid"], "the endpoint issues the app's system-assigned identity")
	assert.Equal(t, *updated.ID, system["xms_mirid"])
	user := decodeJWTClaims(t, tokens[1]["access_token"].(string))
	assert.Equal(t, *uai.Properties.PrincipalID, user["oid"], "and the user-assigned identity client_id names")
	assert.Equal(t, *uai.Properties.ClientID, tokens[1]["client_id"])

	removed, err := put(app(&armappcontainers.ManagedServiceIdentity{Type: to.Ptr(armappcontainers.ManagedServiceIdentityTypeNone)}, plain, plainEnv))
	require.NoError(t, err)
	assert.Equal(t, armappcontainers.ManagedServiceIdentityTypeNone, *removed.Identity.Type)
	assert.Nil(t, removed.Identity.PrincipalID)
}

func TestSDK_ContainerAppsJobs_ManagedIdentityAndKeyVaultSecrets(t *testing.T) {
	rg := "sdk-aca-job-identity-rg"
	ensureRG(t, rg)
	vaults, secretURL, _, vault, uai := acaIdentityVault(t, rg, "acajobid")
	jobs, err := armappcontainers.NewJobsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := uniqueName("sdk-job-id")
	job := armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Identity: &armappcontainers.ManagedServiceIdentity{
			Type:                   to.Ptr(armappcontainers.ManagedServiceIdentityTypeUserAssigned),
			UserAssignedIdentities: map[string]*armappcontainers.UserAssignedIdentity{*uai.ID: {}},
		},
		Properties: &armappcontainers.JobProperties{
			EnvironmentID: to.Ptr(acaLogsEnvironmentID(t)),
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout: to.Ptr[int32](60),
				Secrets: []*armappcontainers.Secret{
					{Name: to.Ptr("db"), KeyVaultURL: to.Ptr(secretURL), Identity: uai.ID},
					{Name: to.Ptr("plain"), Value: to.Ptr("plain-value")},
				},
			},
			Template: &armappcontainers.JobTemplate{Containers: []*armappcontainers.Container{{
				Name:    to.Ptr("worker"),
				Image:   to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
				Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(acaIdentityWorkloadScript)},
				Env: []*armappcontainers.EnvironmentVar{
					{Name: to.Ptr("DB"), SecretRef: to.Ptr("db")},
					{Name: to.Ptr("PLAIN"), SecretRef: to.Ptr("plain")},
					{Name: to.Ptr("UAI_CLIENT_ID"), Value: uai.Properties.ClientID},
				},
			}}},
		},
	}
	put := func() (armappcontainers.Job, error) {
		poller, err := jobs.BeginCreateOrUpdate(ctx, rg, name, job, nil)
		if err != nil {
			return armappcontainers.Job{}, err
		}
		done, err := poller.PollUntilDone(ctx, nil)
		return done.Job, err
	}
	t.Cleanup(func() {
		if poller, err := jobs.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = poller.PollUntilDone(ctx, nil)
		}
	})

	_, err = put()
	requireSecretInvalid(t, err, *uai.ID)
	grantSecretGet(t, vaults, rg, vault, *uai.Properties.PrincipalID)
	created, err := put()
	require.NoError(t, err)
	require.NotNil(t, created.Identity)
	assert.Equal(t, armappcontainers.ManagedServiceIdentityTypeUserAssigned, *created.Identity.Type)
	assert.Equal(t, *uai.Properties.PrincipalID, ptrVal(created.Identity.UserAssignedIdentities[*uai.ID].PrincipalID))

	execName := acaStartExecution(t, rg, name)
	exec := acaWaitExecution(t, rg, name, execName)
	assert.Equal(t, "Succeeded", exec["properties"].(map[string]any)["status"])

	logs := acaJobConsoleLines(t, name, "done")
	assert.Contains(t, logs, "db=hunter2 pinned= plain=plain-value", "the job sees the Key Vault secret's value")
	var tokens []map[string]any
	for _, line := range logs {
		var body map[string]any
		if json.Unmarshal([]byte(line), &body) == nil {
			tokens = append(tokens, body)
		}
	}
	require.Len(t, tokens, 1, "a job with only a user-assigned identity gets no system-assigned token: %q", logs)
	user := decodeJWTClaims(t, tokens[0]["access_token"].(string))
	assert.Equal(t, *uai.Properties.PrincipalID, user["oid"])
	assert.Equal(t, *uai.ID, user["xms_mirid"])
}

// acaJobConsoleLines reads the job's console lines from its environment's Log
// Analytics workspace once the line want has been ingested.
func acaJobConsoleLines(t *testing.T, job, want string) []string {
	t.Helper()
	var lines []string
	require.Eventually(t, func() bool {
		result := queryWorkspace(t, acaLogsCustomerID(t), `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "`+job+`"`)
		if len(result.Tables) != 1 {
			return false
		}
		logIdx := -1
		for i, col := range result.Tables[0].Columns {
			if col.Name == "Log_s" {
				logIdx = i
			}
		}
		if logIdx < 0 {
			return false
		}
		lines = lines[:0]
		found := false
		for _, row := range result.Tables[0].Rows {
			if s, ok := row[logIdx].(string); ok {
				lines = append(lines, s)
				found = found || s == want
			}
		}
		return found
	}, 60*time.Second, 200*time.Millisecond)
	return lines
}

// TestSDK_ContainerAppsApps_IngressAbortsAResponseItsReplicaCutsShort covers a
// replica that closes its connection in the middle of a chunked body: the
// ingress has already relayed the status and headers, so it ends the client's
// response there rather than appending an error document to the body.
func TestSDK_ContainerAppsApps_IngressAbortsAResponseItsReplicaCutsShort(t *testing.T) {
	rg, name := "sdk-aca-ingress-abort-rg", uniqueName("ingressabort")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	script := `nc -lk -p 8080 -e sh -c 'while read -r l && [ ${#l} -gt 1 ]; do :; done; printf "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\npartial\r\n"'`
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.App/managedEnvironments/sim-env"),
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{
				External: to.Ptr(true), TargetPort: to.Ptr[int32](8080),
			}},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{
					Name:    to.Ptr("main"),
					Image:   to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
					Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(script)},
				}},
				Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Host = ptrVal(created.Properties.Configuration.Ingress.Fqdn)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the replica's status reached the client")
	body, err := io.ReadAll(resp.Body)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the response ends where the replica stopped")
	assert.Equal(t, "partial", string(body), "nothing follows what the replica sent")
}
