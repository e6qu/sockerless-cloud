package azure_sdk_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/apimanagement/armapimanagement"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIMSubscription_RegenerateKeyRotation drives the API Management
// subscription key rotation contract through the official armapimanagement
// client: regeneratePrimaryKey / regenerateSecondaryKey each rotate only their
// own slot, listSecrets observes the rotation, and delete → recreate under the
// same name resets both keys.
func TestAPIMSubscription_RegenerateKeyRotation(t *testing.T) {
	rg := "apim-rotation-rg"
	service := "apimrotationsvc"
	sid := "apim-rotation-sub"
	ensureRG(t, rg)

	// The service itself is provisioned through the ARM wire path the sim
	// serves for Microsoft.ApiManagement/service.
	svcPath := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.ApiManagement/service/" + service
	resp := armReq(t, "PUT", svcPath,
		`{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"ops@example.com","publisherName":"Ops"}}`)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	client, err := armapimanagement.NewSubscriptionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createSubscription := func() {
		_, err := client.CreateOrUpdate(ctx, rg, service, sid, armapimanagement.SubscriptionCreateParameters{
			Properties: &armapimanagement.SubscriptionCreateParameterProperties{
				DisplayName: to.Ptr("Rotation Subscription"),
				Scope:       to.Ptr("/apis"),
			},
		}, nil)
		require.NoError(t, err)
	}
	createSubscription()

	initial, err := client.ListSecrets(ctx, rg, service, sid, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.PrimaryKey)
	require.NotNil(t, initial.SecondaryKey)
	primaryBefore, secondaryBefore := *initial.PrimaryKey, *initial.SecondaryKey
	require.NotEqual(t, primaryBefore, secondaryBefore, "primary and secondary keys must be distinct")

	// regeneratePrimaryKey answers 204 and rotates only the primary slot.
	_, err = client.RegeneratePrimaryKey(ctx, rg, service, sid, nil)
	require.NoError(t, err)

	afterPrimary, err := client.ListSecrets(ctx, rg, service, sid, nil)
	require.NoError(t, err)
	assert.NotEqual(t, primaryBefore, *afterPrimary.PrimaryKey, "listSecrets must return the rotated primary key")
	assert.Equal(t, secondaryBefore, *afterPrimary.SecondaryKey, "regenerating the primary key must not change the secondary key")

	// regenerateSecondaryKey rotates only the secondary slot.
	_, err = client.RegenerateSecondaryKey(ctx, rg, service, sid, nil)
	require.NoError(t, err)

	afterSecondary, err := client.ListSecrets(ctx, rg, service, sid, nil)
	require.NoError(t, err)
	assert.Equal(t, *afterPrimary.PrimaryKey, *afterSecondary.PrimaryKey, "regenerating the secondary key must not change the primary key")
	assert.NotEqual(t, secondaryBefore, *afterSecondary.SecondaryKey, "listSecrets must return the rotated secondary key")

	// A second regenerate of the same slot yields yet another value.
	_, err = client.RegeneratePrimaryKey(ctx, rg, service, sid, nil)
	require.NoError(t, err)
	again, err := client.ListSecrets(ctx, rg, service, sid, nil)
	require.NoError(t, err)
	assert.NotEqual(t, *afterSecondary.PrimaryKey, *again.PrimaryKey,
		"a second regenerate of the primary key must yield yet another value")

	// Delete + recreate under the same name starts from fresh key material.
	_, err = client.Delete(ctx, rg, service, sid, "*", nil)
	require.NoError(t, err)
	createSubscription()
	fresh, err := client.ListSecrets(ctx, rg, service, sid, nil)
	require.NoError(t, err)
	assert.Equal(t, primaryBefore, *fresh.PrimaryKey, "a recreated subscription must start from the unrotated primary key")
	assert.Equal(t, secondaryBefore, *fresh.SecondaryKey, "a recreated subscription must start from the unrotated secondary key")
}

