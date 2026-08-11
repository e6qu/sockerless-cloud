package azure_sdk_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogicApps_WorkflowExtrasSDK exercises the workflow version / trigger /
// run-action surface plus the workflow-level POST actions through armlogic, so
// every response shape (including the list pagers) is validated against the
// vendored Microsoft.Logic Swagger.
func TestLogicApps_WorkflowExtrasSDK(t *testing.T) {
	rg := "logic-more-rg"
	wfName := "logic-more-wf"
	cred := &fakeCredential{}

	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	versions, err := armlogic.NewWorkflowVersionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	triggers, err := armlogic.NewWorkflowTriggersClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	histories, err := armlogic.NewWorkflowTriggerHistoriesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runs, err := armlogic.NewWorkflowRunsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runActions, err := armlogic.NewWorkflowRunActionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	operations, err := armlogic.NewOperationsClient(cred, clientOpts())
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

	// ---- provider operations ----
	opPager := operations.NewListPager(nil)
	require.True(t, opPager.More())
	opPage, err := opPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, opPage.Value)

	// ---- versions ----
	verPager := versions.NewListPager(rg, wfName, nil)
	require.True(t, verPager.More())
	verPage, err := verPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, verPage.Value)
	versionID := ptrVal(verPage.Value[0].Name)
	gotVersion, err := versions.Get(ctx, rg, wfName, versionID, nil)
	require.NoError(t, err)
	assert.Equal(t, versionID, ptrVal(gotVersion.Name))

	// ---- triggers ----
	trgPager := triggers.NewListPager(rg, wfName, nil)
	require.True(t, trgPager.More())
	trgPage, err := trgPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, trgPage.Value)
	gotTrigger, err := triggers.Get(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)
	assert.Equal(t, "manual", ptrVal(gotTrigger.Name))
	_, err = triggers.GetSchemaJSON(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)
	cb, err := triggers.ListCallbackURL(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(cb.Value))
	_, err = triggers.Reset(ctx, rg, wfName, "manual", nil)
	require.NoError(t, err)
	_, err = triggers.SetState(ctx, rg, wfName, "manual", armlogic.SetTriggerStateActionDefinition{
		Source: &armlogic.WorkflowTriggerReference{},
	}, nil)
	require.NoError(t, err)

	// ---- run a trigger, then read its run actions + trigger history ----
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
	gotAction, err := runActions.Get(ctx, rg, wfName, runName, actionName, nil)
	require.NoError(t, err)
	assert.Equal(t, actionName, ptrVal(gotAction.Name))
	tracePager := runActions.NewListExpressionTracesPager(rg, wfName, runName, actionName, nil)
	require.True(t, tracePager.More())
	_, err = tracePager.NextPage(ctx)
	require.NoError(t, err)

	histPager := histories.NewListPager(rg, wfName, "manual", nil)
	require.True(t, histPager.More())
	histPage, err := histPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, histPage.Value)
	histName := ptrVal(histPage.Value[0].Name)
	gotHist, err := histories.Get(ctx, rg, wfName, "manual", histName, nil)
	require.NoError(t, err)
	assert.Equal(t, histName, ptrVal(gotHist.Name))
	_, err = histories.Resubmit(ctx, rg, wfName, "manual", histName, nil)
	require.NoError(t, err)

	// ---- workflow POST actions ----
	_, err = workflows.GenerateUpgradedDefinition(ctx, rg, wfName, armlogic.GenerateUpgradedDefinitionParameters{
		TargetSchemaVersion: to.Ptr("2016-06-01"),
	}, nil)
	require.NoError(t, err)
	wfCb, err := workflows.ListCallbackURL(ctx, rg, wfName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(wfCb.Value))
	_, err = workflows.ListSwagger(ctx, rg, wfName, nil)
	require.NoError(t, err)
	_, err = workflows.RegenerateAccessKey(ctx, rg, wfName, armlogic.RegenerateActionParameter{
		KeyType: to.Ptr(armlogic.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	_, err = workflows.ValidateByLocation(ctx, rg, "eastus", wfName, armlogic.Workflow{
		Location:   to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{Definition: definition},
	}, nil)
	require.NoError(t, err)

	movePoller, err := workflows.BeginMove(ctx, rg, wfName, armlogic.WorkflowReference{
		ID: to.Ptr(logicWorkflowResourceID(rg, "logic-more-wf-moved")),
	}, nil)
	require.NoError(t, err)
	_, err = movePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = workflows.Delete(ctx, rg, "logic-more-wf-moved", nil) })
	_, err = workflows.Get(ctx, rg, "logic-more-wf-moved", nil)
	require.NoError(t, err)
}

