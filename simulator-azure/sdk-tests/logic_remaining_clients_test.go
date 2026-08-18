package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logic_remaining_clients_test.go closes the Microsoft.Logic routes that the
// other Logic Apps SDK tests leave undriven: the integration-account partner /
// agreement / certificate artifacts, the workflow-version trigger callback URL,
// the integration-service-environment SKU and managed-API-operation catalogs,
// and the run-action repetition / request-history / scope-repetition /
// run-operation reads. Each is exercised through the official armlogic client
// that owns it, so every route registered in simulator-azure/logicapps_more.go
// has a real SDK caller.

// TestLogicApps_IntegrationAccountArtifactsSDK covers the partner, agreement,
// and certificate artifacts under an integration account.
func TestLogicApps_IntegrationAccountArtifactsSDK(t *testing.T) {
	rg := "logic-ia-artifacts-rg"
	iaName := "logic-ia-artifacts"
	cred := &fakeCredential{}

	accounts, err := armlogic.NewIntegrationAccountsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	partners, err := armlogic.NewIntegrationAccountPartnersClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	agreements, err := armlogic.NewIntegrationAccountAgreementsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	certificates, err := armlogic.NewIntegrationAccountCertificatesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = accounts.CreateOrUpdate(ctx, rg, iaName, armlogic.IntegrationAccount{
		Location:   to.Ptr("eastus"),
		SKU:        &armlogic.IntegrationAccountSKU{Name: to.Ptr(armlogic.IntegrationAccountSKUNameStandard)},
		Properties: &armlogic.IntegrationAccountProperties{State: to.Ptr(armlogic.WorkflowStateEnabled)},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = accounts.Delete(ctx, rg, iaName, nil) })

	// ---- partners ----
	hostPartnerContent := &armlogic.PartnerContent{
		B2B: &armlogic.B2BPartnerContent{
			BusinessIdentities: []*armlogic.BusinessIdentity{
				{Qualifier: to.Ptr("ZZ"), Value: to.Ptr("HOST")},
			},
		},
	}
	_, err = partners.CreateOrUpdate(ctx, rg, iaName, "hostPartner", armlogic.IntegrationAccountPartner{
		Properties: &armlogic.IntegrationAccountPartnerProperties{
			PartnerType: to.Ptr(armlogic.PartnerTypeB2B),
			Content:     hostPartnerContent,
		},
	}, nil)
	require.NoError(t, err)
	gotPartner, err := partners.Get(ctx, rg, iaName, "hostPartner", nil)
	require.NoError(t, err)
	assert.Equal(t, "hostPartner", ptrVal(gotPartner.Name))

	guestPartnerContent := &armlogic.PartnerContent{
		B2B: &armlogic.B2BPartnerContent{
			BusinessIdentities: []*armlogic.BusinessIdentity{
				{Qualifier: to.Ptr("ZZ"), Value: to.Ptr("GUEST")},
			},
		},
	}
	_, err = partners.CreateOrUpdate(ctx, rg, iaName, "guestPartner", armlogic.IntegrationAccountPartner{
		Properties: &armlogic.IntegrationAccountPartnerProperties{
			PartnerType: to.Ptr(armlogic.PartnerTypeB2B),
			Content:     guestPartnerContent,
		},
	}, nil)
	require.NoError(t, err)

	partnerPager := partners.NewListPager(rg, iaName, nil)
	require.True(t, partnerPager.More())
	partnerPage, err := partnerPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, partnerPage.Value)

	partnerCb, err := partners.ListContentCallbackURL(ctx, rg, iaName, "hostPartner",
		armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(partnerCb.Value))

	// ---- agreements ----
	_, err = agreements.CreateOrUpdate(ctx, rg, iaName, "agreement1", armlogic.IntegrationAccountAgreement{
		Properties: &armlogic.IntegrationAccountAgreementProperties{
			AgreementType: to.Ptr(armlogic.AgreementTypeX12),
			HostPartner:   to.Ptr("hostPartner"),
			GuestPartner:  to.Ptr("guestPartner"),
			HostIdentity:  &armlogic.BusinessIdentity{Qualifier: to.Ptr("ZZ"), Value: to.Ptr("HOST")},
			GuestIdentity: &armlogic.BusinessIdentity{Qualifier: to.Ptr("ZZ"), Value: to.Ptr("GUEST")},
			Content:       &armlogic.AgreementContent{},
			Metadata:      map[string]any{"owner": "sockerless"},
		},
	}, nil)
	require.NoError(t, err)
	gotAgreement, err := agreements.Get(ctx, rg, iaName, "agreement1", nil)
	require.NoError(t, err)
	assert.Equal(t, "agreement1", ptrVal(gotAgreement.Name))

	agreementPager := agreements.NewListPager(rg, iaName, nil)
	require.True(t, agreementPager.More())
	agreementPage, err := agreementPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agreementPage.Value)

	agreementCb, err := agreements.ListContentCallbackURL(ctx, rg, iaName, "agreement1",
		armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(agreementCb.Value))

	// ---- certificates ----
	_, err = certificates.CreateOrUpdate(ctx, rg, iaName, "cert1", armlogic.IntegrationAccountCertificate{
		Properties: &armlogic.IntegrationAccountCertificateProperties{
			PublicCertificate: to.Ptr("MIIB"),
		},
	}, nil)
	require.NoError(t, err)
	gotCert, err := certificates.Get(ctx, rg, iaName, "cert1", nil)
	require.NoError(t, err)
	assert.Equal(t, "cert1", ptrVal(gotCert.Name))

	certPager := certificates.NewListPager(rg, iaName, nil)
	require.True(t, certPager.More())
	certPage, err := certPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, certPage.Value)

	// ---- deletes ----
	_, err = certificates.Delete(ctx, rg, iaName, "cert1", nil)
	require.NoError(t, err)
	_, err = agreements.Delete(ctx, rg, iaName, "agreement1", nil)
	require.NoError(t, err)
	_, err = partners.Delete(ctx, rg, iaName, "hostPartner", nil)
	require.NoError(t, err)
	_, err = partners.Delete(ctx, rg, iaName, "guestPartner", nil)
	require.NoError(t, err)
}

