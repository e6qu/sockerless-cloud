package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Coverage round-trips for the Microsoft.App slice operations beyond the
// create/get/delete core: containerApps start/stop/update/list-by-
// subscription/getAuthToken/listCustomHostNameAnalysis, jobs update/
// list-by-subscription/stop-multiple-executions, and the
// managedEnvironments list/update/getAuthToken/workloadProfileStates/
// checkNameAvailability surfaces plus the certificates and
// managedCertificates sub-resources. Each exercises the same SDK client
// the aca / azure-functions backends and terraform-provider-azurerm use.

func acaEnvID(rg, env string) string {
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/" + env
}

func newACAApp(t *testing.T, client *armappcontainers.ContainerAppsClient, rg, name, envID string) {
	t.Helper()
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{Name: to.Ptr("main"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
				},
				Scale: &armappcontainers.Scale{
					MinReplicas: to.Ptr[int32](1),
					MaxReplicas: to.Ptr[int32](1),
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func ensureManagedEnv(t *testing.T, rg, env string) {
	t.Helper()
	cred := &fakeCredential{}
	client, err := armappcontainers.NewManagedEnvironmentsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, env, armappcontainers.ManagedEnvironment{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ManagedEnvironmentProperties{
			WorkloadProfiles: []*armappcontainers.WorkloadProfile{
				{Name: to.Ptr("Consumption"), WorkloadProfileType: to.Ptr("Consumption")},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestSDK_ContainerApps_StartStopUpdate(t *testing.T) {
	rg := "sdk-aca-startstop-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	newACAApp(t, client, rg, "sdk-startstop-app", acaEnvID(rg, "sim-env"))
	defer func() {
		if dp, derr := client.BeginDelete(ctx, rg, "sdk-startstop-app", nil); derr == nil {
			_, _ = dp.PollUntilDone(ctx, nil)
		}
	}()

	// BeginStop → BeginStart round-trip.
	stopP, err := client.BeginStop(ctx, rg, "sdk-startstop-app", nil)
	require.NoError(t, err)
	stopped, err := stopP.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, stopped.Name)
	assert.Equal(t, "sdk-startstop-app", *stopped.Name)

	startP, err := client.BeginStart(ctx, rg, "sdk-startstop-app", nil)
	require.NoError(t, err)
	started, err := startP.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, started.Properties)
	require.NotNil(t, started.Properties.ProvisioningState)
	assert.Equal(t, "Succeeded", string(*started.Properties.ProvisioningState))

	// BeginUpdate (PATCH) — change tags.
	upP, err := client.BeginUpdate(ctx, rg, "sdk-startstop-app", armappcontainers.ContainerApp{
		Tags: map[string]*string{"team": to.Ptr("sockerless")},
	}, nil)
	require.NoError(t, err)
	_, err = upP.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := client.Get(ctx, rg, "sdk-startstop-app", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Tags["team"])
	assert.Equal(t, "sockerless", *got.Tags["team"])
}

func TestSDK_ContainerApps_ListBySubscription_AuthToken_HostnameAnalysis(t *testing.T) {
	rg := "sdk-aca-listsub-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	newACAApp(t, client, rg, "sdk-listsub-app", acaEnvID(rg, "sim-env"))
	defer func() {
		if dp, derr := client.BeginDelete(ctx, rg, "sdk-listsub-app", nil); derr == nil {
			_, _ = dp.PollUntilDone(ctx, nil)
		}
	}()

	// List by subscription.
	pager := client.NewListBySubscriptionPager(nil)
	found := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, a := range page.Value {
			if a.Name != nil && *a.Name == "sdk-listsub-app" {
				found = true
			}
		}
	}
	assert.True(t, found, "list-by-subscription must include the created app")

	// getAuthToken.
	tok, err := client.GetAuthToken(ctx, rg, "sdk-listsub-app", nil)
	require.NoError(t, err)
	require.NotNil(t, tok.Properties)
	require.NotNil(t, tok.Properties.Token)
	assert.NotEmpty(t, *tok.Properties.Token)

	// listCustomHostNameAnalysis.
	analysis, err := client.ListCustomHostNameAnalysis(ctx, rg, "sdk-listsub-app",
		&armappcontainers.ContainerAppsClientListCustomHostNameAnalysisOptions{
			CustomHostname: to.Ptr("app.example.com"),
		})
	require.NoError(t, err)
	require.NotNil(t, analysis.HostName)
	assert.Equal(t, "app.example.com", *analysis.HostName)
}

func TestSDK_Jobs_UpdateListStopMultiple(t *testing.T) {
	rg := "sdk-aca-jobops-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	mkJob := func() armappcontainers.Job {
		return armappcontainers.Job{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.JobProperties{
				EnvironmentID: to.Ptr(acaEnvID(rg, "sim-env")),
				Configuration: &armappcontainers.JobConfiguration{
					TriggerType:       to.Ptr(armappcontainers.TriggerTypeManual),
					ReplicaTimeout:    to.Ptr[int32](60),
					ReplicaRetryLimit: to.Ptr[int32](0),
					ManualTriggerConfig: &armappcontainers.JobConfigurationManualTriggerConfig{
						Parallelism:            to.Ptr[int32](1),
						ReplicaCompletionCount: to.Ptr[int32](1),
					},
				},
				Template: &armappcontainers.JobTemplate{
					Containers: []*armappcontainers.Container{
						{Name: to.Ptr("main"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
					},
				},
			},
		}
	}

	cp, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-jobops-job", mkJob(), nil)
	require.NoError(t, err)
	_, err = cp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if dp, derr := client.BeginDelete(ctx, rg, "sdk-jobops-job", nil); derr == nil {
			_, _ = dp.PollUntilDone(ctx, nil)
		}
	}()

	// BeginUpdate (PATCH).
	up, err := client.BeginUpdate(ctx, rg, "sdk-jobops-job", armappcontainers.JobPatchProperties{
		Tags: map[string]*string{"owner": to.Ptr("sockerless")},
	}, nil)
	require.NoError(t, err)
	_, err = up.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	got, err := client.Get(ctx, rg, "sdk-jobops-job", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Tags["owner"])
	assert.Equal(t, "sockerless", *got.Tags["owner"])

	// List by subscription.
	pager := client.NewListBySubscriptionPager(nil)
	found := false
	for pager.More() {
		page, perr := pager.NextPage(ctx)
		require.NoError(t, perr)
		for _, j := range page.Value {
			if j.Name != nil && *j.Name == "sdk-jobops-job" {
				found = true
			}
		}
	}
	assert.True(t, found, "list-by-subscription must include the created job")

	// BeginStopMultipleExecutions — returns the (possibly empty) collection
	// of executions requested to stop. With no running executions the
	// collection is empty; the call must still succeed.
	sp, err := client.BeginStopMultipleExecutions(ctx, rg, "sdk-jobops-job", nil)
	require.NoError(t, err)
	res, err := sp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.NotNil(t, res.Value)
}

func TestSDK_ManagedEnvironments_ListUpdateAuthTokenWorkloadProfiles(t *testing.T) {
	rg := "sdk-aca-envops-rg"
	env := "sdk-envops-env"
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, env)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewManagedEnvironmentsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// List by resource group.
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	foundRG := false
	for rgPager.More() {
		page, perr := rgPager.NextPage(ctx)
		require.NoError(t, perr)
		for _, e := range page.Value {
			if e.Name != nil && *e.Name == env {
				foundRG = true
			}
		}
	}
	assert.True(t, foundRG, "list-by-resource-group must include the env")

	// List by subscription.
	subPager := client.NewListBySubscriptionPager(nil)
	foundSub := false
	for subPager.More() {
		page, perr := subPager.NextPage(ctx)
		require.NoError(t, perr)
		for _, e := range page.Value {
			if e.Name != nil && *e.Name == env {
				foundSub = true
			}
		}
	}
	assert.True(t, foundSub, "list-by-subscription must include the env")

	// BeginUpdate (PATCH).
	up, err := client.BeginUpdate(ctx, rg, env, armappcontainers.ManagedEnvironment{
		Tags: map[string]*string{"tier": to.Ptr("prod")},
	}, nil)
	require.NoError(t, err)
	_, err = up.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	got, err := client.Get(ctx, rg, env, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Tags["tier"])
	assert.Equal(t, "prod", *got.Tags["tier"])

	// getAuthToken.
	tok, err := client.GetAuthToken(ctx, rg, env, nil)
	require.NoError(t, err)
	require.NotNil(t, tok.Properties)
	require.NotNil(t, tok.Properties.Token)
	assert.NotEmpty(t, *tok.Properties.Token)

	// workloadProfileStates.
	wpPager := client.NewListWorkloadProfileStatesPager(rg, env, nil)
	foundWP := false
	for wpPager.More() {
		page, perr := wpPager.NextPage(ctx)
		require.NoError(t, perr)
		for _, s := range page.Value {
			if s.Name != nil && *s.Name == "Consumption" {
				foundWP = true
			}
		}
	}
	assert.True(t, foundWP, "workloadProfileStates must report the env's Consumption profile")
}

func TestSDK_ManagedEnvironments_CheckNameAvailability(t *testing.T) {
	rg := "sdk-aca-checkname-rg"
	env := "sdk-checkname-env"
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, env)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewNamespacesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	resp, err := client.CheckNameAvailability(ctx, rg, env, armappcontainers.CheckNameAvailabilityRequest{
		Name: to.Ptr("my-cert"),
		Type: to.Ptr("Microsoft.App/managedEnvironments/certificates"),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.NameAvailable)
	assert.True(t, *resp.NameAvailable, "an unused certificate name must be available")
}

func TestSDK_Certificates_CRUD(t *testing.T) {
	rg := "sdk-aca-cert-rg"
	env := "sdk-cert-env"
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, env)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewCertificatesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	created, err := client.CreateOrUpdate(ctx, rg, env, "sdk-cert", &armappcontainers.CertificatesClientCreateOrUpdateOptions{
		CertificateEnvelope: &armappcontainers.Certificate{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.CertificateProperties{
				CertificateKeyVaultProperties: &armappcontainers.CertificateKeyVaultProperties{
					Identity:    to.Ptr("System"),
					KeyVaultURL: to.Ptr("https://vault.vault.azure.net/secrets/cert"),
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created.Name)
	assert.Equal(t, "sdk-cert", *created.Name)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.ProvisioningState)
	assert.Equal(t, "Succeeded", string(*created.Properties.ProvisioningState))

	got, err := client.Get(ctx, rg, env, "sdk-cert", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Name)
	assert.Equal(t, "sdk-cert", *got.Name)

	updated, err := client.Update(ctx, rg, env, "sdk-cert", armappcontainers.CertificatePatch{
		Tags: map[string]*string{"env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Tags["env"])
	assert.Equal(t, "test", *updated.Tags["env"])

	pager := client.NewListPager(rg, env, nil)
	found := false
	for pager.More() {
		page, perr := pager.NextPage(ctx)
		require.NoError(t, perr)
		for _, c := range page.Value {
			if c.Name != nil && *c.Name == "sdk-cert" {
				found = true
			}
		}
	}
	assert.True(t, found, "list must include the certificate")

	_, err = client.Delete(ctx, rg, env, "sdk-cert", nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, env, "sdk-cert", nil)
	assert.Error(t, err, "Get after delete must fail")
}

func TestSDK_ManagedCertificates_CRUD(t *testing.T) {
	rg := "sdk-aca-mcert-rg"
	env := "sdk-mcert-env"
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, env)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewManagedCertificatesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	cp, err := client.BeginCreateOrUpdate(ctx, rg, env, "sdk-mcert", &armappcontainers.ManagedCertificatesClientBeginCreateOrUpdateOptions{
		ManagedCertificateEnvelope: &armappcontainers.ManagedCertificate{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.ManagedCertificateProperties{
				SubjectName:             to.Ptr("app.example.com"),
				DomainControlValidation: to.Ptr(armappcontainers.ManagedCertificateDomainControlValidationCNAME),
			},
		},
	})
	require.NoError(t, err)
	created, err := cp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Name)
	assert.Equal(t, "sdk-mcert", *created.Name)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.ProvisioningState)
	assert.Equal(t, "Succeeded", string(*created.Properties.ProvisioningState))

	got, err := client.Get(ctx, rg, env, "sdk-mcert", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Name)
	assert.Equal(t, "sdk-mcert", *got.Name)

	updated, err := client.Update(ctx, rg, env, "sdk-mcert", armappcontainers.ManagedCertificatePatch{
		Tags: map[string]*string{"env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Tags["env"])
	assert.Equal(t, "test", *updated.Tags["env"])

	pager := client.NewListPager(rg, env, nil)
	found := false
	for pager.More() {
		page, perr := pager.NextPage(ctx)
		require.NoError(t, perr)
		for _, c := range page.Value {
			if c.Name != nil && *c.Name == "sdk-mcert" {
				found = true
			}
		}
	}
	assert.True(t, found, "list must include the managed certificate")

	_, err = client.Delete(ctx, rg, env, "sdk-mcert", nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, env, "sdk-mcert", nil)
	assert.Error(t, err, "Get after delete must fail")
}