// TestLogicApps_IntegrationAccountsSDK exercises the integration account and
// its artifact collections through armlogic, including the subscription- and
// resource-group-wide list pagers and the callback-url / key actions.
func TestLogicApps_IntegrationAccountsSDK(t *testing.T) {
	rg := "logic-ia-rg"
	iaName := "logic-ia"
	cred := &fakeCredential{}

	accounts, err := armlogic.NewIntegrationAccountsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	maps, err := armlogic.NewIntegrationAccountMapsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	schemas, err := armlogic.NewIntegrationAccountSchemasClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	assemblies, err := armlogic.NewIntegrationAccountAssembliesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	batches, err := armlogic.NewIntegrationAccountBatchConfigurationsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	sessions, err := armlogic.NewIntegrationAccountSessionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = accounts.CreateOrUpdate(ctx, rg, iaName, armlogic.IntegrationAccount{
		Location:   to.Ptr("eastus"),
		SKU:        &armlogic.IntegrationAccountSKU{Name: to.Ptr(armlogic.IntegrationAccountSKUNameStandard)},
		Properties: &armlogic.IntegrationAccountProperties{State: to.Ptr(armlogic.WorkflowStateEnabled)},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = accounts.Delete(ctx, rg, iaName, nil) })

	got, err := accounts.Get(ctx, rg, iaName, nil)
	require.NoError(t, err)
	assert.Equal(t, iaName, ptrVal(got.Name))

	_, err = accounts.Update(ctx, rg, iaName, armlogic.IntegrationAccount{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"env": to.Ptr("test")},
		SKU:      &armlogic.IntegrationAccountSKU{Name: to.Ptr(armlogic.IntegrationAccountSKUNameStandard)},
	}, nil)
	require.NoError(t, err)

	rgPager := accounts.NewListByResourceGroupPager(rg, nil)
	require.True(t, rgPager.More())
	rgPage, err := rgPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rgPage.Value)

	subPager := accounts.NewListBySubscriptionPager(nil)
	foundInSub := false
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, acct := range page.Value {
			if ptrVal(acct.Name) == iaName {
				foundInSub = true
			}
		}
	}
	assert.True(t, foundInSub)

	acctCb, err := accounts.ListCallbackURL(ctx, rg, iaName, armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(acctCb.Value))
	kvPager := accounts.NewListKeyVaultKeysPager(rg, iaName, armlogic.ListKeyVaultKeysDefinition{
		KeyVault: &armlogic.KeyVaultReference{ID: to.Ptr("/subscriptions/x/kv")},
	}, nil)
	require.True(t, kvPager.More())
	_, err = kvPager.NextPage(ctx)
	require.NoError(t, err)
	_, err = accounts.RegenerateAccessKey(ctx, rg, iaName, armlogic.RegenerateActionParameter{
		KeyType: to.Ptr(armlogic.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)

	// ---- map ----
	_, err = maps.CreateOrUpdate(ctx, rg, iaName, "map1", armlogic.IntegrationAccountMap{
		Properties: &armlogic.IntegrationAccountMapProperties{
			MapType:     to.Ptr(armlogic.MapTypeXslt),
			ContentType: to.Ptr("application/xml"),
			Content:     to.Ptr("<xsl/>"),
		},
	}, nil)
	require.NoError(t, err)
	gotMap, err := maps.Get(ctx, rg, iaName, "map1", nil)
	require.NoError(t, err)
	assert.Equal(t, "map1", ptrVal(gotMap.Name))
	mapPager := maps.NewListPager(rg, iaName, nil)
	require.True(t, mapPager.More())
	_, err = mapPager.NextPage(ctx)
	require.NoError(t, err)
	mapCb, err := maps.ListContentCallbackURL(ctx, rg, iaName, "map1", armlogic.GetCallbackURLParameters{}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(mapCb.Value))
	_, err = maps.Delete(ctx, rg, iaName, "map1", nil)
	require.NoError(t, err)

	// ---- schema ----
	_, err = schemas.CreateOrUpdate(ctx, rg, iaName, "schema1", armlogic.IntegrationAccountSchema{
		Properties: &armlogic.IntegrationAccountSchemaProperties{
			SchemaType:  to.Ptr(armlogic.SchemaTypeXML),
			ContentType: to.Ptr("application/xml"),
			Content:     to.Ptr("<xsd/>"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = schemas.Get(ctx, rg, iaName, "schema1", nil)
	require.NoError(t, err)
	schemaPager := schemas.NewListPager(rg, iaName, nil)
	require.True(t, schemaPager.More())
	_, err = schemaPager.NextPage(ctx)
	require.NoError(t, err)

	// ---- assembly ----
	_, err = assemblies.CreateOrUpdate(ctx, rg, iaName, "assembly1", armlogic.AssemblyDefinition{
		Properties: &armlogic.AssemblyProperties{AssemblyName: to.Ptr("MyAssembly")},
	}, nil)
	require.NoError(t, err)
	_, err = assemblies.Get(ctx, rg, iaName, "assembly1", nil)
	require.NoError(t, err)
	asmPager := assemblies.NewListPager(rg, iaName, nil)
	require.True(t, asmPager.More())
	_, err = asmPager.NextPage(ctx)
	require.NoError(t, err)
	asmCb, err := assemblies.ListContentCallbackURL(ctx, rg, iaName, "assembly1", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(asmCb.Value))

	// ---- batch configuration ----
	_, err = batches.CreateOrUpdate(ctx, rg, iaName, "batch1", armlogic.BatchConfiguration{
		Properties: &armlogic.BatchConfigurationProperties{
			BatchGroupName:  to.Ptr("BatchGroup"),
			ReleaseCriteria: &armlogic.BatchReleaseCriteria{MessageCount: to.Ptr[int32](10)},
		},
	}, nil)
	require.NoError(t, err)
	_, err = batches.Get(ctx, rg, iaName, "batch1", nil)
	require.NoError(t, err)
	batchPager := batches.NewListPager(rg, iaName, nil)
	require.True(t, batchPager.More())
	_, err = batchPager.NextPage(ctx)
	require.NoError(t, err)

	// ---- session ----
	_, err = sessions.CreateOrUpdate(ctx, rg, iaName, "session1", armlogic.IntegrationAccountSession{
		Properties: &armlogic.IntegrationAccountSessionProperties{Content: map[string]any{"controlNumber": "1234"}},
	}, nil)
	require.NoError(t, err)
	_, err = sessions.Get(ctx, rg, iaName, "session1", nil)
	require.NoError(t, err)
	sessPager := sessions.NewListPager(rg, iaName, nil)
	require.True(t, sessPager.More())
	_, err = sessPager.NextPage(ctx)
	require.NoError(t, err)

	// ---- partner / agreement / certificate via raw ARM (compact bodies) ----
	logicExerciseIAPartnerAgreementCert(t, rg, iaName)
}

// TestLogicApps_ServiceEnvironmentsSDK exercises the integration service
// environment long-running operations and its managed-API child collection.
func TestLogicApps_ServiceEnvironmentsSDK(t *testing.T) {
	rg := "logic-ise-rg"
	iseName := "logic-ise"
	cred := &fakeCredential{}

	envs, err := armlogic.NewIntegrationServiceEnvironmentsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	managed, err := armlogic.NewIntegrationServiceEnvironmentManagedApisClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	createPoller, err := envs.BeginCreateOrUpdate(ctx, rg, iseName, armlogic.IntegrationServiceEnvironment{
		Location:   to.Ptr("eastus"),
		SKU:        &armlogic.IntegrationServiceEnvironmentSKU{Name: to.Ptr(armlogic.IntegrationServiceEnvironmentSKUNameDeveloper), Capacity: to.Ptr[int32](1)},
		Properties: &armlogic.IntegrationServiceEnvironmentProperties{State: to.Ptr(armlogic.WorkflowStateEnabled)},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, armlogic.WorkflowProvisioningStateSucceeded, ptrVal(created.Properties.ProvisioningState))
	t.Cleanup(func() { _, _ = envs.Delete(ctx, rg, iseName, nil) })

	got, err := envs.Get(ctx, rg, iseName, nil)
	require.NoError(t, err)
	assert.Equal(t, iseName, ptrVal(got.Name))

	updatePoller, err := envs.BeginUpdate(ctx, rg, iseName, armlogic.IntegrationServiceEnvironment{
		Tags: map[string]*string{"env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	_, err = updatePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	rgPager := envs.NewListByResourceGroupPager(rg, nil)
	require.True(t, rgPager.More())
	_, err = rgPager.NextPage(ctx)
	require.NoError(t, err)

	subPager := envs.NewListBySubscriptionPager(nil)
	foundInSub := false
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, e := range page.Value {
			if ptrVal(e.Name) == iseName {
				foundInSub = true
			}
		}
	}
	assert.True(t, foundInSub)

	_, err = envs.Restart(ctx, rg, iseName, nil)
	require.NoError(t, err)

	// ---- managed API (LRO put/delete + list/get) ----
	putPoller, err := managed.BeginPut(ctx, rg, iseName, "servicebus", armlogic.IntegrationServiceEnvironmentManagedAPI{}, nil)
	require.NoError(t, err)
	_, err = putPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	gotAPI, err := managed.Get(ctx, rg, iseName, "servicebus", nil)
	require.NoError(t, err)
	assert.Equal(t, "servicebus", ptrVal(gotAPI.Name))
	apiPager := managed.NewListPager(rg, iseName, nil)
	require.True(t, apiPager.More())
	_, err = apiPager.NextPage(ctx)
	require.NoError(t, err)
	delPoller, err := managed.BeginDelete(ctx, rg, iseName, "servicebus", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// ---- ISE network health + skus list (raw ARM; no SDK client method for these) ----
	logicExerciseISEHealthAndSkus(t, rg, iseName)
}

func logicWorkflowResourceID(rg, name string) string {
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Logic/workflows/" + name
}

func logicIntegrationAccountBase(rg, iaName string) string {
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Logic/integrationAccounts/" + iaName
}

func logicExerciseIAPartnerAgreementCert(t *testing.T, rg, iaName string) {
	t.Helper()
	base := logicIntegrationAccountBase(rg, iaName)
	bizID := `{"qualifier":"AS2Identity","value":"PartnerA"}`

	mustStatus := func(method, path, body string, want int) []byte {
		t.Helper()
		resp := armReq(t, method, path, body)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, want, resp.StatusCode, "%s %s -> %s", method, path, string(b))
		return b
	}

	// partner
	mustStatus("PUT", base+"/partners/partnerA", `{"properties":{"partnerType":"B2B","content":{"b2b":{"businessIdentities":[`+bizID+`]}}}}`, http.StatusOK)
	mustStatus("GET", base+"/partners/partnerA", "", http.StatusOK)
	mustStatus("GET", base+"/partners", "", http.StatusOK)
	pcb := mustStatus("POST", base+"/partners/partnerA/listContentCallbackUrl", "{}", http.StatusOK)
	assert.Contains(t, string(pcb), `"value"`)

	// agreement
	agreement := `{"properties":{"agreementType":"AS2","hostPartner":"partnerA","guestPartner":"partnerA",` +
		`"hostIdentity":` + bizID + `,"guestIdentity":` + bizID + `,"content":{}}}`
	mustStatus("PUT", base+"/agreements/agreementA", agreement, http.StatusOK)
	mustStatus("GET", base+"/agreements/agreementA", "", http.StatusOK)
	mustStatus("GET", base+"/agreements", "", http.StatusOK)
	mustStatus("POST", base+"/agreements/agreementA/listContentCallbackUrl", "{}", http.StatusOK)

	// certificate
	mustStatus("PUT", base+"/certificates/certA", `{"properties":{"publicCertificate":"MIICert"}}`, http.StatusOK)
	mustStatus("GET", base+"/certificates/certA", "", http.StatusOK)
	mustStatus("GET", base+"/certificates", "", http.StatusOK)

	// log tracking events
	mustStatus("POST", base+"/logTrackingEvents",
		`{"sourceType":"Microsoft.Logic/workflows","events":[{"eventLevel":"Informational","eventTime":"2024-01-01T00:00:00Z","recordType":"AS2Message","record":{}}]}`,
		http.StatusOK)

	// delete partner + agreement + certificate
	mustStatus("DELETE", base+"/agreements/agreementA", "", http.StatusOK)
	mustStatus("DELETE", base+"/partners/partnerA", "", http.StatusOK)
	mustStatus("DELETE", base+"/certificates/certA", "", http.StatusOK)
}

func logicExerciseISEHealthAndSkus(t *testing.T, rg, iseName string) {
	t.Helper()
	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Logic/integrationServiceEnvironments/" + iseName
	mustStatus := func(method, path string, want int) {
		t.Helper()
		resp := armReq(t, method, path, "")
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, want, resp.StatusCode, "%s %s -> %s", method, path, string(b))
	}
	mustStatus("GET", base+"/health/network", http.StatusOK)
	mustStatus("GET", base+"/skus", http.StatusOK)
	mustStatus("GET", base+"/managedApis/servicebus/apiOperations", http.StatusOK)
}