// TestLogicApps_WorkflowVersionTriggerCallbackSDK covers the workflow-version
// trigger callback URL — the versions/{versionId}/triggers/{name}/listCallbackUrl
// route, which is a distinct client from the live-trigger callback URL.
func TestLogicApps_WorkflowVersionTriggerCallbackSDK(t *testing.T) {
	rg := "logic-verstrig-rg"
	wfName := "logic-verstrig-wf"
	cred := &fakeCredential{}

	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	versions, err := armlogic.NewWorkflowVersionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	versionTriggers, err := armlogic.NewWorkflowVersionTriggersClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	definition := map[string]any{
		"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
		"contentVersion": "1.0.0.0",
		"triggers":       map[string]any{"manual": map[string]any{"type": "Request", "kind": "Http"}},
		"actions":        map[string]any{"compose": map[string]any{"type": "Compose", "inputs": "hello"}},
		"outputs":        map[string]any{},
	}
	_, err = workflows.CreateOrUpdate(ctx, rg, wfName, armlogic.Workflow{
		Location:   to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{Definition: definition},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = workflows.Delete(ctx, rg, wfName, nil) })

	verPager := versions.NewListPager(rg, wfName, nil)
	require.True(t, verPager.More())
	verPage, err := verPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, verPage.Value)
	versionID := ptrVal(verPage.Value[0].Name)

	cb, err := versionTriggers.ListCallbackURL(ctx, rg, wfName, versionID, "manual",
		&armlogic.WorkflowVersionTriggersClientListCallbackURLOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(cb.Value),
		"a workflow-version trigger callback URL must be returned")
}

// TestLogicApps_ServiceEnvironmentCatalogsSDK covers the integration-service-
// environment SKU catalog and the managed-API operation catalog.
func TestLogicApps_ServiceEnvironmentCatalogsSDK(t *testing.T) {
	rg := "logic-ise-catalog-rg"
	iseName := "logic-ise-catalog"
	cred := &fakeCredential{}

	envs, err := armlogic.NewIntegrationServiceEnvironmentsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	skus, err := armlogic.NewIntegrationServiceEnvironmentSKUsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	managedAPIs, err := armlogic.NewIntegrationServiceEnvironmentManagedApisClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	apiOps, err := armlogic.NewIntegrationServiceEnvironmentManagedAPIOperationsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	createPoller, err := envs.BeginCreateOrUpdate(ctx, rg, iseName, armlogic.IntegrationServiceEnvironment{
		Location: to.Ptr("eastus"),
		SKU: &armlogic.IntegrationServiceEnvironmentSKU{
			Name: to.Ptr(armlogic.IntegrationServiceEnvironmentSKUNameDeveloper),
		},
		Properties: &armlogic.IntegrationServiceEnvironmentProperties{},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = envs.Delete(ctx, rg, iseName, nil) })

	// SKU catalog.
	skuPager := skus.NewListPager(rg, iseName, nil)
	require.True(t, skuPager.More())
	_, err = skuPager.NextPage(ctx)
	require.NoError(t, err)

	// Managed APIs, then the operation catalog for one of them.
	const apiName = "servicebus"
	putPoller, err := managedAPIs.BeginPut(ctx, rg, iseName, apiName,
		armlogic.IntegrationServiceEnvironmentManagedAPI{}, nil)
	require.NoError(t, err)
	_, err = putPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	apiPager := managedAPIs.NewListPager(rg, iseName, nil)
	require.True(t, apiPager.More())
	_, err = apiPager.NextPage(ctx)
	require.NoError(t, err)

	opPager := apiOps.NewListPager(rg, iseName, apiName, nil)
	require.True(t, opPager.More())
	_, err = opPager.NextPage(ctx)
	require.NoError(t, err)
}