// TestACR_RegenerateCredentialRotation drives the Azure Container Registry
// admin-credential rotation contract through the official
// armcontainerregistry client: regenerateCredential rotates only the named
// password slot, listCredentials observes the rotation, and delete → recreate
// resets both passwords.
func TestACR_RegenerateCredentialRotation(t *testing.T) {
	rg := "acr-rotation-rg"
	registry := "acrrotationreg"
	ensureRG(t, rg)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createRegistry := func() {
		poller, err := client.BeginCreate(ctx, rg, registry, armcontainerregistry.Registry{
			Location: to.Ptr("eastus"),
			SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameStandard)},
			Properties: &armcontainerregistry.RegistryProperties{
				AdminUserEnabled: to.Ptr(true),
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}
	createRegistry()

	passwordValues := func(creds []*armcontainerregistry.RegistryPassword) (password, password2 string) {
		for _, pw := range creds {
			require.NotNil(t, pw.Name)
			require.NotNil(t, pw.Value)
			switch *pw.Name {
			case armcontainerregistry.PasswordNamePassword:
				password = *pw.Value
			case armcontainerregistry.PasswordNamePassword2:
				password2 = *pw.Value
			}
		}
		require.NotEmpty(t, password, "listCredentials must include the password slot")
		require.NotEmpty(t, password2, "listCredentials must include the password2 slot")
		return password, password2
	}

	initial, err := client.ListCredentials(ctx, rg, registry, nil)
	require.NoError(t, err)
	passwordBefore, password2Before := passwordValues(initial.Passwords)
	require.NotEqual(t, passwordBefore, password2Before, "the two admin passwords must be distinct")

	regen, err := client.RegenerateCredential(ctx, rg, registry, armcontainerregistry.RegenerateCredentialParameters{
		Name: to.Ptr(armcontainerregistry.PasswordNamePassword),
	}, nil)
	require.NoError(t, err)
	passwordRotated, password2Rotated := passwordValues(regen.Passwords)
	assert.NotEqual(t, passwordBefore, passwordRotated, "regenerating 'password' must change that slot")
	assert.Equal(t, password2Before, password2Rotated, "regenerating 'password' must not change 'password2'")

	listed, err := client.ListCredentials(ctx, rg, registry, nil)
	require.NoError(t, err)
	passwordListed, password2Listed := passwordValues(listed.Passwords)
	assert.Equal(t, passwordRotated, passwordListed, "listCredentials must return the rotated password")
	assert.Equal(t, password2Before, password2Listed, "listCredentials must keep the untouched password2")

	// Rotating the other slot leaves the first alone.
	regen2, err := client.RegenerateCredential(ctx, rg, registry, armcontainerregistry.RegenerateCredentialParameters{
		Name: to.Ptr(armcontainerregistry.PasswordNamePassword2),
	}, nil)
	require.NoError(t, err)
	password2ndPass, password2Rotated2 := passwordValues(regen2.Passwords)
	assert.Equal(t, passwordRotated, password2ndPass, "regenerating 'password2' must not change 'password'")
	assert.NotEqual(t, password2Before, password2Rotated2, "regenerating 'password2' must change that slot")

	// Delete + recreate under the same name starts from fresh passwords.
	delPoller, err := client.BeginDelete(ctx, rg, registry, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	createRegistry()
	fresh, err := client.ListCredentials(ctx, rg, registry, nil)
	require.NoError(t, err)
	passwordFresh, password2Fresh := passwordValues(fresh.Passwords)
	assert.Equal(t, passwordBefore, passwordFresh, "a recreated registry must start from the unrotated password")
	assert.Equal(t, password2Before, password2Fresh, "a recreated registry must start from the unrotated password2")
}

// logicCallbackSig extracts the `sig` query value from a Logic Apps callback
// URL — the part real Logic Apps derives from the workflow's access key.
func logicCallbackSig(t *testing.T, callbackURL string) string {
	t.Helper()
	parsed, err := url.Parse(callbackURL)
	require.NoError(t, err, "callback URL must parse: %s", callbackURL)
	sig := parsed.Query().Get("sig")
	require.NotEmpty(t, sig, "a Logic Apps callback URL must carry a signature: %s", callbackURL)
	return sig
}

// TestLogicApps_RegenerateAccessKeyRotation drives the Logic Apps access-key
// rotation contract through the official armlogic client. Callback URLs are
// signed with the workflow's access key, so regenerateAccessKey changes the
// signature listCallbackUrl returns — the observable effect of real Logic
// Apps invalidating outstanding callback URLs. The integration account
// behaves the same way, and delete → recreate resets the signature.
func TestLogicApps_RegenerateAccessKeyRotation(t *testing.T) {
	rg := "logic-rotation-rg"
	wfName := "logic-rotation-workflow"
	ensureRG(t, rg)

	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	definition := map[string]any{
		"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
		"contentVersion": "1.0.0.0",
		"triggers": map[string]any{
			"manual": map[string]any{
				"type": "Request",
				"kind": "Http",
			},
		},
		"actions":         map[string]any{},
		"outputs":         map[string]any{},
		"triggersOutputs": map[string]any{},
	}
	createWorkflow := func() {
		_, err := workflows.CreateOrUpdate(ctx, rg, wfName, armlogic.Workflow{
			Location:   to.Ptr("eastus"),
			Properties: &armlogic.WorkflowProperties{Definition: definition},
		}, nil)
		require.NoError(t, err)
	}
	createWorkflow()

	initial, err := workflows.ListCallbackURL(ctx, rg, wfName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.Value)
	sigBefore := logicCallbackSig(t, *initial.Value)

	// A repeated read is stable — only a rotation changes the signature.
	stable, err := workflows.ListCallbackURL(ctx, rg, wfName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.Equal(t, sigBefore, logicCallbackSig(t, *stable.Value),
		"listCallbackUrl must be stable between rotations")

	_, err = workflows.RegenerateAccessKey(ctx, rg, wfName, armlogic.RegenerateActionParameter{
		KeyType: to.Ptr(armlogic.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)

	rotated, err := workflows.ListCallbackURL(ctx, rg, wfName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	sigRotated := logicCallbackSig(t, *rotated.Value)
	assert.NotEqual(t, sigBefore, sigRotated,
		"regenerating the access key must change the callback URL signature")

	// A second rotation changes it again.
	_, err = workflows.RegenerateAccessKey(ctx, rg, wfName, armlogic.RegenerateActionParameter{
		KeyType: to.Ptr(armlogic.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	rotatedAgain, err := workflows.ListCallbackURL(ctx, rg, wfName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, sigRotated, logicCallbackSig(t, *rotatedAgain.Value),
		"a second regenerate must change the callback URL signature again")

	// The trigger-scoped callback is signed with the same workflow access key,
	// so it rotates with the workflow.
	triggers, err := armlogic.NewWorkflowTriggersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	triggerCb, err := triggers.ListCallbackURL(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)
	triggerSig := logicCallbackSig(t, *triggerCb.Value)
	_, err = workflows.RegenerateAccessKey(ctx, rg, wfName, armlogic.RegenerateActionParameter{
		KeyType: to.Ptr(armlogic.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	triggerCbAfter, err := triggers.ListCallbackURL(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)
	assert.NotEqual(t, triggerSig, logicCallbackSig(t, *triggerCbAfter.Value),
		"the trigger callback signature must follow the workflow's access key")

	// Delete + recreate under the same name starts from fresh key material.
	_, err = workflows.Delete(ctx, rg, wfName, nil)
	require.NoError(t, err)
	createWorkflow()
	fresh, err := workflows.ListCallbackURL(ctx, rg, wfName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.Equal(t, sigBefore, logicCallbackSig(t, *fresh.Value),
		"a recreated workflow must sign callbacks with unrotated key material")
}

// TestLogicIntegrationAccount_RegenerateAccessKeyRotation covers the
// integration-account half of the same contract.
func TestLogicIntegrationAccount_RegenerateAccessKeyRotation(t *testing.T) {
	rg := "logic-ia-rotation-rg"
	acctName := "logic-ia-rotation"
	ensureRG(t, rg)

	accounts, err := armlogic.NewIntegrationAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createAccount := func() {
		_, err := accounts.CreateOrUpdate(ctx, rg, acctName, armlogic.IntegrationAccount{
			Location: to.Ptr("eastus"),
			SKU:      &armlogic.IntegrationAccountSKU{Name: to.Ptr(armlogic.IntegrationAccountSKUNameStandard)},
		}, nil)
		require.NoError(t, err)
	}
	createAccount()

	initial, err := accounts.ListCallbackURL(ctx, rg, acctName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.Value)
	sigBefore := logicCallbackSig(t, *initial.Value)

	_, err = accounts.RegenerateAccessKey(ctx, rg, acctName, armlogic.RegenerateActionParameter{
		KeyType: to.Ptr(armlogic.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)

	rotated, err := accounts.ListCallbackURL(ctx, rg, acctName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, sigBefore, logicCallbackSig(t, *rotated.Value),
		"regenerating the integration account access key must change the callback signature")

	// Delete + recreate under the same name starts from fresh key material.
	_, err = accounts.Delete(ctx, rg, acctName, nil)
	require.NoError(t, err)
	createAccount()
	fresh, err := accounts.ListCallbackURL(ctx, rg, acctName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.Equal(t, sigBefore, logicCallbackSig(t, *fresh.Value),
		"a recreated integration account must sign callbacks with unrotated key material")
	assert.True(t, strings.Contains(*fresh.Value, "sig="),
		"the integration account callback URL must carry its signature in the query string")
}