// TestLogicApps_RunActionSubReadsSDK covers the run-action repetition,
// request-history, scope-repetition, and run-operation reads. The simulator
// runs a workflow to completion in one step, so these collections are empty and
// their item reads are 404s — the assertions pin exactly that, which is the
// contract a client sees for a run with no repeated or scoped actions.
func TestLogicApps_RunActionSubReadsSDK(t *testing.T) {
	rg := "logic-runsub-rg"
	wfName := "logic-runsub-wf"
	cred := &fakeCredential{}

	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	triggers, err := armlogic.NewWorkflowTriggersClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runs, err := armlogic.NewWorkflowRunsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runActions, err := armlogic.NewWorkflowRunActionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	repetitions, err := armlogic.NewWorkflowRunActionRepetitionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	reqHistories, err := armlogic.NewWorkflowRunActionRequestHistoriesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	repReqHistories, err := armlogic.NewWorkflowRunActionRepetitionsRequestHistoriesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	scopeReps, err := armlogic.NewWorkflowRunActionScopeRepetitionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runOps, err := armlogic.NewWorkflowRunOperationsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	definition := map[string]any{
		"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
		"contentVersion": "1.0.0.0",
		"triggers":       map[string]any{"manual": map[string]any{"type": "Request", "kind": "Http"}},
		"actions":        map[string]any{"compose": map[string]any{"type": "Compose", "inputs": "hello"}},
		"outputs":        map[string]any{},
	}
	_, err = workflows.CreateOrUpdate(ctx, rg, wfName, armlogic.Workflow{
		Location:   to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{Definition: definition},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = workflows.Delete(ctx, rg, wfName, nil) })

	_, err = triggers.Run(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)

	runPager := runs.NewListPager(rg, wfName, nil)
	require.True(t, runPager.More())
	runPage, err := runPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, runPage.Value)
	runName := ptrVal(runPage.Value[0].Name)

	actPager := runActions.NewListPager(rg, wfName, runName, nil)
	require.True(t, actPager.More())
	actPage, err := actPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, actPage.Value)
	actionName := ptrVal(actPage.Value[0].Name)

	// Repetition collections list empty for a single-pass action.
	repPager := repetitions.NewListPager(rg, wfName, runName, actionName, nil)
	require.True(t, repPager.More())
	repPage, err := repPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, repPage.Value)

	scopePager := scopeReps.NewListPager(rg, wfName, runName, actionName, nil)
	require.True(t, scopePager.More())
	scopePage, err := scopePager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, scopePage.Value)

	reqPager := reqHistories.NewListPager(rg, wfName, runName, actionName, nil)
	require.True(t, reqPager.More())
	reqPage, err := reqPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, reqPage.Value)

	// The repetition-scoped request-history collection is a distinct route from
	// the action-scoped one above.
	repReqPager := repReqHistories.NewListPager(rg, wfName, runName, actionName, "000000", nil)
	require.True(t, repReqPager.More())
	repReqPage, err := repReqPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, repReqPage.Value)

	// Expression traces for a repetition list empty too.
	tracePager := repetitions.NewListExpressionTracesPager(rg, wfName, runName, actionName, "000000", nil)
	require.True(t, tracePager.More())
	_, err = tracePager.NextPage(ctx)
	require.NoError(t, err)

	// Item reads under those empty collections are 404s. Each is held to the
	// refusal it names rather than to "an error happened": a transport fault,
	// a 500 and a deserialisation failure all satisfy a bare error assertion,
	// and none of them shows the service looked the item up.
	_, err = repetitions.Get(ctx, rg, wfName, runName, actionName, "000000", nil)
	requireAzureNotFound(t, err, "a repetition that does not exist")

	_, err = scopeReps.Get(ctx, rg, wfName, runName, actionName, "000000", nil)
	requireAzureNotFound(t, err, "a scope repetition that does not exist")

	_, err = reqHistories.Get(ctx, rg, wfName, runName, actionName, "000000", nil)
	requireAzureNotFound(t, err, "a request history that does not exist")

	_, err = repReqHistories.Get(ctx, rg, wfName, runName, actionName, "000000", "000000", nil)
	requireAzureNotFound(t, err, "a repetition request history that does not exist")

	_, err = runOps.Get(ctx, rg, wfName, runName, "00000000-0000-0000-0000-000000000000", nil)
	requireAzureNotFound(t, err, "a run operation that does not exist")
}
